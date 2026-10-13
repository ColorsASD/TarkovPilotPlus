package plus

import (
	"context"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func fallbackForTest(t *testing.T, a *ItemWindowApp) localprogress.Item {
	t.Helper()
	stock, err := a.loadMarketFallback(a.state.ItemOptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range stock.Items {
		if row.ItemID == trackingBSGID {
			return row
		}
	}
	return localprogress.Item{}
}

func TestMarketFallbackVerifiedNoOpUsesSeparateTypedPoolAndMarketQuota(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	a.AddTrackedItem(trackingItemUID)
	a.AddTrackedItemFIR(trackingItemUID)
	a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 0 {
		t.Fatal("local Add waited for or sent a Market write")
	}
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	if row := fallbackForTest(t, a); row.LocalAvailableQuantity != 3 || row.LocalRegularQuantity != 2 || row.LocalFIRQuantity != 1 || len(fake.writes) != 1 {
		t.Fatalf("failed Market additions lost, untyped or repeatedly sent: %+v writes=%+v", row, fake.writes)
	}
	if row := handoverStockForTest(t, a, trackingBSGID); row.LocalAvailableQuantity != 0 {
		t.Fatal("Market fallback leaked into dev-local stock")
	}
	a.AddTrackedItem(trackingItemUID)
	if a.GetState().Tracking.AvailableQuantity != 4 || handoverStockForTest(t, a, trackingBSGID).LocalAvailableQuantity != 1 || fake.snapshot.Items[0].AvailableQuantity != 0 || len(fake.writes) != 1 {
		t.Fatal("fallback was not included in Market quota or total stock")
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	requireHandoverComplete(t, a.ConfirmHandover())
	fallback := fallbackForTest(t, a)
	if fallback.LocalAvailableQuantity != 2 || fallback.LocalUsedQuantity != 1 || fallback.LocalFIRQuantity != 1 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 || len(fake.writes) != 1 {
		t.Fatalf("hand-in did not consume dev then fallback: %+v", fallback)
	}
	a.CloseHandover()
	fake.writeHook = nil
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	row := editorRowForTest(t, a.OpenStockEditor(), trackingBSGID)
	if row.AvailableQuantity != 2 || row.MarketFallbackStock != (localprogress.StockAmounts{}) || row.MarketStock.Regular != 1 || row.MarketStock.FIR != 1 || row.UsedQuantity != 2 || row.MarketFallbackUsed.Regular != 1 || fake.snapshot.Items[0].AvailableQuantity != 2 || len(fake.writes) != 2 {
		t.Fatalf("verified transfer changed total, types or usage: %+v", row)
	}
}

func TestMarketFallbackUncertainAddNeverCreatesDuplicateLocalStock(t *testing.T) {
	a, fake := newLocalTestApp(t)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.commitWrite(write)
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	a.AddTrackedItemFIR(trackingItemUID)
	if fallbackForTest(t, a).LocalAvailableQuantity != 1 || len(fake.writes) != 0 {
		t.Fatal("initial Add was not saved locally before background work")
	}
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	if fallbackForTest(t, a).LocalAvailableQuantity != 0 || len(fake.writes) != 1 || fake.snapshot.Items[0].AvailableQuantity != 1 {
		t.Fatal("uncertain applied Market update also created fallback")
	}
}

func TestMarketFallbackPendingUploadBlocksWritesAndResumesWithoutReplay(t *testing.T) {
	a, fake := newLocalTestApp(t)
	amounts := localprogress.StockAmounts{FIR: 1}
	if _, err := a.marketFallbackStock().CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	start := fake.loads
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.commitWrite(write)
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	fake.loadHook = func(n int) (market.TrackedSnapshot, error) {
		if n == start+2 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	if _, err := a.recoverMarketFallback(context.Background(), a.apiKey, a.state.ItemOptions, trackingBSGID, true); err == nil {
		t.Fatal("readback failure was confirmed")
	}
	if !a.GetState().MarketFallbackPending || len(fake.writes) != 1 {
		t.Fatal("pending upload did not block competing stock")
	}
	a.restoreStockEditorLocked()
	a.AddTrackedItemFIR(trackingItemUID)
	a.OpenHandover()
	if len(fake.writes) != 1 || a.GetState().Handover == nil || !a.GetState().Handover.Open {
		t.Fatal("pending upload sent a competing write or blocked the independent hand-in list")
	}
	a.CloseHandover()
	fake.loadHook = nil
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	row := editorRowForTest(t, a.OpenStockEditor(), trackingBSGID)
	if a.GetState().MarketFallbackPending || row.MarketStock.FIR != 1 || row.AvailableQuantity != 1 || row.UsedQuantity != 0 || fallbackForTest(t, a).LocalAvailableQuantity != 0 || len(fake.writes) != 1 {
		t.Fatal("pending upload replayed or transfer fabricated usage")
	}
}

func TestMarketFallbackManualCorrectionKeepsDevAndUsedIndependent(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	amounts := localprogress.StockAmounts{Regular: 2}
	if _, err := a.marketFallbackStock().CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.marketFallbackStock().RecordHandover("character-a", "PVP", "fallback-test", []localprogress.HandoverEntry{{ItemID: trackingBSGID, Regular: 1}}); err != nil {
		t.Fatal(err)
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	a.OpenStockEditor()
	row := editorRowForTest(t, a.SaveStockEditorItem(editorRequestForTest(a, "market-fallback", 1, 0, 0)), trackingBSGID)
	if row.MarketFallbackStock.FIR != 1 || row.MarketFallbackUsed.Regular != 1 || row.UsedQuantity != 1 || row.LocalStock != (localprogress.StockAmounts{}) {
		t.Fatalf("source correction leaked or erased use: %+v", row)
	}
	row = editorRowForTest(t, a.SaveStockEditorItem(editorRequestForTest(a, "market-fallback", 0, 0, 0)), trackingBSGID)
	if row.AvailableQuantity != 0 || row.UsedQuantity != 1 || fallbackForTest(t, a).LocalUsedQuantity != 1 {
		t.Fatal("clear pending stock erased usage")
	}
}

func TestMarketFallbackRecoveryThrottle(t *testing.T) {
	a, fake := newLocalTestApp(t)
	amounts := localprogress.StockAmounts{Regular: 1}
	if _, err := a.marketFallbackStock().CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	if _, err := a.recoverMarketFallback(context.Background(), a.apiKey, a.state.ItemOptions, trackingBSGID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.recoverMarketFallback(context.Background(), a.apiKey, a.state.ItemOptions, trackingBSGID, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 1 || fallbackForTest(t, a).LocalAvailableQuantity != 1 {
		t.Fatal("failed sync retried too frequently")
	}
	a.fallbackSyncAt["character-a\x00PVP\x00"+trackingBSGID] = time.Now().Add(-2 * time.Minute)
	if _, err := a.recoverMarketFallback(context.Background(), a.apiKey, a.state.ItemOptions, trackingBSGID, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 2 {
		t.Fatal("restoration was never retried")
	}
}

func TestMarketFallbackTemporaryReadOutageKeepsWarmStockAndNeverSendsPatch(t *testing.T) {
	a, fake := newLocalTestApp(t)
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		return market.TrackedSnapshot{}, market.ErrProgressUnavailable
	}
	a.AddTrackedItemFIR(trackingItemUID)
	a.AddTrackedItem(trackingItemUID)
	if err := a.processProgressWork(a.ctx); err == nil {
		t.Fatal("offline background upload was falsely verified")
	}
	row := fallbackForTest(t, a)
	state := a.GetState()
	if row.LocalAvailableQuantity != 2 || row.LocalFIRQuantity != 1 || row.LocalRegularQuantity != 1 || len(fake.writes) != 0 || state.Tracking.AvailableQuantity != 2 || state.ProgressSync == nil || state.ProgressSync.Error == "" {
		t.Fatal("read outage lost captured additions or sent an unchecked patch")
	}
}

func TestMarketFallbackDoesNotUploadWhileManualMarketEditIsPending(t *testing.T) {
	a, fake := newLocalTestApp(t)
	amounts := localprogress.StockAmounts{Regular: 1}
	if _, err := a.marketFallbackStock().CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	if err := a.saveStockEditorPending(&stockEditorPending{Version: 1, CharUID: "character-a", Mode: "PVP", ItemID: trackingBSGID, Revision: fake.snapshot.Revision, Amounts: localprogress.StockAmounts{Regular: 2}}); err != nil {
		t.Fatal(err)
	}
	a.restoreStockEditorLocked()
	state := a.OpenStockEditor()
	if !state.StockEditor.Pending || len(fake.writes) != 0 {
		t.Fatal("opening pending editor uploaded competing fallback stock")
	}
	a.CloseStockEditor()
	if _, err := a.recoverMarketFallback(context.Background(), a.apiKey, a.state.ItemOptions, "", false); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 0 || fallbackForTest(t, a).LocalAvailableQuantity != 1 {
		t.Fatal("closed pending editor allowed a background upload")
	}
}

func TestMarketFallbackPendingVerifiedTargetDoesNotDoubleDisplayDuringSettlement(t *testing.T) {
	a, fake := newLocalTestApp(t)
	amounts := localprogress.StockAmounts{FIR: 1}
	if _, err := a.marketFallbackStock().CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	intent := &marketFallbackSyncIntent{Version: 1, CharUID: "character-a", Mode: "PVP", ItemID: trackingBSGID, Revision: fake.snapshot.Revision, Before: 0, Target: 1, Fallback: amounts, MarketAfter: amounts}
	if err := a.saveMarketFallbackSync(a.state.ItemOptions, intent); err != nil {
		t.Fatal(err)
	}
	fake.commitWrite(stockWrite{"character-a", trackingBSGID, fake.snapshot.Revision, 1})
	snapshot, err := a.applyProgressStock(marketProgressOnly(fake.snapshot))
	if err != nil {
		t.Fatal(err)
	}
	row, unique := stockEditorItem(snapshot, trackingBSGID)
	if !unique || row.AvailableQuantity != 1 || row.MarketFallbackAvailableQuantity != 0 || fallbackForTest(t, a).LocalAvailableQuantity != 1 {
		t.Fatal("verified upload displayed remote plus the still unsettled local file")
	}
}
