package plus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestLocalFirstStockEditorCancellationBeforePatchRestoresUnsent(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	a.OpenStockEditor()
	a.SaveStockEditorItem(editorRequestForTest(a, "market", 1, 0, 0))
	base, cancel := context.WithCancel(a.ctx)
	defer cancel()
	ctx := cancelAfterSentContext{Context: base, cancel: cancel, sent: func() bool {
		pending, e := a.readStockEditorPending(a.state.ItemOptions)
		return e == nil && pending != nil && pending.Stage == "sent"
	}}
	if err := a.processLocalFirstStockEdit(ctx, a.apiKey, a.state.ItemOptions); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not returned", err)
	}
	pending, err := a.readStockEditorPending(a.state.ItemOptions)
	if err != nil || pending == nil || pending.Stage != "unsent" || len(fake.writes) != 0 {
		t.Fatalf("never-sent editor intent was quarantined as uncertain: %+v %v", pending, err)
	}
	if err = a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	if a.GetState().StockEditor.Pending || len(fake.writes) != 1 {
		t.Fatal("known-unsent correction was not sent exactly once")
	}
}

func TestLocalFirstStockEditorRefreshAndUnsentDiscardAreImmediateAndNetworkFree(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	a.OpenStockEditor()
	reads := fake.loads
	a.SaveStockEditorItem(editorRequestForTest(a, "market", 1, 0, 0))
	state := a.RefreshStockEditor()
	if state.StockEditor.Loading || state.StockEditor.Saving || !state.StockEditor.Pending || !state.StockEditor.Background || fake.loads != reads || len(fake.writes) != 0 {
		t.Fatal("public Refresh performed network or lost the saved intent")
	}
	state = a.DiscardStockEditorEdit()
	if state.StockEditor.Pending || fake.loads != reads || len(fake.writes) != 0 || editorRowForTest(t, state, trackingBSGID).MarketStock.Regular != 2 {
		t.Fatalf("never-sent discard did not restore the confirmed cached stock without network: %+v", state.StockEditor)
	}
}

func TestLocalFirstStockEditorSentDiscardCannotRaceAnActiveNetworkWorker(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	a.OpenStockEditor()
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	a.SaveStockEditorItem(editorRequestForTest(a, "market", 1, 0, 0))
	_ = a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions)
	path := a.stockEditorPendingPath("character-a", "PVP")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a.progressNetworkMu.Lock()
	state := a.DiscardStockEditorEdit()
	a.progressNetworkMu.Unlock()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || !state.StockEditor.Pending || len(fake.writes) != 1 {
		t.Fatal("active network verification lost the canonical intent")
	}
	state = a.DiscardStockEditorEdit()
	if state.StockEditor.Pending || state.StockEditor.Error != "" || len(fake.writes) != 1 || editorRowForTest(t, state, trackingBSGID).MarketStock.Regular != 2 {
		t.Fatal("idle explicit discard replayed a write or could not restore the confirmed view")
	}
}

func TestLocalFirstStockEditorLegacyIntentVerifiesFromDiskWithoutBackgroundFlag(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	opts := a.state.ItemOptions
	pending := &stockEditorPending{Version: 1, CharUID: opts.CharacterUID, Mode: "PVP", ItemID: trackingBSGID, Amounts: localprogress.StockAmounts{FIR: 1}, Revision: fake.snapshot.Revision}
	if err := a.saveStockEditorPending(pending); err != nil {
		t.Fatal(err)
	}
	fake.snapshot.Items[0].AvailableQuantity = 1
	fake.snapshot.Revision = "legacy-applied-result"
	a.restoreStockEditorLocked()
	if a.GetState().StockEditor.Background {
		t.Fatal("legacy fixture unexpectedly uses the v2 background flag")
	}
	reads := fake.loads
	if err := a.processLocalFirstStockEdit(a.ctx, a.apiKey, opts); err != nil {
		t.Fatal(err)
	}
	state := a.GetState()
	if state.StockEditor.Pending || fake.loads != reads+1 || len(fake.writes) != 0 {
		t.Fatal("legacy pending could not verify independently of the UI flag or replayed a write")
	}
	stored, err := a.localStore.Load(opts.CharacterUID, "PVP")
	row, _ := stockEditorItem(stored, trackingBSGID)
	if err != nil || row.MarketFIRQuantity != 1 || row.LocalRegularQuantity != 1 {
		t.Fatal("legacy verification lost source quality or changed own stock", err)
	}
}

