package plus

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func waitPendingForTest(t *testing.T, a *ItemWindowApp) ItemState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		running := a.pendingAddRunning
		a.mu.Unlock()
		if !running {
			return a.GetState()
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pending Market worker did not finish or stop for verification: %+v", a.GetState().PendingAdds)
	return ItemState{}
}

func blockFirstPendingReadForTest(t *testing.T, fake *fakeTrackingAPI) (<-chan struct{}, func()) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read == 1 {
			close(started)
			<-release
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	return started, unblock
}

func TestPendingAddReturnsImmediatelyAndRapidClicksRespectCapacity(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	started, unblock := blockFirstPendingReadForTest(t, fake)
	returned := make(chan ItemState, 1)
	go func() { returned <- a.AddTrackedItem(trackingItemUID) }()
	select {
	case state := <-returned:
		if state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 0 || state.Tracking.Adding || state.PendingAdds == nil || state.PendingAdds.Count != 1 {
			t.Fatalf("first click did not return its optimistic memory reservation: %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("public Add waited for a blocked network request")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background worker did not start")
	}
	for i := 2; i <= 7; i++ {
		state := a.AddTrackedItem(trackingItemUID)
		if state.Tracking.AvailableQuantity != int64(i) || state.Tracking.MarketAvailableQuantity != 0 || state.PendingAdds.Count != i {
			t.Fatalf("rapid click %d did not reserve exactly one item: %+v", i, state)
		}
	}
	state := a.AddTrackedItem(trackingItemUID)
	if state.PendingAdds.Count != 7 || state.Tracking.AvailableQuantity != 7 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || len(fake.writes) != 0 {
		t.Fatalf("queued capacity allowed an eighth item or sent an unchecked write: %+v", state)
	}
	state.PendingAdds.Count = 999
	if a.GetPendingAddCount() != 7 || a.GetState().PendingAdds.Count != 7 {
		t.Fatal("returned pending state aliases the reservation queue")
	}
	unblock()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Running || state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 7 || state.Tracking.MarketAvailableQuantity != 7 || len(fake.writes) != 7 {
		t.Fatalf("serialized worker lost or duplicated a reservation: %+v writes=%+v", state, fake.writes)
	}
	for i, write := range fake.writes {
		if write.Quantity != int64(i+1) || write.Character != "character-a" || write.Item != trackingBSGID {
			t.Fatalf("queued operation changed target or skipped a quantity: %+v", fake.writes)
		}
	}
	files, err := os.ReadDir(a.dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("pending queue or type labels reached disk: %v %v", files, err)
	}
}

func TestPendingRejectedAddRollsBackOnlyItsReservation(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	started, unblock := blockFirstPendingReadForTest(t, fake)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressForbidden
	}
	initial := a.AddTrackedItem(trackingItemUID)
	if initial.Tracking.AvailableQuantity != 1 || initial.PendingAdds.Count != 1 {
		t.Fatal("addition was not reserved before the network result")
	}
	<-started
	unblock()
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error == "" || state.Tracking.AvailableQuantity != 0 || state.Tracking.MarketAvailableQuantity != 0 || len(fake.writes) != 1 || fake.loads != 1 {
		t.Fatalf("definite rejection retained a phantom item or retried it: %+v", state)
	}
}

func TestPendingUncertainAddRechecksOnlyGETAndKeepsExitCount(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, errors.Join(market.ErrProgressConflict, market.ErrProgressWriteUncertain)
	}
	a.AddTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || state.PendingAdds.Running || state.PendingAdds.Error == "" || state.Tracking.Adding || len(fake.writes) != 1 || fake.loads != 2 || a.GetPendingAddCount() != 1 {
		t.Fatalf("uncertain add was repeated, hidden, or left busy: %+v", state)
	}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || len(fake.writes) != 1 || fake.loads != 3 {
		t.Fatal("verification repeated a PATCH or discarded uncertainty")
	}
	fake.commitWrite(stockWrite{Character: "character-a", Item: trackingBSGID, Revision: "revision-1", Quantity: 1})
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 1 || len(fake.writes) != 1 || fake.loads != 4 {
		t.Fatalf("fresh GET did not settle the original immutable target: %+v", state)
	}
}

