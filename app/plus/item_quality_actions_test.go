package plus

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type typedActionTrackingFixture struct{ *fakeTrackingAPI }

func (f *typedActionTrackingFixture) LoadQuests(context.Context, string, string, string) (market.QuestSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return typedActionQuests(f.snapshot), nil
}

func (f *typedActionTrackingFixture) LoadHideout(context.Context, string, string, string) (market.HideoutSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return market.HideoutSnapshot{CharUID: f.snapshot.CharUID, GameMode: f.snapshot.GameMode, Revision: f.snapshot.Revision, Modules: []market.HideoutUpgrade{}}, nil
}

func typedActionQuests(remote market.TrackedSnapshot) market.QuestSnapshot {
	return market.QuestSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision,
		Quests: []market.Quest{{QuestID: "fir-goal", Name: "FiR goal", Status: "available"}, {QuestID: "regular-goal", Name: "Regular goal", Status: "available"}}}
}

func newTypedActionTestApp(t *testing.T, fir, regular int64) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newTrackingTestApp(t)
	a.qualityTracking = true
	a.state.ItemOptions = normalizeItemOptions(a.state.ItemOptions)
	fake.snapshot.Items[0].RequiredQuantity = 4
	fake.snapshot.Items[0].AvailableQuantity = fir + regular
	fake.snapshot.Items[0].RemainingQuantity = 4 - fir - regular
	fake.snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", QuestID: "fir-goal", Amount: 2, RemainingRequiredQuantity: 2, FIR: true},
		{Type: "quest", QuestID: "regular-goal", Amount: 2, RemainingRequiredQuantity: 2},
	}
	a.tracker = &typedActionTrackingFixture{fake}
	remote := fake.snapshot
	merged, err := localprogress.Merge(remote, typedActionQuests(remote), market.HideoutSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Modules: []market.HideoutUpgrade{}})
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, remote, merged, "")
	a.mu.Unlock()
	inv := a.GetTrackerInventory()
	state := a.SetTrackerItemQuality(inv.Profile, trackingBSGID, fir+regular, fir, regular)
	if state.Local == nil || state.Local.Error != "" {
		t.Fatalf("could not set fixture quality: %+v", state.Local)
	}
	return a, fake
}

