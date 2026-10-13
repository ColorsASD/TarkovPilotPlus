package plus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func pendingFallbackRecoveryFixture(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI, ItemOptions, localprogress.StockAmounts) {
	t.Helper()
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	confirmed, err := a.localStore.Apply(marketProgressOnly(fake.snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.rememberProgressSources(a.apiKey, opts, confirmed, ""); err != nil {
		t.Fatal(err)
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	_, _, err = a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
	if !errors.Is(err, errMarketFallbackSyncPending) {
		t.Fatal("fixture did not retain uncertain upload")
	}
	a.setMarketFallbackPending(a.apiKey, opts, true)
	return a, fake, opts, fallbackSyncAvailableForTest(t, a, opts)
}

func TestMarketFallbackPendingEditorRefreshStillLoadsReadableStock(t *testing.T) {
	a, fake, opts, fallback := pendingFallbackRecoveryFixture(t)
	writes := len(fake.writes)
	state := a.OpenStockEditor()
	row := editorRowForTest(t, state, trackingBSGID)
	if !state.MarketFallbackPending || state.StockEditor.Loading || state.StockEditor.Saving ||
		state.StockEditor.Error != "" || !strings.Contains(row.EditError, "awaiting verification") || row.MarketEditable || row.MarketFallbackEditable || !row.LocalEditable || row.AvailableQuantity != 8 ||
		row.MarketFallbackStock != fallback || row.LocalStock.Regular != 2 || len(fake.writes) != writes {
		t.Fatalf("pending upload hid readable stock or replayed it: %+v %+v", state.StockEditor, fake.writes)
	}
	if fallbackSyncAvailableForTest(t, a, opts) != fallback {
		t.Fatal("refresh lost supplementary stock")
	}
}

func TestMarketFallbackPublicRetryResolvesOnceAndRefreshesEditor(t *testing.T) {
	a, fake, opts, _ := pendingFallbackRecoveryFixture(t)
	a.OpenStockEditor()
	fake.writeHook = nil
	state := a.RetryMarketUpload()
	row := editorRowForTest(t, state, trackingBSGID)
	if state.MarketFallbackPending || state.StockEditor.Loading || state.StockEditor.Saving || state.StockEditor.Error != "" ||
		row.AvailableQuantity != 8 || row.MarketFallbackStock != (localprogress.StockAmounts{}) || row.MarketStock.FIR != 1 || row.LocalStock.Regular != 2 ||
		len(fake.writes) != 2 || fake.writes[0].Quantity != 6 || fake.writes[1].Quantity != 6 || fake.writes[1].Revision != fake.writes[0].Revision {
		t.Fatalf("saved target was not safely retried: %+v %+v", state.StockEditor, fake.writes)
	}
	if row.UsedQuantity != 3 || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{}) {
		t.Fatal("retry changed usage or left duplicate available stock")
	}
	a.RetryMarketUpload()
	if len(fake.writes) != 2 {
		t.Fatal("completed retry sent another upload")
	}
}

func TestMarketFallbackPublicRetryBusyGuardsPreserveSavedUpload(t *testing.T) {
	for _, block := range []string{"scan", "backup", "handover", "editor", "editor-pending", "no-key"} {
		t.Run(block, func(t *testing.T) {
			a, fake, opts, before := pendingFallbackRecoveryFixture(t)
			switch block {
			case "scan":
				a.state.Busy = true
			case "backup":
				a.state.LocalBackup = &ItemLocalBackupState{Busy: true}
			case "handover":
				a.state.Handover = &ItemHandoverState{Pending: true}
			case "editor":
				a.state.StockEditor = &ItemStockEditorState{Loading: true}
			case "editor-pending":
				a.state.StockEditor = &ItemStockEditorState{Pending: true}
			case "no-key":
				a.apiKey = ""
			}
			reads, writes := fake.loads, len(fake.writes)
			a.RetryMarketUpload()
			pending, err := a.marketFallbackSyncPending(opts)
			if !pending || err != nil || fallbackSyncAvailableForTest(t, a, opts) != before || fake.loads != reads || len(fake.writes) != writes {
				t.Fatal("blocked retry read, wrote, or changed saved stock")
			}
		})
	}
}

func TestMarketFallbackPublicRetryObsoleteRevisionUnlocksWithoutAnotherWrite(t *testing.T) {
	a, fake, opts, before := pendingFallbackRecoveryFixture(t)
	fake.snapshot.Revision = "revision-changed-externally"
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: write.Revision}, nil
	}
	opened := a.OpenStockEditor()
	if !opened.MarketFallbackPending {
		t.Fatal("opening a cached editor silently sent a synchronous reconciliation")
	}
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); err != nil {
		t.Fatal(err)
	}
	state := a.RetryMarketUpload()
	row := editorRowForTest(t, state, trackingBSGID)
	if state.MarketFallbackPending || state.StockEditor.Error != "" || state.StockEditor.Saving ||
		row.AvailableQuantity != 8 || row.MarketFallbackStock != before || row.LocalStock.Regular != 2 || row.UsedQuantity != 3 ||
		len(fake.writes) != 1 {
		t.Fatalf("obsolete no-op upload still blocked stock or lost a source: %+v %+v", state.StockEditor, fake.writes)
	}
	state = a.SaveStockEditorItem(ItemStockEditRequest{Generation: state.StockEditor.Generation, ItemID: trackingBSGID, Source: "market-fallback", FIR: 1, Regular: 1, Unknown: 1})
	row = editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Error != "" || row.MarketFallbackStock.Regular != 1 || row.LocalStock.Regular != 2 || row.UsedQuantity != 3 || len(fake.writes) != 1 {
		t.Fatalf("unlocked supplementary stock cannot be edited independently: %+v", state.StockEditor)
	}
	if available := fallbackSyncAvailableForTest(t, a, opts); available != (localprogress.StockAmounts{FIR: 1, Regular: 1, Unknown: 1}) {
		t.Fatal("manual supplementary correction was not saved")
	}
}