func TestLocalFirstStockEditorOwnSourcesCorrectWithoutNetwork(t *testing.T) {
	for _, source := range []string{"local", "market-fallback"} {
		t.Run(source, func(t *testing.T) {
			a, fake := newDevOnlyTestApp(t)
			store := a.localStore
			if source == "market-fallback" {
				store = a.marketFallbackStock()
			}
			amounts := localprogress.StockAmounts{Regular: 2, FIR: 1}
			if _, err := store.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RecordHandover("character-a", "PVP", strings.Repeat("c", 32), []localprogress.HandoverEntry{{ItemID: trackingBSGID, Regular: 1}}); err != nil {
				t.Fatal(err)
			}
			primeLocalFirstForTest(t, a)
			reads := fake.loads
			fake.loadHook = func(int) (market.TrackedSnapshot, error) {
				t.Fatal("own stock edit performed a GET")
				return market.TrackedSnapshot{}, market.ErrProgressUnavailable
			}
			a.OpenStockEditor()
			state := a.SaveStockEditorItem(editorRequestForTest(a, source, 2, 3, 0))
			own, err := store.Load("character-a", "PVP")
			if err != nil {
				t.Fatal(err)
			}
			row, _ := stockEditorItem(own, trackingBSGID)
			if state.StockEditor.Error != "" || row.LocalAvailableQuantity != 5 || row.LocalCollectedQuantity != 6 || row.LocalUsedRegularQuantity != 1 || fake.loads != reads || len(fake.writes) != 0 {
				t.Fatalf("pure own edit violated history/network contract: %+v %+v", state.StockEditor, row)
			}
		})
	}
}

func TestLocalFirstStockEditorMarketIntentIsImmediateThenFreshCAS(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	a.OpenStockEditor()
	reads := fake.loads
	state := a.SaveStockEditorItem(editorRequestForTest(a, "market", 1, 0, 0))
	if state.StockEditor == nil || !state.StockEditor.Pending || !state.StockEditor.Background || state.StockEditor.Saving || state.StockEditor.PendingItemID != trackingBSGID || editorRowForTest(t, state, trackingBSGID).MarketStock.FIR != 1 || fake.loads != reads || len(fake.writes) != 0 || fake.snapshot.Items[0].AvailableQuantity != 2 {
		t.Fatalf("Market target was not durably immediate: %+v", state.StockEditor)
	}
	pending, err := a.readStockEditorPending(a.state.ItemOptions)
	if err != nil || pending.Version != 2 || pending.Stage != "unsent" || pending.Baseline.Regular != 2 {
		t.Fatalf("original unsent baseline missing: %+v %v", pending, err)
	}
	if err = a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	state = a.GetState()
	if state.StockEditor.Pending || state.StockEditor.Error != "" || len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || editorRowForTest(t, state, trackingBSGID).MarketStock.FIR != 1 || editorRowForTest(t, state, trackingBSGID).LocalStock.Regular != 1 {
		t.Fatalf("background correction changed independent source or did not verify: %+v", state.StockEditor)
	}
}

func TestLocalFirstStockEditorNeverVerifiesUnsentCoincidentalTarget(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	a.OpenStockEditor()
	a.SaveStockEditorItem(editorRequestForTest(a, "market", 1, 0, 0))
	fake.snapshot.Items[0].AvailableQuantity = 1
	fake.snapshot.Revision = "external-target"
	if err := a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions); err == nil {
		t.Fatal("unsent target was inferred as our write")
	}
	pending, err := a.readStockEditorPending(a.state.ItemOptions)
	if err != nil || pending == nil || pending.Stage != "conflict" || len(fake.writes) != 0 || !a.GetState().StockEditor.Pending {
		t.Fatalf("conflicting unsent intent was lost or sent: %+v %v", pending, err)
	}
	state := a.RefreshStockEditor()
	if !state.StockEditor.Pending || len(fake.writes) != 0 {
		t.Fatal("Refresh settled an unsent external target")
	}
}

