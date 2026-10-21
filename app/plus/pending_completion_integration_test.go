package plus

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type pendingCompletionTrackingAPI struct {
	*pendingProfileTrackingAPI
	completed  map[[2]string]map[string]bool
	questReads map[[2]string]int
}

func (f *pendingCompletionTrackingAPI) LoadQuests(_ context.Context, key, character, _ string) (market.QuestSnapshot, error) {
	identity := [2]string{key, character}
	f.mu.Lock()
	route := f.routes[identity]
	f.questReads[identity]++
	completed := make(map[string]bool)
	for goal, done := range f.completed[identity] {
		completed[goal] = done
	}
	f.mu.Unlock()
	if route == nil {
		return market.QuestSnapshot{}, market.ErrProgressNotFound
	}
	route.mu.Lock()
	result := typedActionQuests(route.snapshot)
	route.mu.Unlock()
	for i := range result.Quests {
		if completed[result.Quests[i].QuestID] {
			result.Quests[i].Status, result.Quests[i].Completed = "completed", true
		}
	}
	return result, nil
}

func (f *pendingCompletionTrackingAPI) LoadHideout(_ context.Context, key, character, _ string) (market.HideoutSnapshot, error) {
	f.mu.Lock()
	route := f.routes[[2]string{key, character}]
	f.mu.Unlock()
	if route == nil {
		return market.HideoutSnapshot{}, market.ErrProgressNotFound
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	return market.HideoutSnapshot{CharUID: route.snapshot.CharUID, GameMode: route.snapshot.GameMode, Revision: route.snapshot.Revision,
		Modules: []market.HideoutUpgrade{}}, nil
}

func newPendingCompletionTestApp(t *testing.T, fir, regular int64) (*ItemWindowApp, *pendingCompletionTrackingAPI, *fakeTrackingAPI) {
	t.Helper()
	a, remote := newTypedActionTestApp(t, fir, regular)
	identity := [2]string{a.apiKey, a.state.CharacterUID}
	router := &pendingProfileTrackingAPI{fakeTrackingAPI: remote, routes: map[[2]string]*fakeTrackingAPI{identity: remote},
		errors: make(map[[2]string]error), reads: make(map[[2]string]int)}
	api := &pendingCompletionTrackingAPI{pendingProfileTrackingAPI: router, completed: make(map[[2]string]map[string]bool), questReads: make(map[[2]string]int)}
	a.tracker = api
	refreshPendingCompletionForTest(t, a)
	return a, api, remote
}

func refreshPendingCompletionForTest(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	a.mu.Lock()
	a.localAt = time.Time{}
	key, opts, item := a.apiKey, a.state.ItemOptions, *a.state.Item
	a.mu.Unlock()
	if err := a.refreshLocalProgress(a.ctx, key, opts, item); err != nil {
		t.Fatal(err)
	}
}

func completePendingGoalForTest(t *testing.T, api *pendingCompletionTrackingAPI, key, character, goal, revision string) market.TrackedSnapshot {
	t.Helper()
	identity := [2]string{key, character}
	api.mu.Lock()
	route := api.routes[identity]
	if api.completed[identity] == nil {
		api.completed[identity] = make(map[string]bool)
	}
	api.completed[identity][goal] = true
	api.mu.Unlock()
	if route == nil {
		t.Fatal("completion fixture profile is missing")
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	route.snapshot.Revision = revision
	for i := range route.snapshot.Items {
		for j := range route.snapshot.Items[i].Sources {
			source := &route.snapshot.Items[i].Sources[j]
			if source.QuestID == goal {
				source.Completed, source.RemainingRequiredQuantity = true, 0
			}
		}
	}
	return cloneTrackedSnapshot(route.snapshot)
}

func mergedPendingCompletionForTest(t *testing.T, api *pendingCompletionTrackingAPI, key string, raw market.TrackedSnapshot) localprogress.Snapshot {
	t.Helper()
	quests, err := api.LoadQuests(context.Background(), key, raw.CharUID, "en")
	if err != nil {
		t.Fatal(err)
	}
	hideout, err := api.LoadHideout(context.Background(), key, raw.CharUID, "en")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := localprogress.Merge(raw, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	return merged
}

func TestPendingCompletionQueuedAddKeepsOldStockUnknownAndNewQualityKnown(t *testing.T) {
	a, api, remote := newPendingCompletionTestApp(t, 1, 1)
	holdTypedPendingWorkerForTest(a)
	state := a.AddRegularTrackedItem(trackingItemUID)
	if state.PendingAdds.Count != 1 {
		t.Fatalf("the valid pre-completion addition was not reserved: %+v", state.PendingAdds)
	}
	api.mu.Lock()
	readsBefore := api.questReads[[2]string{a.apiKey, a.state.CharacterUID}]
	api.mu.Unlock()
	completePendingGoalForTest(t, api, a.apiKey, a.state.CharacterUID, "fir-goal", "completion-before-unsent-add")
	refreshPendingCompletionForTest(t, a)
	state = a.GetState()
	if state.PendingAdds.Count != 1 || state.Tracking.AvailableQuantity != 3 || state.Tracking.MarketAvailableQuantity != 2 ||
		state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 1 || state.Tracking.UncategorizedQuantity != 2 {
		t.Fatalf("completion lost the queued intent or retained the old stock classification: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(remote); len(writes) != 0 {
		t.Fatalf("completion refresh wrote to the API: %+v", writes)
	}
	api.mu.Lock()
	readsAfter := api.questReads[[2]string{a.apiKey, a.state.CharacterUID}]
	api.mu.Unlock()
	if readsAfter <= readsBefore {
		t.Fatal("completion was inferred without a fresh quest API read")
	}
	startFastTypedPendingWorkerForTest(a)
	state = waitAutomaticPendingDrainForTest(t, a)
	if state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 3 || state.Tracking.MarketAvailableQuantity != 3 ||
		state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 1 || state.Tracking.UncategorizedQuantity != 2 {
		t.Fatalf("the accepted post-completion add resurrected old quality: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(remote); len(writes) != 1 || writes[0].Quantity != 3 || writes[0].Revision != "completion-before-unsent-add" {
		t.Fatalf("only the accepted intent may write its fresh absolute stock: %+v", writes)
	}
}

func TestPendingCompletionInflightChangesInvalidateJournalAndACKCannotResurrectQuality(t *testing.T) {
	for _, delta := range []int64{-1, 1} {
		t.Run(map[int64]string{-1: "remove", 1: "add"}[delta], func(t *testing.T) {
			runPendingCompletionInflightChangeForTest(t, delta)
		})
	}
}

func runPendingCompletionInflightChangeForTest(t *testing.T, delta int64) {
	t.Helper()
	a, api, remote := newPendingCompletionTestApp(t, 1, 1)
	target := 2 + delta
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		waitPendingForTest(t, a)
	})
	remote.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		close(started)
		<-release
		return remote.commitWrite(write), nil
	}
	if delta < 0 {
		a.RemoveFIRTrackedItem(trackingItemUID)
	} else {
		a.AddFIRTrackedItem(trackingItemUID)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stock change did not enter the in-flight write stage")
	}
	raw := completePendingGoalForTest(t, api, a.apiKey, a.state.CharacterUID, "fir-goal", "completion-during-sent-remove")
	merged := mergedPendingCompletionForTest(t, api, a.apiKey, raw)
	a.mu.Lock()
	if len(a.pendingAddQueue) != 1 || a.pendingAddQueue[0].Stage != "sent" {
		a.mu.Unlock()
		t.Fatal("the in-flight stock change lost its sent reservation")
	}
	a.applyMergedMarketSnapshotLocked(*a.state.Item, raw, merged, "")
	if len(a.qualities.data.Pending) != 1 {
		a.mu.Unlock()
		t.Fatal("completion discarded the still-unacknowledged quality journal")
	}
	for _, op := range a.qualities.data.Pending {
		if !op.Invalidated || op.Delta != delta || op.Before.FIR != 1 || !validQualityRecord(op.Before) {
			a.mu.Unlock()
			t.Fatalf("completion corrupted the stock change's restart-safe before record: %+v", op)
		}
	}
	a.mu.Unlock()
	mid := a.GetState()
	if mid.Tracking.AvailableQuantity != target || mid.Tracking.MarketAvailableQuantity != 2 || mid.Tracking.FIRQuantity != 0 ||
		mid.Tracking.RegularQuantity != 0 || mid.Tracking.UncategorizedQuantity != target {
		t.Fatalf("completion reapplied an invalidated typed reservation instead of pending unknown stock: %+v", mid.Tracking)
	}
	restarted := &ItemWindowApp{dir: a.dir}
	store, err := restarted.qualityStoreLocked()
	if err != nil || len(store.data.Pending) != 1 {
		t.Fatalf("invalidated stock change journal could not be reloaded before its ACK: err=%v store=%+v", err, store)
	}
	for _, op := range store.data.Pending {
		if !op.Invalidated {
			t.Fatal("restart lost the sent operation's completion invalidation")
		}
	}
	unblock()
	state := waitAutomaticPendingDrainForTest(t, a)
	if state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != target || state.Tracking.MarketAvailableQuantity != target ||
		state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 0 || state.Tracking.UncategorizedQuantity != target {
		t.Fatalf("the late ACK resurrected pre-completion quality: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	restarted = &ItemWindowApp{dir: a.dir}
	store, err = restarted.qualityStoreLocked()
	if err != nil || len(store.data.Pending) != 0 {
		t.Fatalf("the completed invalidated operation left an invalid restart journal: err=%v store=%+v", err, store)
	}
	if _, writes := trackedHotkeyAPICalls(remote); len(writes) != 1 || writes[0].Quantity != target {
		t.Fatalf("completion or readback replayed the in-flight stock change: %+v", writes)
	}
}

func TestPendingCompletionQueuedRemovalCannotBorrowUnknownStockOrWriteToAPI(t *testing.T) {
	a, api, remote := newPendingCompletionTestApp(t, 1, 1)
	holdTypedPendingWorkerForTest(a)
	state := a.RemoveFIRTrackedItem(trackingItemUID)
	if state.PendingAdds.Count != 1 || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("valid pre-completion removal was not reserved: %+v", state)
	}
	completePendingGoalForTest(t, api, a.apiKey, a.state.CharacterUID, "fir-goal", "completion-before-unsent-remove")
	refreshPendingCompletionForTest(t, a)
	state = a.GetState()
	if state.PendingAdds.Count != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 2 ||
		state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 0 || state.Tracking.UncategorizedQuantity != 1 {
		t.Fatalf("queued removal made reset quality negative before its fresh preflight: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	startFastTypedPendingWorkerForTest(a)
	state = waitAutomaticPendingDrainForTest(t, a)
	if state.PendingAdds.Error == "" || state.Tracking.AvailableQuantity != 2 || state.Tracking.MarketAvailableQuantity != 2 ||
		state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 0 || state.Tracking.UncategorizedQuantity != 2 {
		t.Fatalf("queued typed removal consumed unknown quality or failed to release its reservation: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(remote); len(writes) != 0 {
		t.Fatalf("completion invalidated the selected removal quality, but its pending intent still wrote: %+v", writes)
	}
}

func TestPendingCompletionProfileIsolationPreservesOtherProfilesQueuedQuality(t *testing.T) {
	a, api, first := newPendingCompletionTestApp(t, 1, 1)
	keyA, optsA := a.apiKey, a.state.ItemOptions
	holdTypedPendingWorkerForTest(a)
	a.AddRegularTrackedItem(trackingItemUID)
	const keyB = "completion-second-profile-key"
	first.mu.Lock()
	secondRaw := cloneTrackedSnapshot(first.snapshot)
	first.mu.Unlock()
	secondRaw.CharUID, secondRaw.GameMode, secondRaw.Revision = "completion-second-character", "PVE", "second-profile-initial"
	second := &fakeTrackingAPI{snapshot: secondRaw}
	api.mu.Lock()
	api.routes[[2]string{keyB, secondRaw.CharUID}] = second
	api.mu.Unlock()
	secondMerged := mergedPendingCompletionForTest(t, api, keyB, secondRaw)
	a.mu.Lock()
	a.apiKey = keyB
	a.state.CharacterUID, a.state.Mode = secondRaw.CharUID, strings.ToLower(secondRaw.GameMode)
	a.state.ItemOptions.revision++
	a.applyMergedMarketSnapshotLocked(*a.state.Item, secondRaw, secondMerged, "")
	a.mu.Unlock()
	inv := a.GetTrackerInventory()
	state := a.SetTrackerItemQuality(inv.Profile, trackingBSGID, 2, 1, 1)
	if state.Local != nil && state.Local.Error != "" {
		t.Fatalf("second profile quality could not be seeded: %+v", state.Local)
	}
	state = a.AddFIRTrackedItem(trackingItemUID)
	if state.PendingAdds.Count != 2 {
		t.Fatalf("second profile did not keep its separate reservation: %+v", state.PendingAdds)
	}
	firstRaw := completePendingGoalForTest(t, api, keyA, optsA.CharacterUID, "fir-goal", "first-profile-completion")
	firstMerged := mergedPendingCompletionForTest(t, api, keyA, firstRaw)
	a.mu.Lock()
	current, unique := stockItem(a.confirmedProgress, trackingBSGID)
	wasCompleted := a.qualities.data.Completions[qualityProfile(keyA, optsA)].Goals["quest:fir-goal"].Completed
	a.mu.Unlock()
	if !unique || current.FIRQuantity != 1 || current.RegularQuantity != 1 || wasCompleted {
		t.Fatalf("first-profile completion was inferred before the worker's fresh API preflight or affected the visible second profile: completed=%v unique=%v row=%+v", wasCompleted, unique, current)
	}
	startFastTypedPendingWorkerForTest(a)
	state = waitAutomaticPendingDrainForTest(t, a)
	if state.PendingAdds.Error != "" || state.Tracking.CharUID != secondRaw.CharUID || state.Tracking.AvailableQuantity != 3 ||
		state.Tracking.FIRQuantity != 2 || state.Tracking.RegularQuantity != 1 || state.Tracking.UncategorizedQuantity != 0 {
		t.Fatalf("another profile's completion leaked into the queued quality: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	a.mu.Lock()
	recordA := a.qualities.data.Profiles[qualityProfile(keyA, optsA)][qualityRowKey(firstMerged.Items[0])]
	a.mu.Unlock()
	if recordA.Total != 3 || recordA.FIR != 0 || recordA.Regular != 1 {
		t.Fatalf("the completed profile's old classification was retained or its new intent was lost: %+v", recordA)
	}
	_, writesA := trackedHotkeyAPICalls(first)
	_, writesB := trackedHotkeyAPICalls(second)
	if len(writesA) != 1 || writesA[0].Character != optsA.CharacterUID || writesA[0].Quantity != 3 ||
		len(writesB) != 1 || writesB[0].Character != secondRaw.CharUID || writesB[0].Quantity != 3 {
		t.Fatalf("completion crossed profiles or wrote extra API stock changes: A=%+v B=%+v", writesA, writesB)
	}
}

func TestPendingCachedPublicationDoesNotInventCompletionCyclesAndFreshReusedETagsDo(t *testing.T) {
	a, api, remote := newPendingCompletionTestApp(t, 1, 1)
	key, opts := a.apiKey, a.state.ItemOptions
	remote.mu.Lock()
	remote.snapshot.Revision = "opaque-A"
	before := cloneTrackedSnapshot(remote.snapshot)
	remote.mu.Unlock()
	mergedBefore := mergedPendingCompletionForTest(t, api, key, before)
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, before, mergedBefore, "")
	a.mu.Unlock()
	after := completePendingGoalForTest(t, api, key, opts.CharacterUID, "fir-goal", "opaque-B")
	mergedAfter := mergedPendingCompletionForTest(t, api, key, after)
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, after, mergedAfter, "")
	a.mu.Unlock()
	inv := a.GetTrackerInventory()
	state := a.SetTrackerItemQuality(inv.Profile, trackingBSGID, 2, 1, 1)
	if state.Local == nil || state.Local.Error != "" || state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 1 {
		t.Fatalf("post-completion local classification failed: %+v", state.Tracking)
	}
	entry := pendingMarketAdd{Key: key, Opts: opts, Item: *state.Item, Typed: true, Stage: "queued", Progress: mergedBefore, ProgressCached: true}
	a.mu.Lock()
	a.publishPendingRemoteLocked(entry, before, "cached preflight")
	// A cache használható a stock CAS előkészítéséhez, de nem jelent új
	// false állapotot a már frissen megfigyelt completion után.
	a.qualityChangeAllowedLocked(mergedBefore, entry)
	historyAfterCache := a.qualities.data.Completions[qualityProfile(key, opts)].Goals["quest:fir-goal"].Completed
	a.applyMergedMarketSnapshotLocked(*a.state.Item, after, mergedAfter, "fresh still completed")
	a.mu.Unlock()
	state = a.GetState()
	if !historyAfterCache || state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 1 {
		t.Fatalf("cached false metadata invented a new false-to-true cycle and erased local classification: completed=%v tracking=%+v", historyAfterCache, state.Tracking)
	}
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, before, mergedBefore, "fresh false with reused ETag")
	a.applyMergedMarketSnapshotLocked(*a.state.Item, after, mergedAfter, "fresh true with reused ETag")
	a.mu.Unlock()
	state = a.GetState()
	if state.Tracking.AvailableQuantity != 2 || state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 0 || state.Tracking.UncategorizedQuantity != 2 {
		t.Fatalf("reused opaque ETags suppressed a genuine fresh false-to-true cycle: %+v", state.Tracking)
	}
	if _, writes := trackedHotkeyAPICalls(remote); len(writes) != 0 {
		t.Fatalf("cache/fresh goal reconciliation unexpectedly wrote API stock: %+v", writes)
	}
}

func TestPendingCachedPreflightReadbackProcessesFreshCompletion(t *testing.T) {
	a, api, remote := newPendingCompletionTestApp(t, 0, 0)
	var cachedSecondWrite atomic.Bool
	remote.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		ack := remote.commitWrite(write)
		remote.mu.Lock()
		second := remote.commits == 2
		remote.mu.Unlock()
		if second {
			a.mu.Lock()
			if len(a.pendingAddQueue) == 1 {
				cachedSecondWrite.Store(a.pendingAddQueue[0].ProgressCached)
			}
			a.mu.Unlock()
			// A második kérés cache-ből ellenőrzött készlettel indult, de
			// a teljesítést már csak az utána következő friss GET észleli.
			completePendingGoalForTest(t, api, a.apiKey, a.state.CharacterUID, "fir-goal", "completion-on-cached-intent-readback")
		}
		return ack, nil
	}
	holdTypedPendingWorkerForTest(a)
	a.AddRegularTrackedItem(trackingItemUID)
	a.AddFIRTrackedItem(trackingItemUID)
	startFastTypedPendingWorkerForTest(a)
	state := waitAutomaticPendingDrainForTest(t, a)
	if !cachedSecondWrite.Load() {
		t.Fatal("the second accepted intent did not exercise the cached-preflight worker path")
	}
	if state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 2 || state.Tracking.MarketAvailableQuantity != 2 ||
		state.Tracking.FIRQuantity != 0 || state.Tracking.RegularQuantity != 0 || state.Tracking.UncategorizedQuantity != 2 {
		t.Fatalf("cached-preflight provenance incorrectly suppressed its fresh readback completion: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
	a.mu.Lock()
	completed := a.qualities.data.Completions[qualityProfile(a.apiKey, a.state.ItemOptions)].Goals["quest:fir-goal"].Completed
	a.mu.Unlock()
	if !completed {
		t.Fatal("fresh readback metadata did not advance the completion baseline")
	}
	if _, writes := trackedHotkeyAPICalls(remote); len(writes) != 2 || writes[0].Quantity != 1 || writes[1].Quantity != 2 {
		t.Fatalf("completion verification sent more than the two accepted intents: %+v", writes)
	}
}
