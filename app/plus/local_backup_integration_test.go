package plus

import (
	"path/filepath"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

func TestLocalBackupBusyPreservesIdentityAndBlocksCompetingActions(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.state.LocalBackup = &ItemLocalBackupState{Busy: true, Status: "Reviewing backup."}
	opts, key := a.state.ItemOptions, a.apiKey
	reads := fake.loads
	other := opts
	other.CharacterUID, other.Mode, other.AddHotkey = "other-character", "pve", "F8"
	a.SetOptions(other)
	a.SaveAPIKey("different-test-key")
	a.ClearAPIKey()
	a.SetHotkeyCapture(true)
	a.OpenStockEditor()
	a.OpenHandover()
	a.AddTrackedItemFIR(trackingItemUID)
	if a.state.ItemOptions != opts || a.apiKey != key || !a.state.HasAPIKey || a.state.HotkeyCapture ||
		a.state.StockEditor != nil || a.state.Handover != nil || len(fake.writes) != 0 || fake.loads != reads {
		t.Fatal("backup review permitted a competing action or changed identity")
	}
	state := a.GetState()
	state.LocalBackup.Busy = false
	state.LocalBackup.Status = "Changed outside the app."
	if !a.state.LocalBackup.Busy || a.state.LocalBackup.Status != "Reviewing backup." {
		t.Fatal("frontend snapshot aliases mutable backup state")
	}
}

func TestLocalBackupImportResetsLiveAllocationAndRestoresPendingWithoutAPI(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.localStore = localprogress.NewStore(filepath.Join(a.dir, "local-progress"))
	amounts := localprogress.StockAmounts{FIR: 2}
	if _, err := a.localStore.CorrectStock(a.state.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.localStore.RecordHandover(a.state.CharacterUID, "PVP", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", []localprogress.HandoverEntry{{ItemID: trackingBSGID, FIR: 1}}); err != nil {
		t.Fatal(err)
	}
	fallback := localprogress.StockAmounts{Regular: 3}
	if _, err := a.marketFallbackStock().CorrectStock(a.state.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &fallback}); err != nil {
		t.Fatal(err)
	}
	if err := a.saveStockEditorPending(&stockEditorPending{Version: 1, CharUID: a.state.CharacterUID, Mode: "PVP", ItemID: trackingBSGID, Amounts: localprogress.StockAmounts{FIR: 1}, Revision: "revision-1"}); err != nil {
		t.Fatal(err)
	}
	a.state.StockEditor = &ItemStockEditorState{Open: true}
	a.state.LocalBackup = &ItemLocalBackupState{Busy: true}
	a.trackingRequests = make(chan struct{}, 1)
	a.requestTracking()
	oldStore, oldRevision, reads := a.localStore, a.state.ItemOptions.revision, fake.loads
	a.mu.Lock()
	a.resetAfterLocalProgressImportLocked()
	a.mu.Unlock()
	if a.localStore == oldStore || a.fallbackStore != nil || len(a.localSnapshot.Items) != 0 ||
		a.state.ItemOptions.revision != oldRevision+1 || a.state.Local != nil || a.state.Tracking != nil ||
		a.state.StockEditor == nil || !a.state.StockEditor.Open || !a.state.StockEditor.Pending ||
		!a.state.LocalBackup.Busy || len(a.trackingRequests) != 0 || len(fake.writes) != 0 || fake.loads != reads {
		t.Fatal("import retained stale allocation, lost the pending guard or made an API call")
	}
	stock, err := a.localStore.Load(a.state.CharacterUID, "PVP")
	if err != nil || len(stock.Items) != 1 || stock.Items[0].LocalAvailableQuantity != 1 || stock.Items[0].LocalUsedFIRQuantity != 1 {
		t.Fatal("store reload lost imported stock or usage")
	}
	stock, err = a.loadMarketFallback(a.state.ItemOptions)
	if err != nil || len(stock.Items) != 1 || stock.Items[0].LocalAvailableQuantity != 3 || stock.Items[0].LocalRegularQuantity != 3 {
		t.Fatal("fallback handle reset lost imported stock")
	}
}