func TestTypedActionsKeepFourOperationsAndPersistTheirQuality(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	a.AddRegularTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 1 || !state.Tracking.RemoveRegularAllowed || state.Tracking.RemoveFIRAllowed {
		t.Fatalf("Regular add acquired the wrong quality: state=%+v tracking=%+v", state, state.Tracking)
	}
	a.AddFIRTrackedItem(trackingItemUID)
	state = waitPendingForTest(t, a)
	if state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 1 {
		t.Fatalf("FiR add changed Regular stock: %+v", state.Tracking)
	}
	a.RemoveRegularTrackedItem(trackingItemUID)
	state = waitPendingForTest(t, a)
	if state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 0 || state.Tracking.RemoveRegularAllowed {
		t.Fatalf("Regular removal changed FiR stock: %+v", state.Tracking)
	}
	a.RemoveFIRTrackedItem(trackingItemUID)
	state = waitPendingForTest(t, a)
	if state.Tracking.AvailableQuantity != 0 || state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 0 || state.PendingAdds.Count != 0 {
		t.Fatalf("typed actions failed to return to zero: %+v", state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 4 || writes[0].Quantity != 1 || writes[1].Quantity != 2 || writes[2].Quantity != 1 || writes[3].Quantity != 0 {
		t.Fatalf("typed actions did not write four absolute stock values: %+v", writes)
	}
	a.qualities = nil
	inv := a.GetTrackerInventory()
	if inv.Error != "" || len(inv.Rows) != 0 {
		t.Fatalf("persisted zero quality did not reload: %+v", inv)
	}
}

func TestTypedRemovalCannotBorrowTheOtherQuality(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 0)
	state := a.RemoveRegularTrackedItem(trackingItemUID)
	if state.Tracking.AvailableQuantity != 1 || state.Tracking.FIRQuantity != 1 || state.PendingAdds != nil && state.PendingAdds.Count != 0 {
		t.Fatalf("Regular removal borrowed a FiR piece: %+v", state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 0 {
		t.Fatalf("unavailable quality wrote to API: %+v", writes)
	}
}

func TestTypedRapidAddsAndRemovalsReserveTheSpecificQuality(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	started, release := blockFirstPendingReadForTest(t, fake)
	a.AddRegularTrackedItem(trackingItemUID)
	<-started
	state := a.AddRegularTrackedItem(trackingItemUID)
	if state.Tracking.RegularQuantity != 2 || state.Tracking.AddRegularAllowed || state.PendingAdds.Count != 2 {
		t.Fatalf("two Regular reservations did not fill its own goal: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	state = a.AddRegularTrackedItem(trackingItemUID)
	if state.PendingAdds.Count != 2 || state.Tracking.RegularQuantity != 2 || !state.Tracking.AddFIRAllowed {
		t.Fatalf("Regular overbooked a FiR-only remaining goal: %+v", state.Tracking)
	}
	a.AddFIRTrackedItem(trackingItemUID)
	state = a.AddFIRTrackedItem(trackingItemUID)
	if state.Tracking.FIRQuantity != 2 || state.Tracking.RegularQuantity != 2 || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed || state.PendingAdds.Count != 4 {
		t.Fatalf("rapid mixed quality reservations did not stop at both goals: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	a.RemoveFIRTrackedItem(trackingItemUID)
	state = a.RemoveFIRTrackedItem(trackingItemUID)
	if state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 2 || state.Tracking.RemoveFIRAllowed || !state.Tracking.RemoveRegularAllowed || state.PendingAdds.Count != 6 {
		t.Fatalf("rapid FiR removals borrowed Regular stock: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	state = a.RemoveFIRTrackedItem(trackingItemUID)
	if state.PendingAdds.Count != 6 || state.Tracking.RegularQuantity != 2 {
		t.Fatal("third FiR removal overbooked its type")
	}
	release()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 2 || state.Tracking.AvailableQuantity != 2 {
		t.Fatalf("serialized mixed changes lost their type: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 6 {
		t.Fatalf("mixed reservations did not each save once: %+v", writes)
	}
}

func TestTypedHotkeyTargetsIncludeQualityAndSuppressUnavailableActions(t *testing.T) {
	a, _ := newTypedActionTestApp(t, 1, 0)
	a.state.AddFIRHotkeyReady, a.state.AddRegularHotkeyReady = true, true
	a.state.RemoveFIRHotkeyReady, a.state.RemoveRegularHotkeyReady = true, true
	a.publishTrackedAddHotkey()
	for i := 0; i < 3; i++ {
		target := a.qualityHotkeyTargets[i].Load()
		if target == nil || !target.Typed || target.FIR != (i%2 == 0) || target.Remove != (i >= 2) || !a.trackedAddHotkeyTargetCurrent(target) {
			t.Fatalf("typed target %d lost action identity: %+v", i, target)
		}
	}
	if a.qualityHotkeyTargets[3].Load() != nil {
		t.Fatal("F4 published despite having no Regular stock")
	}
	old := a.qualityHotkeyTargets[1].Load()
	a.SetHotkeyCapture(true)
	if a.trackedAddHotkeyTargetCurrent(old) {
		t.Fatal("capture did not invalidate a queued typed target")
	}
}

func TestTypedNativeF2AddsRegularAndF3RemovesOnlyFIR(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 0)
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	a.state.AddFIRHotkeyReady, a.state.AddRegularHotkeyReady = true, true
	a.state.RemoveFIRHotkeyReady, a.state.RemoveRegularHotkeyReady = true, true
	a.publishTrackedAddHotkey()
	runTrackedHotkeyTestWorker(t, a)
	trigger := func(index int, key uint32) {
		h := &trackedAddHotkey{trigger: func() { a.queueTrackedAddHotkey(a.qualityHotkeyTargets[index].Load()) }}
		h.SetKey(qualityShortcutKeys(a.state.ItemOptions)[index])
		var keys trackedHotkeyKeys
		if !h.keyEvent(&keys, key, true, false, false) || !h.keyEvent(&keys, key, false, false, false) {
			t.Fatal("typed native key did not capture one activation")
		}
		waitTrackedHotkeyIdle(t, a)
	}
	trigger(1, 0x71)
	state := a.GetState()
	if state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 1 || state.Tracking.AvailableQuantity != 2 {
		t.Fatalf("F2 was not a Regular addition: %+v", state.Tracking)
	}
	trigger(2, 0x72)
	state = a.GetState()
	if state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 1 || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("F3 did not remove only the FiR piece: %+v", state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 2 || writes[0].Quantity != 2 || writes[1].Quantity != 1 {
		t.Fatalf("typed shortcut wrote wrong absolute quantities: %+v", writes)
	}
}

func TestTypedRefreshSettlesConfirmedQualityOnceAfterReadOutage(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read == 2 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	a.AddFIRTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || state.PendingAdds.Error == "" {
		t.Fatalf("read outage discarded pending quality: %+v", state.PendingAdds)
	}
	a.mu.Lock()
	a.localAt = time.Time{}
	key, opts, item := a.apiKey, a.state.ItemOptions, *a.state.Item
	a.mu.Unlock()
	if err := a.refreshLocalProgress(a.ctx, key, opts, item); err != nil {
		t.Fatal(err)
	}
	state = a.GetState()
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.FIRQuantity != 1 || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("refresh did not settle exactly one FiR classification: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.Tracking.FIRQuantity != 1 {
		t.Fatalf("recheck duplicated the settled classification: %+v", state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 1 {
		t.Fatalf("refresh replayed a confirmed stock write: %+v", writes)
	}
}

func TestTypedCommitFailureRetainsReadOnlyVerificationAndCommitsExactlyOnce(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	path := filepath.Join(a.dir, "item-quality.json")
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		result := fake.commitWrite(write)
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Error(err)
		}
		return result, nil
	}
	a.AddFIRTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || state.PendingAdds.Error == "" {
		t.Fatalf("a failed local commit discarded its pending operation: %+v", state.PendingAdds)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 0 {
		t.Fatalf("verification did not save exactly one FiR piece: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 1 {
		t.Fatalf("local commit retry repeated the Market write: %+v", writes)
	}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.Tracking.FIRQuantity != 1 {
		t.Fatalf("second verification duplicated quality: %+v", state.Tracking)
	}
}

func TestTypedCommitFailureUsesLatestAPITotalWithoutAddingItsReservationAgain(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	path := filepath.Join(a.dir, "item-quality.json")
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		result := fake.commitWrite(write)
		setPendingACKExternalStock(fake, 3, "external-after-own-fir-add")
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Error(err)
		}
		return result, nil
	}
	a.AddFIRTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || state.PendingAdds.Error == "" || state.Tracking.AvailableQuantity != 3 || state.Tracking.MarketAvailableQuantity != 3 {
		t.Fatalf("local commit failure overlaid +1 on latest API stock: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	a.mu.Lock()
	for _, entry := range a.pendingAddQueue {
		if delta := pendingMarketDeltaForSnapshot(entry, a.confirmedProgress); delta != 0 {
			t.Errorf("API-confirmed pending delta remained %d", delta)
		}
	}
	a.mu.Unlock()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 3 || state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 0 || state.Tracking.UncategorizedQuantity != 2 {
		t.Fatalf("local retry changed API total or classified the external pieces: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 1 {
		t.Fatalf("local retry replayed API request: %+v", writes)
	}
}
