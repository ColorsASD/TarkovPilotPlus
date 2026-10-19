package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func newPendingRemoveTestApp(t *testing.T, stock int64, noRequirements bool) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newTrackingTestApp(t)
	fake.snapshot.Items[0].AvailableQuantity = stock
	if noRequirements {
		fake.snapshot.Items[0].RequiredQuantity = 0
		fake.snapshot.Items[0].Sources = nil
	}
	fake.snapshot.Items[0].RemainingQuantity = max(0, fake.snapshot.Items[0].RequiredQuantity-stock)
	setTrackingTestSnapshot(t, a, fake.snapshot)
	return a, fake
}

func TestPendingRemoveIsImmediateWithoutRequirementsAndStopsAtZero(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 2, true)
	started, release := blockFirstPendingReadForTest(t, fake)
	state := a.RemoveTrackedItem(trackingItemUID)
	<-started
	if state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 2 || state.PendingAdds.Count != 1 || !state.Tracking.RemoveAllowed || state.Tracking.AddAllowed {
		t.Fatalf("remove waited for API or required a goal: %+v", state)
	}
	state = a.RemoveTrackedItem(trackingItemUID)
	if state.Tracking.AvailableQuantity != 0 || state.Tracking.MarketAvailableQuantity != 2 || state.PendingAdds.Count != 2 || state.Tracking.RemoveAllowed {
		t.Fatalf("second remove did not reserve the last piece: %+v", state)
	}
	state = a.RemoveTrackedItem(trackingItemUID)
	if state.Tracking.AvailableQuantity != 0 || state.PendingAdds.Count != 2 || len(fake.writes) != 0 {
		t.Fatalf("remove overbooked empty stock: %+v", state)
	}
	release()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 0 || state.Tracking.MarketAvailableQuantity != 0 || state.Tracking.RemoveAllowed || len(fake.writes) != 2 || fake.writes[0].Quantity != 1 || fake.writes[1].Quantity != 0 {
		t.Fatalf("removal did not send absolute 1 then 0 exactly once: state=%+v writes=%+v", state, fake.writes)
	}
	assertNoMarketStockFilesForTest(t, a.dir)
}

func TestPendingRemoveEmptyStockNeverQueuesOrWrites(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 0, false)
	state := a.RemoveTrackedItem(trackingItemUID)
	if state.PendingAdds != nil && state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 0 || state.Tracking.RemoveAllowed || fake.loads != 0 || len(fake.writes) != 0 {
		t.Fatalf("empty stock accepted a removal: state=%+v writes=%+v", state, fake.writes)
	}
}

func TestPendingRemoveFreshZeroCancelsBeforePATCH(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 1, false)
	fake.snapshot.Items[0].AvailableQuantity = 0
	fake.snapshot.Revision = "externally-cleared"
	a.RemoveTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 0 || state.Tracking.RemoveAllowed || len(fake.writes) != 0 {
		t.Fatalf("fresh zero allowed a negative PATCH or stale remove state: state=%+v writes=%+v", state, fake.writes)
	}
}

func TestPendingSignedAddRemovePairsKeepFIFOAndNetStock(t *testing.T) {
	for _, removeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "add-then-remove", true: "remove-then-add"}[removeFirst], func(t *testing.T) {
			initial := int64(0)
			if removeFirst {
				initial = 1
			}
			a, fake := newPendingRemoveTestApp(t, initial, false)
			started, release := blockFirstPendingReadForTest(t, fake)
			if removeFirst {
				a.RemoveTrackedItem(trackingItemUID)
			} else {
				a.AddTrackedItem(trackingItemUID)
			}
			<-started
			var state ItemState
			if removeFirst {
				state = a.AddTrackedItem(trackingItemUID)
			} else {
				state = a.RemoveTrackedItem(trackingItemUID)
			}
			if state.Tracking.AvailableQuantity != initial || state.Tracking.MarketAvailableQuantity != initial || state.PendingAdds.Count != 2 {
				t.Fatalf("opposite reservations lost their signed net: %+v", state)
			}
			release()
			state = waitPendingForTest(t, a)
			first, last := int64(1), int64(0)
			if removeFirst {
				first, last = 0, 1
			}
			if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != initial || len(fake.writes) != 2 || fake.writes[0].Quantity != first || fake.writes[1].Quantity != last {
				t.Fatalf("signed pair reordered or duplicated a stock write: state=%+v writes=%+v", state, fake.writes)
			}
		})
	}
}

func TestPendingRemoveDefiniteRejectionRestoresOptimisticStock(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 1, false)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressForbidden
	}
	a.RemoveTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error == "" || state.Tracking.AvailableQuantity != 1 || !state.Tracking.RemoveAllowed || len(fake.writes) != 1 || fake.writes[0].Quantity != 0 {
		t.Fatalf("rejected removal lost the confirmed piece: state=%+v writes=%+v", state, fake.writes)
	}
}