func TestLocalFirstStockEditorNoOpReadbackCannotUseProjectedTarget(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	a.OpenStockEditor()
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	a.SaveStockEditorItem(editorRequestForTest(a, "market", 1, 0, 0))
	if err := a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions); err == nil {
		t.Fatal("no-op was verified")
	}
	// A different unrelated revision still has the original raw stock. The
	// displayed target must not masquerade as the GET observation.
	fake.snapshot.Revision = "other-item-revision"
	state := a.RefreshStockEditor()
	if !state.StockEditor.Pending || len(fake.writes) != 1 || fake.snapshot.Items[0].AvailableQuantity != 2 {
		t.Fatalf("projected target fabricated readback or replayed PATCH: %+v", state.StockEditor)
	}
}

func TestStockEditorPendingUploadAllowsOwnLocalCorrectionButLocksCapturedLanes(t *testing.T) {
	a, fake, opts, path := savedFallbackGateFixture(t)
	pendingBefore, _ := os.ReadFile(path)
	fallbackBefore := fallbackSyncAvailableForTest(t, a, opts)
	state := a.OpenStockEditor()
	row := editorRowForTest(t, state, trackingBSGID)
	if !state.MarketFallbackPending || !row.LocalEditable || row.MarketEditable || row.MarketFallbackEditable || row.EditError != marketFallbackItemBlockedMessage {
		t.Fatalf("pending upload did not expose source-specific permissions: %+v", row)
	}
	state = saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "local", 0, 4, 0))
	row = editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Error != "" || row.LocalStock.Regular != 4 || row.LocalUsed.Regular != 1 || row.MarketFallbackStock != fallbackBefore || len(fake.writes) != 0 {
		t.Fatalf("independent local correction was blocked or touched upload stock: %+v", state.StockEditor)
	}
	for _, source := range []string{"market", "market-fallback"} {
		reads := fake.loads
		state = saveAndDrainStockEditorForTest(a, editorRequestForTest(a, source, 0, 0, 0))
		if state.StockEditor.Error != marketFallbackItemBlockedMessage || state.StockEditor.Saving || fake.loads != reads || len(fake.writes) != 0 {
			t.Fatalf("%s pending lane sent a request or silently blocked: %+v", source, state.StockEditor)
		}
	}
	pendingAfter, _ := os.ReadFile(path)
	if !bytes.Equal(pendingBefore, pendingAfter) || fallbackSyncAvailableForTest(t, a, opts) != fallbackBefore {
		t.Fatal("local correction altered captured fallback or intent")
	}
	if edit, err := a.readStockEditorPending(opts); err != nil || edit != nil {
		t.Fatal("blocked corrections created a second intent")
	}
}

func TestStockEditorPendingUploadAllowsOtherItemSources(t *testing.T) {
	a, fake, opts, path := savedFallbackGateFixture(t)
	id := "another-stock-item"
	fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: id, Name: "Other item", RequiredQuantity: 3, RemainingQuantity: 3})
	for _, store := range []*localprogress.Store{a.localStore, a.marketFallbackStock()} {
		q := localprogress.StockAmounts{Regular: 1}
		if _, err := store.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: id, LocalAvailable: &q}); err != nil {
			t.Fatal(err)
		}
	}
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for i := range fake.snapshot.Items {
			if fake.snapshot.Items[i].ItemID == write.Item {
				fake.snapshot.Items[i].AvailableQuantity = write.Quantity
				fake.snapshot.Items[i].RemainingQuantity = max(0, fake.snapshot.Items[i].RequiredQuantity-write.Quantity)
			}
		}
		fake.snapshot.Revision = "unrelated-edit-verified"
		return market.ProgressWriteResult{OK: true, Changed: true, Revision: fake.snapshot.Revision}, nil
	}
	pendingBefore, _ := os.ReadFile(path)
	primeLocalFirstForTest(t, a)
	state := a.OpenStockEditor()
	row := editorRowForTest(t, state, id)
	if !row.MarketEditable || !row.MarketFallbackEditable || !row.LocalEditable {
		t.Fatalf("other item inherited pending source lock: %+v", row)
	}
	for _, source := range []string{"local", "market-fallback", "market"} {
		state = saveAndDrainStockEditorForTest(a, ItemStockEditRequest{Generation: a.GetState().StockEditor.Generation, ItemID: id, Source: source, Regular: 2})
		if state.StockEditor.Error != "" || state.StockEditor.Pending || state.StockEditor.Saving {
			t.Fatalf("unrelated %s source was blocked: %+v", source, state.StockEditor)
		}
	}
	pendingAfter, _ := os.ReadFile(path)
	if len(fake.writes) != 1 || fake.writes[0].Item != id || fake.writes[0].Quantity != 2 || !bytes.Equal(pendingBefore, pendingAfter) {
		t.Fatal("unrelated correction mutated the pending upload or wrong remote item")
	}
}