type pendingKeyTracker struct {
	*fakeTrackingAPI
	keys chan string
}

func (f *pendingKeyTracker) SetTrackedQuantity(ctx context.Context, key, character, item, revision string, quantity int64) (market.ProgressWriteResult, error) {
	f.keys <- key
	return f.fakeTrackingAPI.SetTrackedQuantity(ctx, key, character, item, revision, quantity)
}

func TestPendingAcceptedAddKeepsOriginalKeyProfileAndItem(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	keys := make(chan string, 1)
	a.tracker = &pendingKeyTracker{fakeTrackingAPI: fake, keys: keys}
	started, unblock := blockFirstPendingReadForTest(t, fake)
	a.AddTrackedItemFIR(trackingItemUID)
	<-started
	a.mu.Lock()
	a.apiKey = "new-account-key"
	a.state.CharacterUID, a.state.ItemOptions.CharacterUID = "character-b", "character-b"
	a.state.ItemOptions.revision++
	a.state.Item = &market.Item{UID: "new-visible-item", BSGID: "new-visible-bsg", Name: "New visible item"}
	other := market.TrackedSnapshot{CharUID: "character-b", GameMode: "PVP", Revision: "other-profile-revision", Items: []market.TrackedItem{{ItemID: "new-visible-bsg", Name: "New visible item", AvailableQuantity: 9, RequiredQuantity: 10, Sources: []market.TrackedSource{{Type: "quest", Amount: 10, RemainingRequiredQuantity: 10}}}}}
	a.applyLocalMarketStockLocked(*a.state.Item, other, "other profile")
	a.mu.Unlock()
	unblock()
	state := waitPendingForTest(t, a)
	if len(fake.writes) != 1 || fake.writes[0].Character != "character-a" || fake.writes[0].Item != trackingBSGID || <-keys != "local-test-key" {
		t.Fatalf("accepted add was redirected by the new visible target: %+v", fake.writes)
	}
	if state.CharacterUID != "character-b" || state.Item.BSGID != "new-visible-bsg" || state.Tracking.AvailableQuantity != 9 || state.PendingAdds.Count != 0 || len(a.marketKinds) != 0 {
		t.Fatalf("old account response changed current state or its session types: %+v", state)
	}
}

func TestPendingFailureRemainsVisibleAfterAnotherItemSucceeds(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	otherID, otherUID := "other-item", "other-item-uid"
	fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: otherID, Name: "Other item", RequiredQuantity: 2, Sources: []market.TrackedSource{{Type: "quest", Amount: 2, RemainingRequiredQuantity: 2}}})
	setTrackingTestSnapshot(t, a, fake.snapshot)
	started, unblock := blockFirstPendingReadForTest(t, fake)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		if write.Item == trackingBSGID {
			return market.ProgressWriteResult{}, market.ErrProgressForbidden
		}
		return fake.commitWrite(write), nil
	}
	a.AddTrackedItem(trackingItemUID)
	<-started
	a.mu.Lock()
	a.state.Item = &market.Item{UID: otherUID, BSGID: otherID, Name: "Other item"}
	a.applyLocalMarketStockLocked(*a.state.Item, fake.snapshot, "")
	a.mu.Unlock()
	a.AddTrackedItem(otherUID)
	unblock()
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error == "" || !strings.Contains(state.PendingAdds.Error, "PRO") || len(fake.writes) != 2 || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("later success concealed an earlier failed item: %+v", state)
	}
}

