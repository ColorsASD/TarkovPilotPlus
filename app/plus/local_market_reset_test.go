package plus

import (
	"context"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestExternalMarketZeroResetClearsOnlyMarketAnnotationsThroughRefresh(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 3; i++ {
		addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	}
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	before := handoverStockForTest(t, a, trackingBSGID)
	if before.LocalAvailableQuantity != 2 {
		t.Fatalf("fixture missing local overflow: %+v", before)
	}
	fake.commitWrite(stockWrite{"character-a", trackingBSGID, fake.snapshot.Revision, 0})
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := a.GetState()
	if state.Local.Error != "" || state.Tracking.AvailableQuantity != 2 || state.Tracking.LocalAvailableQuantity != 2 {
		t.Fatalf("external Market zero consumed independent local stock: %+v %+v", state.Local, state.Tracking)
	}
	saved, err := a.localStore.Load("character-a", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range saved.Items {
		if row.ItemID == trackingBSGID && (row.LocalCollectedQuantity != 2 || row.LocalFIRQuantity != 1 || row.LocalRegularQuantity != 1 || row.LocalUsedQuantity != 0 || row.MarketFIRQuantity != 0 || row.MarketRegularQuantity != 0) {
			t.Fatalf("reset changed local stock or left Market quality: %+v", row)
		}
	}
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	if state = a.GetState(); state.Tracking.AvailableQuantity != 3 || state.Tracking.LocalAvailableQuantity != 2 {
		t.Fatalf("new Market collection changed independent local stock: %+v", state.Tracking)
	}
}

func TestHandoverUncertainOwnZeroPreservesOtherLocalQualityAcrossRestart(t *testing.T) {
	a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 3; i++ {
		addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	}
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	initialReads := fake.loads
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.commitWrite(write)
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read == initialReads+3 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 3, "regular"))
	requireHandoverPending(t, a, a.confirmHandoverLegacy())
	row := handoverStockForTest(t, a, trackingBSGID)
	if row.LocalFIRQuantity != 2 || row.LocalAvailableQuantity != 2 {
		t.Fatalf("own zero target was treated as an external reset: %+v", row)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	restarted := &ItemWindowApp{ctx: ctx, dir: a.dir, apiKey: a.apiKey, tracker: fake,
		devRequirements: a.devRequirements, localStore: localprogress.NewStore(dir), state: a.GetState()}
	restarted.mu.Lock()
	restarted.restoreHandoverLocked()
	restarted.mu.Unlock()
	fake.writeHook, fake.loadHook = nil, nil
	restarted.refreshLocalProgress(restarted.ctx, restarted.apiKey, restarted.state.ItemOptions, *restarted.state.Item)
	restarted.OpenHandover()
	requireHandoverComplete(t, restarted.confirmHandoverLegacy())
	row = handoverStockForTest(t, restarted, trackingBSGID)
	if len(fake.writes) != 4 || row.LocalFIRQuantity != 2 || row.LocalAvailableQuantity != 2 || fake.snapshot.Items[0].AvailableQuantity != 0 {
		t.Fatalf("restart repeated own deduction or discarded other quality: writes=%+v row=%+v", fake.writes, row)
	}
}