func TestPendingRemoveUncertainResultOnlyRechecksGET(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 1, false)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	a.RemoveTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || state.PendingAdds.Running || state.Tracking.AvailableQuantity != 0 || state.Tracking.MarketAvailableQuantity != 1 || state.Tracking.RemoveAllowed || len(fake.writes) != 1 {
		t.Fatalf("uncertain removal was discarded or shown as confirmed: %+v", state)
	}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || len(fake.writes) != 1 {
		t.Fatal("uncertain removal repeated its PATCH")
	}
	fake.commitWrite(fake.writes[0])
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 0 || state.Tracking.MarketAvailableQuantity != 0 || state.Tracking.RemoveAllowed || len(fake.writes) != 1 {
		t.Fatalf("fresh zero did not reconcile the one sent removal: %+v", state)
	}
}

func TestPendingRemoveConflictRechecksLatestStockOnce(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 3, false)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		if len(fake.writes) == 1 {
			fake.snapshot.Items[0].AvailableQuantity = 2
			fake.snapshot.Revision = "external-two"
			return market.ProgressWriteResult{}, market.ErrProgressConflict
		}
		return fake.commitWrite(write), nil
	}
	a.RemoveTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 1 || len(fake.writes) != 2 || fake.writes[0].Quantity != 2 || fake.writes[1].Quantity != 1 || fake.writes[1].Revision != "external-two" {
		t.Fatalf("remove conflict did not subtract once from fresh stock: state=%+v writes=%+v", state, fake.writes)
	}
}

func TestPendingLegacyZeroDeltaStillMeansOneAddition(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 0, false)
	a.mu.Lock()
	a.pendingAddQueue = []pendingMarketAdd{{ID: 1, Key: a.apiKey, Opts: a.state.ItemOptions, Item: *a.state.Item, Stage: "queued"}}
	a.rebuildPendingViewLocked()
	a.startPendingAddWorkerLocked()
	a.mu.Unlock()
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 1 || len(fake.writes) != 1 || fake.writes[0].Quantity != 1 {
		t.Fatalf("legacy positive pending intent changed meaning: state=%+v writes=%+v", state, fake.writes)
	}
}

func TestPendingRemoveZeroMissingRawRowNeedsExplicitMarketMembership(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown-item-stays-pending", true: "explicit-group-zero-confirmed"}[known], func(t *testing.T) {
			a, fake := newPendingRemoveTestApp(t, 1, false)
			if known {
				fake.snapshot.Groups = []market.TrackedGroup{{QuestID: "group-quest", RequirementID: "group", Amount: 2, RequiredQuantity: 2, AvailableQuantity: 1, RemainingQuantity: 1, ItemIDs: []string{trackingBSGID, "other"}}}
				setTrackingTestSnapshot(t, a, fake.snapshot)
			}
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				fake.snapshot.Items = []market.TrackedItem{}
				fake.snapshot.Revision = "removed-last-raw-row"
				if known {
					setFakeMarketGroupProgress(&fake.snapshot, 0)
				}
				return market.ProgressWriteResult{OK: true, Changed: true, Revision: fake.snapshot.Revision, NotificationDelivered: true}, nil
			}
			a.RemoveTrackedItem(trackingItemUID)
			state := waitPendingForTest(t, a)
			wantPending := 1
			if known {
				wantPending = 0
			}
			if state.PendingAdds.Count != wantPending || len(fake.writes) != 1 || fake.writes[0].Quantity != 0 || state.Tracking.RemoveAllowed {
				t.Fatalf("missing raw row lost its explicit membership boundary: known=%v state=%+v writes=%+v", known, state, fake.writes)
			}
			if known && (state.Tracking.AvailableQuantity != 0 || state.PendingAdds.Error != "") {
				t.Fatalf("explicit zero group candidate was not confirmed: %+v", state)
			}
		})
	}
}

func TestPendingRemoveRefreshReconcilesExplicitGroupZeroWithoutReplay(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 1, false)
	fake.snapshot.Groups = []market.TrackedGroup{{QuestID: "group-quest", RequirementID: "group", Amount: 2, RequiredQuantity: 2, AvailableQuantity: 1, RemainingQuantity: 1, ItemIDs: []string{trackingBSGID, "other"}}}
	setTrackingTestSnapshot(t, a, fake.snapshot)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: true, Revision: "remove-refresh-zero", NotificationDelivered: true}, nil
	}
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read > 1 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	a.RemoveTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 {
		t.Fatalf("read outage discarded removal: %+v", state)
	}
	fake.loadHook = nil
	fake.snapshot.Items = []market.TrackedItem{}
	fake.snapshot.Revision = "remove-refresh-zero"
	setFakeMarketGroupProgress(&fake.snapshot, 0)
	a.localAt = time.Time{}
	a.refreshTracking()
	state = a.GetState()
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 0 || state.Tracking.RemoveAllowed || len(fake.writes) != 1 {
		t.Fatalf("refresh did not confirm explicit group zero without replay: %+v", state)
	}
}