func editorRowForTest(t *testing.T, state ItemState, id string) ItemStockEditorRow {
	t.Helper()
	if state.StockEditor == nil || state.StockEditor.Loading || state.StockEditor.Saving {
		t.Fatal("editor unavailable")
	}
	for _, row := range state.StockEditor.Rows {
		if row.ItemID == id {
			return row
		}
	}
	t.Fatalf("item missing from editor: %+v", state.StockEditor)
	return ItemStockEditorRow{}
}

func editorRequestForTest(a *ItemWindowApp, source string, fir, regular, unknown int64) ItemStockEditRequest {
	return ItemStockEditRequest{Generation: a.GetState().StockEditor.Generation, ItemID: trackingBSGID, Source: source, FIR: fir, Regular: regular, Unknown: unknown}
}

func TestStockEditorUnsafeItemRejectsStaleEditableRowAndCoherentRefreshRestoresLocalCorrection(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	amounts := localprogress.StockAmounts{Regular: 2}
	if _, err := a.localStore.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	primeLocalFirstForTest(t, a)
	state := a.OpenStockEditor()
	if !editorRowForTest(t, state, trackingBSGID).LocalEditable {
		t.Fatal("valid local correction was unavailable before the partial update")
	}
	reads := fake.loads
	message := "Requirements for this item are being refreshed."
	a.mu.Lock()
	a.progressUnsafeItems = map[string]string{trackingBSGID: message}
	a.mu.Unlock()
	state = a.SaveStockEditorItem(editorRequestForTest(a, "local", 0, 1, 0))
	if state.StockEditor.Error != message || fake.loads != reads || len(fake.writes) != 0 || handoverStockForTest(t, a, trackingBSGID).LocalAvailableQuantity != 2 {
		t.Fatal("stale editable row bypassed the current unsafe-item guard")
	}
	state = a.RefreshStockEditor()
	row := editorRowForTest(t, state, trackingBSGID)
	if row.LocalEditable || row.MarketEditable || row.MarketFallbackEditable || row.EditError != message {
		t.Fatal("unsafe row still advertised a mutable source", row)
	}
	primeLocalFirstForTest(t, a)
	state = a.RefreshStockEditor()
	if !editorRowForTest(t, state, trackingBSGID).LocalEditable {
		t.Fatal("coherent refresh left the correction permanently disabled")
	}
	reads = fake.loads
	state = a.SaveStockEditorItem(editorRequestForTest(a, "local", 0, 1, 0))
	row = editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Error != "" || row.LocalStock.Regular != 1 || row.UsedQuantity != 0 || fake.loads != reads || len(fake.writes) != 0 {
		t.Fatal("restored local correction performed network I/O or fabricated usage", state.StockEditor)
	}
}

