package plus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localbackup"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func backupTestApp(t *testing.T) *ItemWindowApp {
	t.Helper()
	root := t.TempDir()
	a := &ItemWindowApp{dir: root, ctx: context.Background(), localStore: localprogress.NewStore(filepath.Join(root, "local-progress")), trackingRequests: make(chan struct{}, 1)}
	a.state.ItemOptions = ItemOptions{Enabled: true, Mode: "pvp", CharacterUID: "char-a", AddHotkey: "F2", FIRHotkey: "F1", revision: 1}
	return a
}
func backupCorrect(t *testing.T, store *localprogress.Store, char, mode, id string, amounts localprogress.StockAmounts) {
	t.Helper()
	if _, err := store.CorrectStock(char, mode, localprogress.ManualStockCorrection{ItemID: id, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
}
func backupFixture(t *testing.T) *ItemWindowApp {
	t.Helper()
	a := backupTestApp(t)
	backupCorrect(t, a.localStore, "char-a", "PVP", "A", localprogress.StockAmounts{Regular: 4, FIR: 3, Unknown: 2})
	if _, err := a.localStore.RecordHandover("char-a", "PVP", "old-own-use", []localprogress.HandoverEntry{{ItemID: "A", Regular: 1, FIR: 1, Unknown: 1}}); err != nil {
		t.Fatal(err)
	}
	backupCorrect(t, a.marketFallbackStock(), "char-a", "PVP", "A", localprogress.StockAmounts{Regular: 3, FIR: 2, Unknown: 1})
	if _, err := a.marketFallbackStock().RecordHandover("char-a", "PVP", "old-fallback-use", []localprogress.HandoverEntry{{ItemID: "A", Regular: 1}}); err != nil {
		t.Fatal(err)
	}
	backupCorrect(t, a.localStore, "char-a", "PVE", "B", localprogress.StockAmounts{FIR: 2})
	backupCorrect(t, a.localStore, "char-b", "SEASON", "C", localprogress.StockAmounts{Unknown: 5})
	path, _, err := a.stockUsagePath("char-a", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	history := stockUsageHistory{Version: 1, CharUID: "char-a", GameMode: "PVP", Receipts: map[string]map[string]localprogress.HandoverEntry{strings.Repeat("c", 32): {"A": {ItemID: "A", Regular: 2}}}}
	data, err := encodeStockUsage(history)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	if os.WriteFile(path, data, 0600) != nil {
		t.Fatal("usage fixture")
	}
	_ = os.WriteFile(filepath.Join(a.dir, "api-key.bin"), []byte("private-credential"), 0600)
	_ = os.WriteFile(filepath.Join(a.dir, "item-settings.json"), []byte("private-settings"), 0600)
	return a
}
func backupAddPendingFixture(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	txID := strings.Repeat("b", 32)
	local := localprogress.HandoverEntry{ItemID: "A", Regular: 1, FIR: 1}
	fallback := localprogress.HandoverEntry{ItemID: "A", Regular: 1}
	if _, err := a.localStore.RecordHandover("char-a", "PVP", txID, []localprogress.HandoverEntry{local}); err != nil {
		t.Fatal(err)
	}
	tx := &handoverTransaction{Version: 1, ID: txID, CharUID: "char-a", Mode: "PVP", Draft: []ItemHandoverEntry{{ID: "draft-r", ItemID: "A", Name: "Item A", Quantity: 2, Kind: "regular"}, {ID: "draft-f", ItemID: "A", Name: "Item A", Quantity: 1, Kind: "fir"}}, Items: []handoverOperation{{ItemID: "A", Name: "Item A", Local: local, Fallback: fallback, Market: localprogress.HandoverEntry{ItemID: "A"}}}}
	if a.saveHandover(tx) != nil {
		t.Fatal("handover fixture")
	}
	if a.saveStockEditorPending(&stockEditorPending{Version: 1, CharUID: "char-a", Mode: "PVE", ItemID: "B", Amounts: localprogress.StockAmounts{FIR: 1}, Revision: "revision-before"}) != nil {
		t.Fatal("edit fixture")
	}
	backupCorrect(t, a.marketFallbackStock(), "char-b", "SEASON", "C", localprogress.StockAmounts{Unknown: 2})
	opts := ItemOptions{CharacterUID: "char-b", Mode: "season"}
	if a.saveMarketFallbackSync(opts, &marketFallbackSyncIntent{Version: 1, CharUID: "char-b", Mode: "SEASON", ItemID: "C", Revision: "revision-before", Before: 1, Target: 3, Fallback: localprogress.StockAmounts{Unknown: 2}, MarketAfter: localprogress.StockAmounts{Unknown: 3}}) != nil {
		t.Fatal("sync fixture")
	}
}
func backupFilesOnDisk(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range localbackup.Directories {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(root, dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[dir+"/"+entry.Name()] = string(data)
		}
	}
	return out
}
func injectBackupDialogs(t *testing.T, dialogs localBackupDialogs) {
	t.Helper()
	old := itemLocalBackupDialogs
	itemLocalBackupDialogs = dialogs
	t.Cleanup(func() { itemLocalBackupDialogs = old })
}
func writeBackupFixture(t *testing.T, a *ItemWindowApp) string {
	t.Helper()
	backup, _, err := collectLocalProgressBackup(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := encodeLocalProgressBackup(backup)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "save"+localBackupExtension)
	if os.WriteFile(path, data, 0600) != nil {
		t.Fatal("backup")
	}
	return path
}
func TestLocalBackupDeterministicAllProfilesTypedUsedAndPrivacy(t *testing.T) {
	a := backupFixture(t)
	backup, summary, err := collectLocalProgressBackup(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Profiles != 3 || summary.Items != 3 || summary.Available != 18 || summary.Used != 6 || summary.Pending != 0 {
		t.Fatalf("wrong summary: %+v", summary)
	}
	first, _ := encodeLocalProgressBackup(backup)
	secondBackup, _, _ := collectLocalProgressBackup(a.dir)
	second, _ := encodeLocalProgressBackup(secondBackup)
	if !bytes.Equal(first, second) {
		t.Fatal("backup not deterministic")
	}
	if bytes.Contains(first, []byte("private-credential")) || bytes.Contains(first, []byte("private-settings")) {
		t.Fatal("credentials/settings leaked")
	}
	decoded, decodedSummary, err := decodeLocalProgressBackup(first)
	if err != nil || decodedSummary != summary || len(decoded.Files) != len(backup.Files) {
		t.Fatal("roundtrip", err)
	}
	for _, file := range decoded.Files {
		for _, forbidden := range []string{"apiKey", "requiredQuantity", "availableQuantity", "spentQuantity", "snapshot", "quests"} {
			if bytes.Contains(file.Data, []byte(`"`+forbidden+`"`)) {
				t.Fatalf("remote mirror leaked in %s", file.Path)
			}
		}
	}
}
func TestLocalBackupCarriesThreePendingKindsAndPartialOwnReceipt(t *testing.T) {
	a := backupFixture(t)
	backupAddPendingFixture(t, a)
	backup, summary, err := collectLocalProgressBackup(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pending != 3 {
		t.Fatal("pending operations omitted", summary)
	}
	data, _ := encodeLocalProgressBackup(backup)
	if _, _, err := decodeLocalProgressBackup(data); err != nil {
		t.Fatal("valid partial local settlement rejected", err)
	}
	for _, dir := range []string{"handovers", "stock-edits", "market-fallback-sync"} {
		found := false
		for _, file := range backup.Files {
			found = found || strings.HasPrefix(file.Path, dir+"/")
		}
		if !found {
			t.Fatal("missing journal", dir)
		}
	}
}

func TestLocalBackupScopedPendingCoexistencePreservesIndependentSourceReceipts(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		valid bool
	}{{"same-item-dev-only", true}, {"same-item-dev-partial", true}, {"same-item-dev-settled", true}, {"other-item-three-pools", true},
		{"other-item-stock-edit", true}, {"same-item-market", false}, {"same-item-fallback", false},
		{"same-item-stock-edit", false}, {"handover-plus-stock-edit", false}} {
		t.Run(scenario.name, func(t *testing.T) {
			a := backupFixture(t)
			char, mode, syncID := "char-b", "SEASON", "C"
			opts := ItemOptions{CharacterUID: char, Mode: "season"}
			backupCorrect(t, a.marketFallbackStock(), char, mode, syncID, localprogress.StockAmounts{Unknown: 2})
			intent := &marketFallbackSyncIntent{Version: 1, CharUID: char, Mode: mode, ItemID: syncID, Revision: "saved-upload-revision",
				Before: 1, Target: 3, Fallback: localprogress.StockAmounts{Unknown: 2}, MarketAfter: localprogress.StockAmounts{Unknown: 3}}
			if err := a.saveMarketFallbackSync(opts, intent); err != nil {
				t.Fatal(err)
			}
			id := syncID
			if scenario.name == "other-item-three-pools" || scenario.name == "other-item-stock-edit" {
				id = "D"
				backupCorrect(t, a.localStore, char, mode, id, localprogress.StockAmounts{Regular: 2})
				backupCorrect(t, a.marketFallbackStock(), char, mode, id, localprogress.StockAmounts{Regular: 2})
			}
			if strings.Contains(scenario.name, "stock-edit") {
				if err := a.saveStockEditorPending(&stockEditorPending{Version: 1, CharUID: char, Mode: mode, ItemID: id,
					Amounts: localprogress.StockAmounts{Regular: 1}, Revision: "saved-edit-revision"}); err != nil {
					t.Fatal(err)
				}
			}
			if !strings.Contains(scenario.name, "stock-edit") || scenario.name == "handover-plus-stock-edit" {
				local := localprogress.HandoverEntry{ItemID: id, Unknown: 1}
				fallback, remote := localprogress.HandoverEntry{ItemID: id}, localprogress.HandoverEntry{ItemID: id}
				quantity := int64(1)
				if scenario.name == "other-item-three-pools" {
					local.Regular, local.Unknown, fallback.Regular, remote.Unknown, quantity = 1, 0, 1, 1, 3
				}
				if scenario.name == "same-item-market" {
					remote.Unknown, quantity = 1, 2
				}
				if scenario.name == "same-item-fallback" {
					fallback.Unknown, quantity = 1, 2
				}
				txID := strings.Repeat("d", 32)
				if scenario.name == "same-item-dev-partial" || scenario.name == "same-item-dev-settled" {
					if _, err := a.localStore.RecordHandover(char, mode, txID, []localprogress.HandoverEntry{local}); err != nil {
						t.Fatal(err)
					}
				}
				tx := &handoverTransaction{Version: 1, ID: txID, CharUID: char, Mode: mode,
					LocalDone: scenario.name == "same-item-dev-settled",
					Draft:     []ItemHandoverEntry{{ID: "draft", ItemID: id, Name: "Saved item", Quantity: quantity, Kind: "regular"}},
					Items: []handoverOperation{{ItemID: id, Name: "Saved item", Local: local, Fallback: fallback, Market: remote,
						RegularToMarket: remote.Unknown, Target: remote.Unknown}}}
				if err := a.saveHandover(tx); err != nil {
					t.Fatal(err)
				}
			}
			before := backupFilesOnDisk(t, a.dir)
			backup, summary, err := collectLocalProgressBackup(a.dir)
			if !scenario.valid {
				if err == nil {
					t.Fatal("overlapping pending sources accepted")
				}
				return
			}
			if err != nil || summary.Pending != 2 {
				t.Fatal("independent pending lanes not portable", summary, err)
			}
			data, err := encodeLocalProgressBackup(backup)
			if err != nil {
				t.Fatal(err)
			}
			decoded, got, err := decodeLocalProgressBackup(data)
			if err != nil || got != summary {
				t.Fatal("valid coexistence roundtrip failed", err)
			}
			target := backupTestApp(t)
			files := make([]localbackup.File, len(decoded.Files))
			for i, file := range decoded.Files {
				files[i] = localbackup.File{Path: file.Path, Data: file.Data}
			}
			if err := localbackup.Restore(target.dir, files); err != nil {
				t.Fatal(err)
			}
			after := backupFilesOnDisk(t, target.dir)
			if len(before) != len(after) {
				t.Fatal("coexisting journals or receipts omitted")
			}
			for path, raw := range before {
				if after[path] != raw {
					t.Fatal("coexisting pending state/receipt mutated on restore", path)
				}
			}
		})
	}
}
func TestLocalBackupPublicExportAndImportWithoutAPIReplacesAllSixAndKeepsOld(t *testing.T) {
	source := backupFixture(t)
	backupAddPendingFixture(t, source)
	before := backupFilesOnDisk(t, source.dir)
	target := backupTestApp(t)
	backupCorrect(t, target.localStore, "char-a", "PVP", "OLD", localprogress.StockAmounts{Regular: 3})
	_ = os.WriteFile(filepath.Join(target.dir, "api-key.bin"), []byte("old-key"), 0600)
	path := filepath.Join(t.TempDir(), "move"+localBackupExtension)
	confirmCount := 0
	injectBackupDialogs(t, localBackupDialogs{save: func(context.Context, wailsRuntime.SaveDialogOptions) (string, error) { return path, nil }, open: func(context.Context, wailsRuntime.OpenDialogOptions) (string, error) { return path, nil }, confirm: func(_ context.Context, opts wailsRuntime.MessageDialogOptions) (string, error) {
		confirmCount++
		if !strings.Contains(opts.Message, "3 pending operations") || !strings.Contains(opts.Message, "REPLACES") || !strings.Contains(opts.Message, "Current:") {
			t.Fatal("review summary incomplete", opts.Message)
		}
		if opts.Type != wailsRuntime.QuestionDialog || opts.DefaultButton != "No" || opts.CancelButton != "No" {
			t.Fatal("unsafe native confirmation contract")
		}
		return "Yes", nil
	}})
	state := source.ExportLocalProgress()
	if state.LocalBackup == nil || state.LocalBackup.Busy || state.LocalBackup.Error != "" {
		t.Fatal("export final response", state.LocalBackup)
	}
	oldRevision := target.state.ItemOptions.revision
	state = target.ImportLocalProgress()
	if state.LocalBackup == nil || state.LocalBackup.Busy || state.LocalBackup.Error != "" || confirmCount != 1 {
		t.Fatal("import final response", state.LocalBackup)
	}
	after := backupFilesOnDisk(t, target.dir)
	if len(after) != len(before) {
		t.Fatal("replacement omitted files")
	}
	for path, data := range before {
		if after[path] != data {
			t.Fatal("changed identity/receipt/types", path)
		}
	}
	if state.HasAPIKey || target.state.ItemOptions.revision != oldRevision+1 || state.Handover == nil || !state.Handover.Pending {
		t.Fatal("no-key import/pending reset failed")
	}
	key, _ := os.ReadFile(filepath.Join(target.dir, "api-key.bin"))
	if string(key) != "old-key" {
		t.Fatal("key replaced")
	}
	history, err := os.ReadDir(filepath.Join(target.dir, ".local-progress-backups"))
	if err != nil || len(history) != 1 {
		t.Fatal("automatic backup missing")
	}
	old, err := localprogress.NewStore(filepath.Join(target.dir, ".local-progress-backups", history[0].Name(), "local-progress")).Load("char-a", "PVP")
	if err != nil || len(old.Items) != 1 || old.Items[0].ItemID != "OLD" || old.Items[0].LocalAvailableQuantity != 3 {
		t.Fatal("old data missing")
	}
	if len(target.trackingRequests) != 0 {
		t.Fatal("import queued an API refresh")
	}
}
func TestLocalBackupCancelCorruptionAndBusyPreserveCurrent(t *testing.T) {
	source := backupFixture(t)
	path := writeBackupFixture(t, source)
	for _, scenario := range []string{"cancel", "corrupt", "busy", "bad-current"} {
		t.Run(scenario, func(t *testing.T) {
			target := backupFixture(t)
			before := backupFilesOnDisk(t, target.dir)
			dialogs := 0
			input := path
			if scenario == "corrupt" {
				input = filepath.Join(t.TempDir(), "bad.json")
				_ = os.WriteFile(input, []byte(`{"format":"bad"}`), 0600)
			}
			if scenario == "bad-current" {
				files := backupFilesOnDisk(t, target.dir)
				for p := range files {
					if strings.HasPrefix(p, "local-progress/") {
						_ = os.WriteFile(filepath.Join(target.dir, filepath.FromSlash(p)), []byte("corrupt-current"), 0600)
						break
					}
				}
				before = backupFilesOnDisk(t, target.dir)
			}
			injectBackupDialogs(t, localBackupDialogs{open: func(context.Context, wailsRuntime.OpenDialogOptions) (string, error) { dialogs++; return input, nil }, confirm: func(context.Context, wailsRuntime.MessageDialogOptions) (string, error) { return "Cancel", nil }})
			if scenario == "busy" {
				target.trackingMu.Lock()
			}
			state := target.ImportLocalProgress()
			if scenario == "busy" {
				target.trackingMu.Unlock()
				if dialogs != 0 {
					t.Fatal("busy operation opened dialog")
				}
			}
			if state.LocalBackup == nil || state.LocalBackup.Busy || state.LocalBackup.RecoveryRequired {
				t.Fatal("busy final result")
			}
			after := backupFilesOnDisk(t, target.dir)
			for p, data := range before {
				if after[p] != data {
					t.Fatal("failure changed current file")
				}
			}
		})
	}
}

func TestLocalBackupFailedRollbackPublishesRecoveryRequiredAndKeepsOldCopies(t *testing.T) {
	source := backupFixture(t)
	path := writeBackupFixture(t, source)
	target := backupFixture(t)
	before := backupFilesOnDisk(t, target.dir)
	dialogCalls := 0
	injectBackupDialogs(t, localBackupDialogs{
		open: func(context.Context, wailsRuntime.OpenDialogOptions) (string, error) {
			dialogCalls++
			return path, nil
		},
		confirm: func(context.Context, wailsRuntime.MessageDialogOptions) (string, error) {
			return "Yes", nil
		},
	})
	oldRestore := itemLocalBackupRestore
	itemLocalBackupRestore = func(root string, _ []localbackup.File) error {
		work := filepath.Join(root, ".local-progress-import", "old")
		if err := os.MkdirAll(work, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "local-progress"), filepath.Join(work, "local-progress")); err != nil {
			t.Fatal(err)
		}
		return localbackup.ErrRestore
	}
	t.Cleanup(func() { itemLocalBackupRestore = oldRestore })
	state := target.ImportLocalProgress()
	if state.LocalBackup == nil || state.LocalBackup.Busy || !state.LocalBackup.RecoveryRequired || state.LocalBackup.Error != localBackupRecoveryMessage {
		t.Fatalf("incomplete rollback did not block later writes: %+v", state.LocalBackup)
	}
	for name, data := range before {
		if !strings.HasPrefix(name, "local-progress/") {
			continue
		}
		actual, err := os.ReadFile(filepath.Join(target.dir, ".local-progress-import", "old", filepath.FromSlash(name)))
		if err != nil || string(actual) != data {
			t.Fatal("failed rollback lost original stock", err)
		}
	}
	if state.ItemOptions.revision != 1 || len(target.trackingRequests) != 0 {
		t.Fatal("failed import published replacement or queued network work")
	}
	target.ExportLocalProgress()
	target.ImportLocalProgress()
	if dialogCalls != 1 || target.state.LocalBackup.Error != localBackupRecoveryMessage || !target.state.LocalBackup.RecoveryRequired {
		t.Fatal("blocked recovery opened another dialog or hid its reason")
	}
}

func TestLocalBackupRecoveryRequiredBlocksStockAndIdentityChangesWithoutAPI(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	a.AddTrackedItem(trackingItemUID)
	before, err := a.localStore.Load("character-a", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	key, opts := a.apiKey, a.state.ItemOptions
	a.state.LocalBackup = &ItemLocalBackupState{RecoveryRequired: true, Error: localBackupRecoveryMessage}
	reads, questReads, hideoutReads, writes := fake.loads, fake.questReads, fake.hideoutReads, len(fake.writes)
	a.AddTrackedItem(trackingItemUID)
	a.AddTrackedItemFIR(trackingItemUID)
	a.state.Handover = &ItemHandoverState{Open: true, CharUID: "character-a", Items: []ItemHandoverEntry{handoverRowForTest(trackingBSGID, 1, "regular")}}
	a.ConfirmHandover()
	changed := opts
	changed.Mode = "pve"
	a.SetOptions(changed)
	a.SaveAPIKey("another-key")
	after, err := a.localStore.Load("character-a", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if !bytes.Equal(beforeJSON, afterJSON) || fake.loads != reads || fake.questReads != questReads || fake.hideoutReads != hideoutReads || len(fake.writes) != writes ||
		a.apiKey != key || a.state.ItemOptions != opts || a.state.Handover.Busy || a.state.Handover.Completed {
		t.Fatal("incomplete recovery allowed stock/API/identity mutation")
	}
	if _, err := os.Stat(a.handoverPath("character-a", "PVP")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete recovery created a hand-in journal")
	}
	encoded, _ := json.Marshal(a.GetState().LocalBackup)
	if !bytes.Contains(encoded, []byte(`"recoveryRequired":true`)) || !bytes.Contains(encoded, []byte(`"busy":false`)) {
		t.Fatal("recovery guard is not represented in the UI contract")
	}
}
func TestLocalBackupRejectsDuplicateEnvelopeTraversalUnknownAndChecksum(t *testing.T) {
	a := backupFixture(t)
	backup, _, _ := collectLocalProgressBackup(a.dir)
	data, _ := encodeLocalProgressBackup(backup)
	for _, bad := range [][]byte{bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 1, "version": 1`), 1), bytes.Replace(data, []byte(`"format"`), []byte(`"Format"`), 1), append(append([]byte{}, data...), []byte("{}")...), bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 2`), 1)} {
		if _, _, err := decodeLocalProgressBackup(bad); err == nil {
			t.Fatal("malformed envelope accepted")
		}
	}
	for _, edit := range []func(*localProgressBackup){func(b *localProgressBackup) { b.Files[0].Path = "../api-key.bin" }, func(b *localProgressBackup) { b.Files = append(b.Files, b.Files[0]) }, func(b *localProgressBackup) { b.Files[0].Data = append(b.Files[0].Data, ' ') }, func(b *localProgressBackup) { b.Files[0].Path = strings.Replace(b.Files[0].Path, "/", "\\", 1) }, func(b *localProgressBackup) { b.Files[0].Path = "api-key/" + filepath.Base(b.Files[0].Path) }} {
		copy := backup
		copy.Files = append([]localBackupFile{}, backup.Files...)
		edit(&copy)
		bad, _ := encodeLocalProgressBackup(copy)
		if _, _, err := decodeLocalProgressBackup(bad); err == nil {
			t.Fatal("invalid path/data accepted")
		}
	}
}
func TestLocalBackupRejectsInvalidPendingCrossLedgerBeforeReplacing(t *testing.T) {
	for _, scenario := range []string{"local-done-missing-fallback-receipt", "stage-before-local-done", "fallback-intent-mismatch", "fallback-intent-missing-stock", "wrong-profile-filename", "duplicate-source-key", "unknown-file"} {
		t.Run(scenario, func(t *testing.T) {
			a := backupFixture(t)
			backupAddPendingFixture(t, a)
			switch scenario {
			case "local-done-missing-fallback-receipt", "stage-before-local-done":
				path := a.handoverPath("char-a", "PVP")
				data, _ := os.ReadFile(path)
				var tx handoverTransaction
				_ = json.Unmarshal(data, &tx)
				if scenario == "local-done-missing-fallback-receipt" {
					tx.LocalDone = true
				} else {
					tx.Items[0].Stage = "done"
				}
				_ = a.saveHandover(&tx)
			case "fallback-intent-mismatch":
				backupCorrect(t, a.marketFallbackStock(), "char-b", "SEASON", "C", localprogress.StockAmounts{Unknown: 3})
			case "fallback-intent-missing-stock":
				opts := ItemOptions{CharacterUID: "char-b", Mode: "season"}
				path, _ := a.marketFallbackSyncPath(opts)
				data, _ := os.ReadFile(path)
				var pending marketFallbackSyncIntent
				_ = json.Unmarshal(data, &pending)
				pending.ItemID = "MISSING"
				_ = os.Remove(path)
				_ = a.saveMarketFallbackSync(opts, &pending)
			case "wrong-profile-filename":
				entries, _ := os.ReadDir(filepath.Join(a.dir, "stock-usage"))
				_ = os.Rename(filepath.Join(a.dir, "stock-usage", entries[0].Name()), filepath.Join(a.dir, "stock-usage", strings.Repeat("f", 64)+".json"))
			case "duplicate-source-key":
				path, _, _ := a.stockUsagePath("char-a", "PVP")
				data, _ := os.ReadFile(path)
				data = bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 1,"version":1`), 1)
				_ = os.WriteFile(path, data, 0600)
			case "unknown-file":
				_ = os.WriteFile(filepath.Join(a.dir, "local-progress", "random.json"), []byte("{}"), 0600)
			}
			if _, _, err := collectLocalProgressBackup(a.dir); err == nil {
				t.Fatal("cross-ledger corruption accepted")
			}
		})
	}
}
func TestLocalBackupVerifiedUploadClearWindowAndLegacyDoneArePortable(t *testing.T) {
	a := backupFixture(t)
	backupAddPendingFixture(t, a)
	backupCorrect(t, a.marketFallbackStock(), "char-b", "SEASON", "C", localprogress.StockAmounts{})
	if _, _, err := collectLocalProgressBackup(a.dir); err != nil {
		t.Fatal("valid fallback clear-before-quality window rejected", err)
	}
	path := a.handoverPath("char-a", "PVP")
	data, _ := os.ReadFile(path)
	var tx handoverTransaction
	_ = json.Unmarshal(data, &tx)
	if _, err := a.marketFallbackStock().RecordHandover("char-a", "PVP", tx.ID, []localprogress.HandoverEntry{{ItemID: "A", Regular: 1}}); err != nil {
		t.Fatal(err)
	}
	tx.LocalDone = true
	tx.Draft[0].Quantity = 3
	tx.Items[0].Market = localprogress.HandoverEntry{ItemID: "A", Unknown: 1}
	tx.Items[0].RegularToMarket = 1
	tx.Items[0].Target = 1
	tx.Items[0].Stage = "done"
	tx.Items[0].Revision = "revision-after"
	if a.saveHandover(&tx) != nil {
		t.Fatal("legacy fixture")
	}
	if _, _, err := collectLocalProgressBackup(a.dir); err != nil {
		t.Fatal("safe legacy done missing Market receipt rejected", err)
	}
}

func TestLocalBackupRejectsMissingJournalFieldsAndCapacityLimits(t *testing.T) {
	if _, err := validateLocalBackupFiles(make([]localBackupFile, maxLocalBackupFiles+1)); err == nil {
		t.Fatal("file count limit missing")
	}
	a := backupFixture(t)
	backupAddPendingFixture(t, a)
	backup, _, err := collectLocalProgressBackup(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"localDone", "target", "regular"} {
		copy := backup
		copy.Files = append([]localBackupFile{}, backup.Files...)
		for i, file := range copy.Files {
			if !strings.HasPrefix(file.Path, "handovers/") {
				continue
			}
			var object map[string]json.RawMessage
			_ = json.Unmarshal(file.Data, &object)
			if field == "localDone" {
				delete(object, field)
			} else {
				var rows []map[string]json.RawMessage
				_ = json.Unmarshal(object["items"], &rows)
				if field == "target" {
					delete(rows[0], field)
				} else {
					var entry map[string]json.RawMessage
					_ = json.Unmarshal(rows[0]["local"], &entry)
					delete(entry, field)
					rows[0]["local"], _ = json.Marshal(entry)
				}
				object["items"], _ = json.Marshal(rows)
			}
			copy.Files[i].Data, _ = json.Marshal(object)
			copy.Files[i].SHA256 = localBackupHash(copy.Files[i].Data)
		}
		data, _ := encodeLocalProgressBackup(copy)
		if _, _, err := decodeLocalProgressBackup(data); err == nil {
			t.Fatal("missing mandatory journal field accepted", field)
		}
	}
}
func TestLocalBackupSymlinkAndManagedDestinationAreRejected(t *testing.T) {
	a := backupFixture(t)
	if localBackupDestination(a.dir, filepath.Join(a.dir, "local-progress", "file"+localBackupExtension)) == nil {
		t.Fatal("managed output allowed")
	}
	outside := t.TempDir()
	linked := filepath.Join(a.dir, "stock-edits")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skip("symlink unavailable")
	}
	if _, _, err := collectLocalProgressBackup(a.dir); err == nil {
		t.Fatal("symlink followed")
	}
}

func TestLocalBackupV2UnsentPartialOwnCommitAndDisjointEditArePortable(t *testing.T) {
	a := backupFixture(t)
	backupAddPendingFixture(t, a)
	path := a.handoverPath("char-a", "PVP")
	tx, err := readHandover(path, "char-a", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	tx.Version = 2
	tx.Draft[0].Quantity = 3
	tx.Items[0].Market = localprogress.HandoverEntry{ItemID: "A", Unknown: 1}
	tx.Items[0].RegularToMarket = 1
	tx.Items[0].MarketRemaining = localprogress.HandoverEntry{ItemID: "A", Unknown: 1}
	tx.Items[0].Target, tx.Items[0].Revision, tx.Items[0].Stage = 1, "saved-baseline", "unsent"
	if err = a.saveHandover(tx); err != nil {
		t.Fatal(err)
	}
	baseline := localprogress.StockAmounts{Unknown: 2}
	if err = a.saveStockEditorPending(&stockEditorPending{Version: 2, CharUID: "char-a", Mode: "PVP", ItemID: "unrelated-item", Amounts: localprogress.StockAmounts{Regular: 3}, Revision: "saved-baseline", Stage: "unsent", Baseline: &baseline}); err != nil {
		t.Fatal(err)
	}
	assertPortable := func() {
		t.Helper()
		before := backupFilesOnDisk(t, a.dir)
		backup, summary, err := collectLocalProgressBackup(a.dir)
		if err != nil || summary.Pending != 4 {
			t.Fatalf("valid partial local commit or independent edit rejected: %+v %v", summary, err)
		}
		data, err := encodeLocalProgressBackup(backup)
		if err != nil {
			t.Fatal(err)
		}
		decoded, _, err := decodeLocalProgressBackup(data)
		if err != nil {
			t.Fatal(err)
		}
		files := make([]localbackup.File, 0, len(decoded.Files))
		for _, file := range decoded.Files {
			files = append(files, localbackup.File{Path: file.Path, Data: file.Data})
		}
		other := t.TempDir()
		if err = localbackup.Restore(other, files); err != nil {
			t.Fatal(err)
		}
		after := backupFilesOnDisk(t, other)
		if len(after) != len(before) {
			t.Fatal("pending or ledger omitted on restore")
		}
		for name, data := range before {
			if after[name] != data {
				t.Fatal("restore changed a pending phase or receipt", name)
			}
		}
	}
	// A dev receipt has committed while fallback/LocalDone have not yet committed.
	assertPortable()
	if _, err = a.marketFallbackStock().RecordHandover("char-a", "PVP", tx.ID, []localprogress.HandoverEntry{tx.Items[0].Fallback}); err != nil {
		t.Fatal(err)
	}
	tx.LocalDone, tx.Items[0].Stage = true, "sent"
	if err = a.saveHandover(tx); err != nil {
		t.Fatal(err)
	}
	assertPortable()
	// A sent phase with LocalDone=false is impossible: no remote write may precede own commits.
	tx.LocalDone = false
	if err = a.saveHandover(tx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = collectLocalProgressBackup(a.dir); err == nil {
		t.Fatal("remote sent before committed own phase accepted")
	}
}

func TestLocalBackupV2FallbackCapturedSubsetAndTransferReceiptNewAdds(t *testing.T) {
	for _, scenario := range []string{"captured-subset", "transfer-complete-with-new-adds", "insufficient-captured", "different-transfer-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			a := backupFixture(t)
			opts := ItemOptions{CharacterUID: "char-b", Mode: "season"}
			operationID := strings.Repeat("d", 32)
			captured := localprogress.StockAmounts{Unknown: 2}
			backupCorrect(t, a.marketFallbackStock(), opts.CharacterUID, "SEASON", "C", localprogress.StockAmounts{Regular: 1, Unknown: 3})
			switch scenario {
			case "transfer-complete-with-new-adds":
				if _, err := a.marketFallbackStock().TransferAvailable(opts.CharacterUID, "SEASON", operationID, "C", captured); err != nil {
					t.Fatal(err)
				}
				backupCorrect(t, a.marketFallbackStock(), opts.CharacterUID, "SEASON", "C", localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1})
			case "insufficient-captured":
				backupCorrect(t, a.marketFallbackStock(), opts.CharacterUID, "SEASON", "C", localprogress.StockAmounts{Unknown: 1})
			case "different-transfer-receipt":
				if _, err := a.marketFallbackStock().TransferAvailable(opts.CharacterUID, "SEASON", operationID, "C", localprogress.StockAmounts{Unknown: 1}); err != nil {
					t.Fatal(err)
				}
			}
			intent := &marketFallbackSyncIntent{Version: 2, OperationID: operationID, CharUID: opts.CharacterUID, Mode: "SEASON", ItemID: "C", Revision: "captured-baseline", Before: 1, Target: 3, Fallback: captured, MarketAfter: localprogress.StockAmounts{Unknown: 3}}
			if err := a.saveMarketFallbackSync(opts, intent); err != nil {
				t.Fatal(err)
			}
			before := backupFilesOnDisk(t, a.dir)
			backup, _, err := collectLocalProgressBackup(a.dir)
			valid := scenario == "captured-subset" || scenario == "transfer-complete-with-new-adds"
			if (err == nil) != valid {
				t.Fatalf("captured/transfer cross-state validity differs: %v", err)
			}
			if valid {
				data, err := encodeLocalProgressBackup(backup)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = decodeLocalProgressBackup(data); err != nil {
					t.Fatal(err)
				}
			}
			after := backupFilesOnDisk(t, a.dir)
			for name, data := range before {
				if after[name] != data {
					t.Fatal("validation mutated stock or pending capture", name)
				}
			}
		})
	}
}

func backupOrphansForTest(t *testing.T, root string) map[string][]byte {
	t.Helper()
	orphans := make(map[string][]byte)
	for dir, pattern := range map[string]string{"local-progress": ".local-progress-*.tmp", "market-fallback": ".local-progress-*.tmp", "handovers": ".handover-*.tmp", "stock-edits": ".stock-edit-*.tmp", "market-fallback-sync": ".fallback-sync-*.tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.CreateTemp(filepath.Join(root, dir), pattern)
		if err != nil {
			t.Fatal(err)
		}
		data := []byte(`{"interrupted":`)
		if _, err = file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		orphans[dir+"/"+filepath.Base(file.Name())] = data
	}
	name := "stock-usage/" + strings.Repeat("a", 64) + ".json.tmp"
	data := []byte{}
	if err := os.MkdirAll(filepath.Join(root, "stock-usage"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), data, 0600); err != nil {
		t.Fatal(err)
	}
	orphans[name] = data
	return orphans
}

func TestLocalBackupGeneratedCrashOrphansAreSkippedWithoutDeletingAndImportKeepsOld(t *testing.T) {
	source := backupFixture(t)
	backupAddPendingFixture(t, source)
	before, summary, err := collectLocalProgressBackup(source.dir)
	if err != nil {
		t.Fatal(err)
	}
	orphans := backupOrphansForTest(t, source.dir)
	after, actualSummary, err := collectLocalProgressBackup(source.dir)
	if err != nil || actualSummary != summary {
		t.Fatal("own interrupted temporary file blocked a valid backup", err)
	}
	beforeData, _ := encodeLocalProgressBackup(before)
	afterData, _ := encodeLocalProgressBackup(after)
	if !bytes.Equal(beforeData, afterData) {
		t.Fatal("orphan payload leaked into the archive or changed current stock")
	}
	for name, data := range orphans {
		current, err := os.ReadFile(filepath.Join(source.dir, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(current, data) {
			t.Fatal("export deleted or modified a crash artifact", name)
		}
	}
	path := filepath.Join(t.TempDir(), "save"+localBackupExtension)
	if err := os.WriteFile(path, afterData, 0600); err != nil {
		t.Fatal(err)
	}
	target := backupFixture(t)
	targetOrphans := backupOrphansForTest(t, target.dir)
	injectBackupDialogs(t, localBackupDialogs{open: func(context.Context, wailsRuntime.OpenDialogOptions) (string, error) { return path, nil }, confirm: func(context.Context, wailsRuntime.MessageDialogOptions) (string, error) { return "Yes", nil }})
	state := target.ImportLocalProgress()
	if state.LocalBackup == nil || state.LocalBackup.Busy || state.LocalBackup.Error != "" || state.LocalBackup.RecoveryRequired {
		t.Fatalf("valid current saves plus orphan could not be replaced: %+v", state.LocalBackup)
	}
	oldDirs, err := os.ReadDir(filepath.Join(target.dir, ".local-progress-backups"))
	if err != nil || len(oldDirs) != 1 {
		t.Fatal("previous saves were not archived", err)
	}
	oldRoot := filepath.Join(target.dir, ".local-progress-backups", oldDirs[0].Name())
	for name, data := range targetOrphans {
		current, err := os.ReadFile(filepath.Join(oldRoot, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(current, data) {
			t.Fatal("import deleted the previous orphan rather than preserving the old directory", name)
		}
	}
	imported, importedSummary, err := collectLocalProgressBackup(target.dir)
	importedData, _ := encodeLocalProgressBackup(imported)
	if err != nil || importedSummary != summary || !bytes.Equal(importedData, afterData) {
		t.Fatal("import changed a canonical receipt, stock, or pending phase", err)
	}
}

func TestLocalBackupUnexpectedOrUnsafeTemporaryLookalikesRemainFailClosed(t *testing.T) {
	for _, name := range []string{".local-progress-user.tmp", ".local-progress-01.tmp", ".local-progress-4294967296.tmp", ".handover-1.tmp", ".local-progress-1.tmp.json", strings.Repeat("A", 64) + ".json.tmp"} {
		t.Run(name, func(t *testing.T) {
			a := backupFixture(t)
			path := filepath.Join(a.dir, "local-progress", name)
			data := []byte("unexpected user data")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := collectLocalProgressBackup(a.dir); err == nil {
				t.Fatal("unexpected temporary lookalike was silently omitted")
			}
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(current, data) {
				t.Fatal("rejected input was modified")
			}
		})
	}
	for _, kind := range []string{"directory", "oversized", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			a := backupFixture(t)
			if err := os.Mkdir(filepath.Join(a.dir, "stock-edits"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(a.dir, "stock-edits", ".stock-edit-1.tmp")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, make([]byte, (8<<10)+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip("symlink unavailable")
				}
			}
			if _, _, err := collectLocalProgressBackup(a.dir); err == nil {
				t.Fatal("unsafe orphan path or oversized data accepted")
			}
		})
	}
}

func TestLocalBackupMultipleItemSyncIntentsAndLegacyFilenameCoexistSafely(t *testing.T) {
	for _, legacySecond := range []bool{false, true} {
		t.Run(map[bool]string{false: "per-item", true: "legacy-plus-per-item"}[legacySecond], func(t *testing.T) {
			a := backupFixture(t)
			opts := ItemOptions{CharacterUID: "char-b", Mode: "season"}
			for _, id := range []string{"C", "D"} {
				backupCorrect(t, a.marketFallbackStock(), opts.CharacterUID, "SEASON", id, localprogress.StockAmounts{Unknown: 2})
				intent := &marketFallbackSyncIntent{Version: 2, OperationID: localBackupHash([]byte(id))[:32], CharUID: opts.CharacterUID, Mode: "SEASON", ItemID: id, Revision: "baseline", Before: 1, Target: 3, Fallback: localprogress.StockAmounts{Unknown: 2}, MarketAfter: localprogress.StockAmounts{Unknown: 3}}
				if id == "D" && legacySecond {
					intent.Version, intent.OperationID = 1, ""
					path, err := a.marketFallbackSyncLegacyPath(opts)
					if err != nil {
						t.Fatal(err)
					}
					data, _ := json.Marshal(intent)
					if err = os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := a.saveMarketFallbackSync(opts, intent); err != nil {
					t.Fatal(err)
				}
			}
			before := backupFilesOnDisk(t, a.dir)
			backup, summary, err := collectLocalProgressBackup(a.dir)
			if err != nil || summary.Pending != 2 {
				t.Fatal("independent per-item upload intent was omitted/rejected", summary, err)
			}
			data, _ := encodeLocalProgressBackup(backup)
			decoded, _, err := decodeLocalProgressBackup(data)
			if err != nil {
				t.Fatal(err)
			}
			files := make([]localbackup.File, 0, len(decoded.Files))
			for _, file := range decoded.Files {
				files = append(files, localbackup.File{Path: file.Path, Data: file.Data})
			}
			other := t.TempDir()
			if err = localbackup.Restore(other, files); err != nil {
				t.Fatal(err)
			}
			after := backupFilesOnDisk(t, other)
			if len(before) != len(after) {
				t.Fatal("multi-intent archive omitted a file")
			}
			for name, raw := range before {
				if after[name] != raw {
					t.Fatal("multi-intent restoration changed a payload", name)
				}
			}
			// An editor conflict with the second intent must not slip through a
			// profile-level first-intent lookup.
			if err = a.saveStockEditorPending(&stockEditorPending{Version: 1, CharUID: opts.CharacterUID, Mode: "SEASON", ItemID: "D", Amounts: localprogress.StockAmounts{Regular: 1}, Revision: "baseline"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err = collectLocalProgressBackup(a.dir); err == nil {
				t.Fatal("editor overlap with a non-first pending item accepted")
			}
		})
	}
}

func TestLocalBackupRejectsDuplicateItemIntentAndPayloadFilenameMismatch(t *testing.T) {
	for _, scenario := range []string{"duplicate-legacy-item", "wrong-item-filename"} {
		t.Run(scenario, func(t *testing.T) {
			a := backupFixture(t)
			opts := ItemOptions{CharacterUID: "char-b", Mode: "season"}
			backupCorrect(t, a.marketFallbackStock(), opts.CharacterUID, "SEASON", "C", localprogress.StockAmounts{Unknown: 2})
			intent := &marketFallbackSyncIntent{Version: 2, OperationID: strings.Repeat("e", 32), CharUID: opts.CharacterUID, Mode: "SEASON", ItemID: "C", Revision: "baseline", Before: 1, Target: 3, Fallback: localprogress.StockAmounts{Unknown: 2}, MarketAfter: localprogress.StockAmounts{Unknown: 3}}
			if err := a.saveMarketFallbackSync(opts, intent); err != nil {
				t.Fatal(err)
			}
			modern, err := a.marketFallbackSyncPath(opts, "C")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "duplicate-legacy-item" {
				legacy, _ := a.marketFallbackSyncLegacyPath(opts)
				data, _ := json.Marshal(intent)
				if err = os.WriteFile(legacy, data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				wrong, _ := a.marketFallbackSyncPath(opts, "D")
				if err = os.Rename(modern, wrong); err != nil {
					t.Fatal(err)
				}
			}
			before := backupFilesOnDisk(t, a.dir)
			if _, _, err = collectLocalProgressBackup(a.dir); err == nil {
				t.Fatal("ambiguous or misbound per-item intent accepted")
			}
			after := backupFilesOnDisk(t, a.dir)
			for name, raw := range before {
				if after[name] != raw {
					t.Fatal("failed validation altered intent evidence")
				}
			}
		})
	}
}
