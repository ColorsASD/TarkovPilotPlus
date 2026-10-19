package plus

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func blockFirstPendingRead(t *testing.T, fake *fakeTrackingAPI) (<-chan struct{}, func()) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
		if count == 1 {
			close(started)
			<-release
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	return started, unblock
}

func TestPendingAddKeepsOriginalItemAfterInspectChanges(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	other := market.Item{UID: "another-item-uid", BSGID: "another-item-id", Name: "Another item"}
	fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: other.BSGID, Name: other.Name, RequiredQuantity: 3, RemainingQuantity: 3,
		Sources: []market.TrackedSource{{Type: "quest", Amount: 3, RemainingRequiredQuantity: 3}}})
	setTrackingTestSnapshot(t, a, fake.snapshot)
	started, release := blockFirstPendingRead(t, fake)
	a.AddTrackedItem(trackingItemUID)
	<-started
	a.mu.Lock()
	a.state.Item = &other
	a.state.Tracking = a.pendingTrackedViewLocked(other)
	a.mu.Unlock()
	release()
	state := waitPendingForTest(t, a)
	fake.mu.Lock()
	writes := append([]stockWrite(nil), fake.writes...)
	fake.mu.Unlock()
	if len(writes) != 1 || writes[0].Item != trackingBSGID || state.PendingAdds.Count != 0 || state.Item.UID != other.UID || state.Tracking.ItemID != other.BSGID || state.Tracking.AvailableQuantity != 0 {
		t.Fatalf("Inspect redirected or canceled an accepted background addition: writes=%+v state=%+v", writes, state)
	}
}

func TestPendingOfflineAdditionStaysInMemoryUntilExplicitRecheck(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	var offline atomic.Bool
	offline.Store(true)
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		if offline.Load() {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	a.AddTrackedItem(trackingItemUID)
	state := waitPendingForTest(t, a)
	if state.PendingAdds.Count != 1 || state.PendingAdds.Running || state.PendingAdds.Error == "" || state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 0 || a.GetPendingAddCount() != 1 {
		t.Fatalf("offline addition was lost or presented as confirmed Market stock: %+v", state)
	}
	assertNoMarketStockFilesForTest(t, a.dir)
	offline.Store(false)
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	fake.mu.Lock()
	writes := len(fake.writes)
	fake.mu.Unlock()
	if state.PendingAdds.Count != 0 || state.Tracking.AvailableQuantity != 1 || state.Tracking.MarketAvailableQuantity != 1 || writes != 1 {
		t.Fatalf("explicit retry lost or duplicated the unsent addition: writes=%d state=%+v", writes, state)
	}
}