func TestStockEditorLocalCorrectionAndClearPreserveUsageWithoutInspectedItem(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	amounts := localprogress.StockAmounts{FIR: 2, Regular: 3, Unknown: 1}
	if _, err := a.localStore.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.localStore.RecordHandover("character-a", "PVP", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", []localprogress.HandoverEntry{{ItemID: trackingBSGID, Regular: 1}}); err != nil {
		t.Fatal(err)
	}
	a.state.Item = nil
	a.state.Tracking = nil
	row := editorRowForTest(t, a.OpenStockEditor(), trackingBSGID)
	if !row.LocalEditable || row.MarketEditable || row.AvailableQuantity != 5 || row.UsedQuantity != 1 || row.LocalUsed.Regular != 1 {
		t.Fatalf("incorrect stock/history: %+v", row)
	}
	state := saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "local", 1, 3, 0))
	row = editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Error != "" || row.LocalStock.FIR != 1 || row.LocalStock.Regular != 3 || row.AvailableQuantity != 4 || row.UsedQuantity != 1 || len(fake.writes) != 0 {
		t.Fatalf("local correction failed: %+v", state.StockEditor)
	}
	state = saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "local", 0, 0, 0))
	row = editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Error != "" || row.AvailableQuantity != 0 || row.UsedQuantity != 1 || len(fake.writes) != 0 {
		t.Fatalf("clear fabricated usage or wrote Market: %+v", row)
	}
	stock := handoverStockForTest(t, a, trackingBSGID)
	if stock.LocalCollectedQuantity != 1 || stock.LocalUsedQuantity != 1 {
		t.Fatalf("clear erased immutable use: %+v", stock)
	}
}

func TestStockEditorMarketQualityOnlyAndQuantityCorrectionKeepLocalSource(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 5; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	a.OpenStockEditor()
	writes := len(fake.writes)
	state := saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "market", 1, 2, 0))
	row := editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Error != "" || row.MarketStock.FIR != 1 || row.MarketStock.Regular != 2 || row.LocalStock.Regular != 2 || len(fake.writes) != writes || row.UsedQuantity != 0 {
		t.Fatalf("quality-only changed quantity/source: %+v", state.StockEditor)
	}
	state = saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "market", 1, 1, 0))
	row = editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Error != "" || state.StockEditor.Pending || row.MarketStock.FIR != 1 || row.MarketStock.Regular != 1 || row.LocalStock.Regular != 2 || row.AvailableQuantity != 4 || row.UsedQuantity != 0 || len(fake.writes) != writes+1 || fake.writes[writes].Quantity != 2 {
		t.Fatalf("Market correction failed: %+v", state.StockEditor)
	}
	if pending, err := a.readStockEditorPending(a.state.ItemOptions); err != nil || pending != nil {
		t.Fatal("verified intent was not cleared")
	}
}

func TestStockEditorNoOpMarketIsPendingAndRefreshNeverReplaysWrite(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.OpenStockEditor()
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	state := saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "market", 1, 0, 0))
	if state.StockEditor == nil || !state.StockEditor.Pending || state.StockEditor.Error == "" || len(fake.writes) != 1 || editorRowForTest(t, state, trackingBSGID).MarketStock.FIR != 1 {
		t.Fatalf("no-op was confirmed: %+v", state.StockEditor)
	}
	stored, err := a.localStore.Load("character-a", "PVP")
	if err != nil || len(stored.Items) != 0 || fake.snapshot.Items[0].AvailableQuantity != 0 {
		t.Fatal("pending projection fabricated a confirmed quality annotation")
	}
	a.RefreshStockEditor()
	_ = a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions)
	a.CloseStockEditor()
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 1 || !a.handoverBlocksItemLocked(trackingBSGID) && !a.stockEditorBlocksItemLocked(trackingBSGID) {
		t.Fatal("unverified edit allowed competing stock write or refresh replay")
	}
	a.restoreStockEditorLocked()
	if a.state.StockEditor == nil || !a.state.StockEditor.Pending || a.canAddLocalForTest() {
		t.Fatal("restart lost pending guard")
	}
	a.OpenStockEditor()
	state = a.DiscardStockEditorEdit()
	if state.StockEditor.Pending || state.StockEditor.Error != "" || len(fake.writes) != 1 || editorRowForTest(t, state, trackingBSGID).AvailableQuantity != 0 {
		t.Fatal("discard changed stock or kept pending")
	}
}