func TestPendingAnyCandidateAbsentFromRawItemsStaysOptimisticUntilVerified(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.snapshot.Items = []market.TrackedItem{}
	fake.snapshot.Groups = []market.TrackedGroup{{QuestID: "medical-quest", RequirementID: "medical-items", Amount: 2, RequiredQuantity: 2, RemainingQuantity: 2, ItemIDs: []string{trackingBSGID, "other-medical"}}}
	setTrackingTestSnapshot(t, a, fake.snapshot)
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read > 1 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: true, Revision: "confirmed-any-add", NotificationDelivered: true}, nil
	}
	a.AddTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 0 || state.Tracking.Adding || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 1 || len(fake.writes) != 1 {
		t.Fatalf("unreadable ACK lost the synthesized Any reservation or claimed confirmation: %+v", state)
	}
	fake.loadHook = nil
	fake.snapshot.Revision = "confirmed-any-add"
	fake.snapshot.Items = []market.TrackedItem{{ItemID: trackingBSGID, Name: "Immobilizing splint", AvailableQuantity: 1}}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 1 || len(fake.writes) != 1 {
		t.Fatalf("verified Any reservation was lost or counted twice: %+v", state)
	}
}

func TestPendingWorkerPreservesCompletionFromQuestAndHideoutGraphs(t *testing.T) {
	for _, completedKind := range []string{"quest", "hideout"} {
		t.Run(completedKind, func(t *testing.T) {
			a, base := newTrackingTestApp(t)
			done := market.TrackedSource{Type: "quest", QuestID: "completed-quest", Amount: 3, RemainingRequiredQuantity: 3}
			fake := &fakeCharacterProgress{fakeTrackingAPI: base, followTrackedRevision: true,
				quests:  market.QuestSnapshot{CharUID: "character-a", GameMode: "PVP", Quests: []market.Quest{{QuestID: "completed-quest", Completed: true}, {QuestID: "active-quest", Status: "available"}}},
				hideout: market.HideoutSnapshot{CharUID: "character-a", GameMode: "PVP", Modules: []market.HideoutUpgrade{}},
			}
			if completedKind == "hideout" {
				done.Type, done.QuestID, done.UpgradeKey = "hideout", "", "medical:1"
				fake.hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "medical:1", Level: 1, Completed: true}}
			}
			fake.snapshot.Items[0].RequiredQuantity = 8
			fake.snapshot.Items[0].Sources = []market.TrackedSource{done, {Type: "quest", QuestID: "active-quest", Amount: 5, RemainingRequiredQuantity: 5}}
			a.tracker = fake
			a.localAt = time.Time{}
			if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item); err != nil {
				t.Fatal(err)
			}
			before := a.GetState().Tracking
			if before.RequiredQuantity != 5 || len(before.CompletedSources) != 1 || before.CompletedSources[0].Quantity != 3 || fake.snapshot.Items[0].Sources[0].Completed {
				t.Fatalf("fixture did not depend on graph-only completion: %+v", before)
			}
			a.AddTrackedItem(trackingItemUID)
			state := waitPendingForTest(t, a)
			if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 1 || state.Tracking.RequiredQuantity != 5 || len(state.Tracking.CompletedSources) != 1 || state.Tracking.CompletedSources[0].Quantity != 3 || len(fake.writes) != 1 {
				t.Fatalf("background add reopened graph-completed requirements: %+v", state)
			}
			if fake.questReads < 3 || fake.hideoutReads < 3 {
				t.Fatal("worker did not validate matching quest/hideout graphs before and after its write")
			}
		})
	}
}

func TestPendingGraphRevisionMismatchStopsBeforePatch(t *testing.T) {
	a, base := newTrackingTestApp(t)
	a.tracker = &fakeCharacterProgress{fakeTrackingAPI: base,
		quests:  market.QuestSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "different-revision", Quests: []market.Quest{}},
		hideout: market.HideoutSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "revision-1", Modules: []market.HideoutUpgrade{}},
	}
	a.AddTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if len(base.writes) != 0 || state.PendingAdds.Count != 1 || state.PendingAdds.Running || state.PendingAdds.Error == "" || state.Tracking.Adding {
		t.Fatalf("incompatible quest/hideout revision reached the stock setter: %+v", state)
	}
}