func TestStockEditorUncertainAppliedWriteCompletesByReadAfterRestart(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.OpenStockEditor()
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.commitWrite(write)
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	startReads := fake.loads
	fake.loadHook = func(n int) (market.TrackedSnapshot, error) {
		if n == startReads+2 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	state := saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "market", 1, 1, 0))
	if !state.StockEditor.Pending || len(fake.writes) != 1 {
		t.Fatal("uncertain intent not preserved")
	}
	fake.loadHook = nil
	a.restoreStockEditorLocked()
	state = a.OpenStockEditor()
	if !state.StockEditor.Pending {
		t.Fatal("opening the restored editor performed network verification")
	}
	_ = a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions)
	state = a.GetState()
	row := editorRowForTest(t, state, trackingBSGID)
	if state.StockEditor.Pending || state.StockEditor.Error != "" || row.MarketStock.FIR != 1 || row.MarketStock.Regular != 1 || row.AvailableQuantity != 2 || row.UsedQuantity != 0 || len(fake.writes) != 1 {
		t.Fatalf("read-only resume failed: %+v", state.StockEditor)
	}
}

func TestStockEditorRejectsStaleGenerationAndExternallyChangedMarket(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.OpenStockEditor()
	request := editorRequestForTest(a, "market", 1, 0, 0)
	a.RefreshStockEditor()
	saveAndDrainStockEditorForTest(a, request)
	if len(fake.writes) != 0 {
		t.Fatal("stale form wrote stock")
	}
	request = editorRequestForTest(a, "market", 2, 0, 0)
	fake.commitWrite(stockWrite{"character-a", trackingBSGID, fake.snapshot.Revision, 1})
	state := saveAndDrainStockEditorForTest(a, request)
	if len(fake.writes) != 0 || state.StockEditor.Error == "" || !state.StockEditor.Pending || editorRowForTest(t, state, trackingBSGID).AvailableQuantity != 1 {
		t.Fatal("external change was overwritten")
	}
	pending, err := a.readStockEditorPending(a.state.ItemOptions)
	if err != nil || pending == nil || pending.Stage != "conflict" {
		t.Fatal("immutable conflicting correction was not kept")
	}
}

func TestStockEditorLocalEditUsesConfirmedCacheAndRejectsKnownDuplicateVariants(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	a.OpenStockEditor()
	request := editorRequestForTest(a, "local", 1, 0, 0)
	fake.snapshot.Items = []market.TrackedItem{{ItemID: trackingBSGID, Name: "Full"}, {ItemID: trackingBSGID, Name: "Partial"}}
	reads := fake.loads
	state := saveAndDrainStockEditorForTest(a, request)
	if state.StockEditor.Error != "" || fake.loads != reads || len(fake.writes) != 0 || handoverStockForTest(t, a, trackingBSGID).LocalAvailableQuantity != 1 {
		t.Fatal("own local edit ignored the confirmed cache or made a network request")
	}
	primeLocalFirstForTest(t, a)
	a.RefreshStockEditor()
	state = saveAndDrainStockEditorForTest(a, editorRequestForTest(a, "local", 2, 0, 0))
	if state.StockEditor.Error == "" || len(fake.writes) != 0 || handoverStockForTest(t, a, trackingBSGID).LocalAvailableQuantity != 1 {
		t.Fatal("known duplicate variants allowed a new ambiguous correction")
	}
}

func TestStockEditorMarketAnnotationsDoNotCreateLocalOwnership(t *testing.T) {
	a, _ := newLocalTestApp(t)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
	row := editorRowForTest(t, a.OpenStockEditor(), trackingBSGID)
	if !row.MarketEditable || row.LocalEditable {
		t.Fatalf("Market-only type annotation became local ownership: %+v", row)
	}
}

func TestStockEditorHandInUsageIsDurableAndMarketSpentIsSeparate(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 5; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 3, "regular"))
	requireHandoverComplete(t, confirmAndDrainHandoverForTest(a))
	a.CloseHandover()
	fake.snapshot.Items[0].SpentQuantity = 3
	primeLocalFirstForTest(t, a)
	row := editorRowForTest(t, a.OpenStockEditor(), trackingBSGID)
	if row.LocalUsed.Regular != 2 || row.MarketHandedIn.Regular != 1 || row.UsedQuantity != 3 || row.MarketSpentQuantity != 3 || row.AvailableQuantity != 2 {
		t.Fatalf("usage lost, double counted, or kept in stock: %+v", row)
	}
	a.RefreshStockEditor()
	row = editorRowForTest(t, a.GetState(), trackingBSGID)
	if row.UsedQuantity != 3 {
		t.Fatal("history was not stable after refresh")
	}
	if _, err := os.Stat(a.handoverPath("character-a", "PVP")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed hand-in journal remained")
	}
}

func TestStockEditorContextAndBusyGuardsKeepSourceUntouched(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.OpenStockEditor()
	request := editorRequestForTest(a, "market", 1, 0, 0)
	a.trackingMu.Lock()
	state := saveAndDrainStockEditorForTest(a, request)
	a.trackingMu.Unlock()
	if state.StockEditor.Error != progressBusyMessage || len(fake.writes) != 0 {
		t.Fatal("busy save was queued or silent")
	}
	opts := a.state.ItemOptions
	opts.CharacterUID = "character-b"
	a.SetOptions(opts)
	saveAndDrainStockEditorForTest(a, request)
	if len(fake.writes) != 0 {
		t.Fatal("old character form mutated new character")
	}
}

func TestStockEditorPausesReservedShortcutsAndRestoresThemWhenClosed(t *testing.T) {
	a, _ := newLocalTestApp(t)
	a.state.AddHotkey = "1"
	a.state.FIRHotkey = "2"
	a.state.AddHotkeyReady = true
	a.addHotkey = &trackedAddHotkey{}
	a.configureTrackedAddHotkey()
	if binding := a.addHotkey.bindings.Load(); binding == nil || binding.regular != uint32('1') || binding.fir != uint32('2') {
		t.Fatal("fixture keys absent")
	}
	state := a.OpenStockEditor()
	if binding := a.addHotkey.bindings.Load(); binding == nil || binding.regular != 0 || binding.fir != 0 || state.AddHotkeyReady || state.FIRHotkeyReady {
		t.Fatal("stock quantity typing could consume digit shortcut")
	}
	a.CloseStockEditor()
	if binding := a.addHotkey.bindings.Load(); binding == nil || binding.regular != uint32('1') || binding.fir != uint32('2') {
		t.Fatal("editor close did not restore shortcuts")
	}
}

func TestStockEditorUsageCapacityStopsHandInBeforeEitherSourceIsDeducted(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 4; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	fake.commitWrite(stockWrite{"character-a", trackingBSGID, fake.snapshot.Revision, 1})
	used := localprogress.HandoverEntry{ItemID: trackingBSGID, Regular: localprogress.MaxQuantity}
	previous := &handoverTransaction{Version: 1, ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CharUID: "character-a", Mode: "PVP", LocalDone: true,
		Items: []handoverOperation{{ItemID: trackingBSGID, Market: used, RegularToMarket: localprogress.MaxQuantity, Stage: "confirmed"}}}
	if err := a.recordStockEditorMarketUsage(previous, &previous.Items[0]); err != nil {
		t.Fatal(err)
	}
	path, _, err := a.stockUsagePath("character-a", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writes := len(fake.writes)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	state := confirmAndDrainHandoverForTest(a)
	if state.Handover == nil || state.Handover.Error == "" || state.Handover.Pending || a.handover != nil {
		t.Fatal("capacity failure was not rejected before intent preparation")
	}
	if _, err := os.Stat(a.handoverPath("character-a", "PVP")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("capacity rejection created a hand-in intent")
	}
	stock := handoverStockForTest(t, a, trackingBSGID)
	if len(fake.writes) != writes || fake.snapshot.Items[0].AvailableQuantity != 1 || stock.LocalAvailableQuantity != 1 || stock.LocalUsedQuantity != 0 {
		t.Fatalf("unrecordable history deducted stock: stock=%+v tx=%+v", stock, a.handover)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("capacity rejection changed existing history")
	}
}

func saveAndDrainStockEditorForTest(a *ItemWindowApp, request ItemStockEditRequest) ItemState {
	state := a.SaveStockEditorItem(request)
	if state.StockEditor != nil && state.StockEditor.Background {
		_ = a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions)
	}
	return a.GetState()
}
