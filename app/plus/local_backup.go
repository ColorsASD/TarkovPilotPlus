package plus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localbackup"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const localBackupExtension = ".tpp-local-backup.json"
const maxLocalBackupBytes = 128 << 20
const maxLocalBackupData = 64 << 20
const maxLocalBackupFiles = 2048

var errLocalBackupInvalid = errors.New("The backup is invalid, unsupported, or too large. Existing local saves were kept.")

const localBackupRecoveryMessage = "Local save recovery is incomplete. Restart the app before changing stock."

type ItemLocalBackupState struct {
	Busy             bool   `json:"busy"`
	Status           string `json:"status"`
	Error            string `json:"error"`
	RecoveryRequired bool   `json:"recoveryRequired"`
}
type localBackupFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data"`
}
type localProgressBackup struct {
	Format   string            `json:"format"`
	Version  int               `json:"version"`
	Files    []localBackupFile `json:"files"`
	Checksum string            `json:"checksum"`
}
type localBackupSummary struct {
	Profiles, Items, Pending int
	Available, Used          int64
}
type localBackupDialogs struct {
	save    func(context.Context, wailsRuntime.SaveDialogOptions) (string, error)
	open    func(context.Context, wailsRuntime.OpenDialogOptions) (string, error)
	confirm func(context.Context, wailsRuntime.MessageDialogOptions) (string, error)
}

var itemLocalBackupDialogs = localBackupDialogs{wailsRuntime.SaveFileDialog, wailsRuntime.OpenFileDialog, wailsRuntime.MessageDialog}
var itemLocalBackupRestore = localbackup.Restore

func (a *ItemWindowApp) localBackupBusyLocked() bool {
	return a.state.LocalBackup != nil && (a.state.LocalBackup.Busy || a.state.LocalBackup.RecoveryRequired)
}
func recoverLocalProgressImport(dir string) error { return localbackup.Recover(dir) }

func (a *ItemWindowApp) beginLocalBackup(status string) bool {
	if !a.trackingMu.TryLock() {
		a.localBackupFeedback("", progressBusyMessage)
		return false
	}
	if !a.scanMu.TryLock() {
		a.trackingMu.Unlock()
		a.localBackupFeedback("", progressBusyMessage)
		return false
	}
	a.mu.Lock()
	blocked := a.localBackupBusyLocked() || a.state.Busy || a.stockEditorBusyLocked() || a.state.Handover != nil && a.state.Handover.Busy || a.dir == ""
	recoveryRequired := a.state.LocalBackup != nil && a.state.LocalBackup.RecoveryRequired
	if !blocked {
		a.state.LocalBackup = &ItemLocalBackupState{Busy: true, Status: status}
	}
	a.mu.Unlock()
	if blocked {
		a.scanMu.Unlock()
		a.trackingMu.Unlock()
		if recoveryRequired {
			a.localBackupFeedback("", localBackupRecoveryMessage)
		} else {
			a.localBackupFeedback("", progressBusyMessage)
		}
		return false
	}
	a.configureTrackedAddHotkey()
	a.emit()
	return true
}
func (a *ItemWindowApp) endLocalBackup() {
	a.mu.Lock()
	if a.state.LocalBackup != nil {
		a.state.LocalBackup.Busy = false
	}
	a.mu.Unlock()
	a.scanMu.Unlock()
	a.trackingMu.Unlock()
	a.configureTrackedAddHotkey()
	a.emit()
}
func (a *ItemWindowApp) localBackupFeedback(status, message string) {
	a.mu.Lock()
	if a.state.LocalBackup == nil {
		a.state.LocalBackup = &ItemLocalBackupState{}
	}
	a.state.LocalBackup.Status = status
	a.state.LocalBackup.Error = message
	a.mu.Unlock()
	a.emit()
}

// Ha a rollback maga is félbemarad, a jelenlegi Store-handle-ekkel nem
// írhatunk tovább a részben visszaállított mappákba. A startup recovery
// ellenőrzött befejezéséig csak ez a futás marad írásra letiltva.
func (a *ItemWindowApp) localBackupRecoveryFailure() {
	a.mu.Lock()
	if a.state.LocalBackup == nil {
		a.state.LocalBackup = &ItemLocalBackupState{}
	}
	a.state.LocalBackup.RecoveryRequired = true
	a.state.LocalBackup.Status = ""
	a.state.LocalBackup.Error = localBackupRecoveryMessage
	a.mu.Unlock()
	a.emit()
}

// ExportLocalProgress kizárólag a saját készleteket és műveletnaplókat menti.
func (a *ItemWindowApp) ExportLocalProgress() (result ItemState) {
	if !a.beginLocalBackup("Checking local saves…") {
		return a.GetState()
	}
	defer func() { a.endLocalBackup(); result = a.GetState() }()
	if err := recoverLocalProgressImport(a.dir); err != nil {
		a.localBackupRecoveryFailure()
		return a.GetState()
	}
	backup, summary, err := collectLocalProgressBackup(a.dir)
	if err != nil {
		a.localBackupFeedback("", err.Error())
		return a.GetState()
	}
	ctx := a.wailsCtx
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := itemLocalBackupDialogs.save(ctx, wailsRuntime.SaveDialogOptions{Title: "Export local Items Tracker save", DefaultFilename: "TarkovPilotPlus-" + time.Now().Format("2006-01-02") + localBackupExtension, Filters: []wailsRuntime.FileFilter{{DisplayName: "TarkovPilotPlus local backup", Pattern: "*" + localBackupExtension}}})
	if err != nil {
		a.localBackupFeedback("", "The backup file could not be selected.")
		return a.GetState()
	}
	if path == "" {
		a.localBackupFeedback("Export cancelled.", "")
		return a.GetState()
	}
	if !strings.HasSuffix(strings.ToLower(path), localBackupExtension) {
		path += localBackupExtension
	}
	data, err := encodeLocalProgressBackup(backup)
	if err != nil || localBackupDestination(a.dir, path) != nil || writeLocalBackupFile(path, data) != nil {
		a.localBackupFeedback("", "The backup could not be saved. Existing local saves were kept.")
		return a.GetState()
	}
	a.localBackupFeedback(fmt.Sprintf("Local save exported: %d profiles, %d items, %d available, %d used, %d pending operations.", summary.Profiles, summary.Items, summary.Available, summary.Used, summary.Pending), "")
	return a.GetState()
}

// ImportLocalProgress restorekor sem GET-et, sem PATCH-et nem indít.
func (a *ItemWindowApp) ImportLocalProgress() (result ItemState) {
	if !a.progressNetworkMu.TryLock() {
		a.localBackupFeedback("", progressBusyMessage)
		return a.GetState()
	}
	defer a.progressNetworkMu.Unlock()
	if !a.beginLocalBackup("Checking backup…") {
		return a.GetState()
	}
	defer func() { a.endLocalBackup(); result = a.GetState() }()
	if err := recoverLocalProgressImport(a.dir); err != nil {
		a.localBackupRecoveryFailure()
		return a.GetState()
	}
	ctx := a.wailsCtx
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := itemLocalBackupDialogs.open(ctx, wailsRuntime.OpenDialogOptions{Title: "Import local Items Tracker save", Filters: []wailsRuntime.FileFilter{{DisplayName: "TarkovPilotPlus local backup", Pattern: "*" + localBackupExtension}}})
	if err != nil {
		a.localBackupFeedback("", "The backup file could not be selected.")
		return a.GetState()
	}
	if path == "" {
		a.localBackupFeedback("Import cancelled.", "")
		return a.GetState()
	}
	data, err := readLocalBackupFile(path, maxLocalBackupBytes)
	if err != nil {
		a.localBackupFeedback("", errLocalBackupInvalid.Error())
		return a.GetState()
	}
	backup, summary, err := decodeLocalProgressBackup(data)
	if err != nil {
		a.localBackupFeedback("", errLocalBackupInvalid.Error())
		return a.GetState()
	}
	_, oldSummary, err := collectLocalProgressBackup(a.dir)
	if err != nil {
		a.localBackupFeedback("", "Current local saves could not be validated. Repair the saved files before replacing them.")
		return a.GetState()
	}
	message := fmt.Sprintf("Import: %d profiles, %d items, %d available, %d used, %d pending operations.\nCurrent: %d profiles, %d items, %d available, %d used, %d pending operations.\n\nThis REPLACES all local Items Tracker saves for every character and game mode. The current files will be kept in an automatic local backup. API keys and settings are kept. Pending operations require verification or an explicit resume; import sends no Market requests.\n\nImport this backup?", summary.Profiles, summary.Items, summary.Available, summary.Used, summary.Pending, oldSummary.Profiles, oldSummary.Items, oldSummary.Available, oldSummary.Used, oldSummary.Pending)
	answer, err := itemLocalBackupDialogs.confirm(ctx, wailsRuntime.MessageDialogOptions{Type: wailsRuntime.QuestionDialog, Title: "Replace local Items Tracker saves?", Message: message, Buttons: []string{"Yes", "No"}, DefaultButton: "No", CancelButton: "No"})
	if err != nil || answer != "Yes" {
		a.localBackupFeedback("Import cancelled.", "")
		return a.GetState()
	}
	files := make([]localbackup.File, 0, len(backup.Files))
	for _, file := range backup.Files {
		files = append(files, localbackup.File{Path: file.Path, Data: file.Data})
	}
	if err := itemLocalBackupRestore(a.dir, files); err != nil {
		if _, statErr := os.Lstat(filepath.Join(a.dir, ".local-progress-import")); !errors.Is(statErr, os.ErrNotExist) {
			a.localBackupRecoveryFailure()
		} else {
			a.localBackupFeedback("", localbackup.ErrRestore.Error())
		}
		return a.GetState()
	}
	a.mu.Lock()
	a.resetAfterLocalProgressImportLocked()
	a.mu.Unlock()
	a.localBackupFeedback(fmt.Sprintf("Local save imported: %d profiles, %d items, %d available, %d used, %d pending operations. Previous saves: .local-progress-backups. Refresh progress to load live requirements.", summary.Profiles, summary.Items, summary.Available, summary.Used, summary.Pending), "")
	return a.GetState()
}

func localBackupDestination(root, path string) error {
	if localbackup.CheckPath(path) != nil {
		return errLocalBackupInvalid
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, dir := range append(append([]string{}, localbackup.Directories...), ".local-progress-import", ".local-progress-backups") {
		base, _ := filepath.Abs(filepath.Join(root, dir))
		rel, err := filepath.Rel(base, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return errLocalBackupInvalid
		}
	}
	return nil
}
func writeLocalBackupFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".tpp-backup-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func readLocalBackupFile(path string, limit int64) ([]byte, error) {
	if localbackup.CheckPath(path) != nil {
		return nil, errLocalBackupInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errLocalBackupInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errLocalBackupInvalid
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errLocalBackupInvalid
	}
	return data, nil
}
func localBackupHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func localBackupChecksum(backup localProgressBackup) string {
	backup.Checksum = ""
	data, _ := json.Marshal(backup)
	return localBackupHash(data)
}
func encodeLocalProgressBackup(backup localProgressBackup) ([]byte, error) {
	backup.Checksum = localBackupChecksum(backup)
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil || len(data) > maxLocalBackupBytes {
		return nil, errLocalBackupInvalid
	}
	return data, nil
}
func decodeLocalProgressBackup(data []byte) (localProgressBackup, localBackupSummary, error) {
	var backup localProgressBackup
	if len(data) > maxLocalBackupBytes || !localBackupUniqueJSON(data) || !localBackupJSONShape(data, reflect.TypeOf(backup), 0) {
		return backup, localBackupSummary{}, errLocalBackupInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&backup) != nil || d.Decode(new(any)) != io.EOF || backup.Format != "TarkovPilotPlus local save" || backup.Version != 1 || backup.Files == nil || backup.Checksum != localBackupChecksum(backup) {
		return backup, localBackupSummary{}, errLocalBackupInvalid
	}
	summary, err := validateLocalBackupFiles(backup.Files)
	return backup, summary, err
}
func collectLocalProgressBackup(root string) (localProgressBackup, localBackupSummary, error) {
	backup := localProgressBackup{Format: "TarkovPilotPlus local save", Version: 1, Files: []localBackupFile{}}
	var total int64
	for _, dir := range localbackup.Directories {
		path := filepath.Join(root, dir)
		if localbackup.CheckPath(path) != nil {
			return backup, localBackupSummary{}, errLocalBackupInvalid
		}
		folder, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return backup, localBackupSummary{}, errLocalBackupInvalid
		}
		entries, err := folder.ReadDir(maxLocalBackupFiles - len(backup.Files) + 1)
		closeErr := folder.Close()
		if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > maxLocalBackupFiles-len(backup.Files) {
			return backup, localBackupSummary{}, errLocalBackupInvalid
		}
		for _, entry := range entries {
			if len(backup.Files) >= maxLocalBackupFiles {
				return backup, localBackupSummary{}, errLocalBackupInvalid
			}
			rel := dir + "/" + entry.Name()
			if limit, temporary := localbackup.GeneratedTemporaryLimit(dir, entry.Name()); temporary {
				path := filepath.Join(path, entry.Name())
				info, err := os.Lstat(path)
				if localbackup.CheckPath(path) != nil || err != nil || !info.Mode().IsRegular() || info.Size() > limit {
					return backup, localBackupSummary{}, errLocalBackupInvalid
				}
				continue
			}
			if !localbackup.ValidPath(rel) || entry.IsDir() {
				return backup, localBackupSummary{}, errLocalBackupInvalid
			}
			data, err := readLocalBackupFile(filepath.Join(path, entry.Name()), 8<<20)
			if err != nil {
				return backup, localBackupSummary{}, err
			}
			if total > maxLocalBackupData-int64(len(data)) {
				return backup, localBackupSummary{}, errLocalBackupInvalid
			}
			total += int64(len(data))
			backup.Files = append(backup.Files, localBackupFile{Path: rel, SHA256: localBackupHash(data), Data: data})
		}
	}
	sort.Slice(backup.Files, func(i, j int) bool { return backup.Files[i].Path < backup.Files[j].Path })
	summary, err := validateLocalBackupFiles(backup.Files)
	return backup, summary, err
}

func validateLocalBackupFiles(files []localBackupFile) (localBackupSummary, error) {
	var summary localBackupSummary
	if len(files) > maxLocalBackupFiles {
		return summary, errLocalBackupInvalid
	}
	stage, err := os.MkdirTemp("", ".tpp-backup-check-")
	if err != nil {
		return summary, errLocalBackupInvalid
	}
	defer os.RemoveAll(stage)
	profiles, items := map[string]bool{}, map[string]bool{}
	total := int64(0)
	stocks := map[string]localprogress.Snapshot{}
	ledgers := map[string]map[string][]localprogress.HandoverEntry{}
	transfers := map[string]map[string]localprogress.HandoverEntry{}
	transactions := map[string]*handoverTransaction{}
	usages := map[string]stockUsageHistory{}
	stockEdits := map[string]*stockEditorPending{}
	syncIntents := map[string]map[string]*marketFallbackSyncIntent{}
	for index, file := range files {
		if !localbackup.ValidPath(file.Path) || index > 0 && files[index-1].Path >= file.Path || len(file.Data) > 8<<20 || file.SHA256 != localBackupHash(file.Data) || total > maxLocalBackupData-int64(len(file.Data)) {
			return summary, errLocalBackupInvalid
		}
		total += int64(len(file.Data))
		parts := strings.Split(file.Path, "/")
		dir := parts[0]
		var header struct {
			CharUID  string `json:"charUid"`
			GameMode string `json:"gameMode"`
			Mode     string `json:"mode"`
			ItemID   string `json:"itemId"`
		}
		if !localBackupUniqueJSON(file.Data) || json.Unmarshal(file.Data, &header) != nil {
			return summary, errLocalBackupInvalid
		}
		mode := header.GameMode
		if dir == "handovers" || dir == "stock-edits" || dir == "market-fallback-sync" {
			mode = header.Mode
		}
		profileName := localBackupHash([]byte(header.CharUID + "\x00" + mode))
		filenameValid := parts[1] == profileName+".json"
		if dir == "market-fallback-sync" {
			filenameValid = filenameValid || handoverItemID(header.ItemID) && parts[1] == profileName+"-"+localBackupHash([]byte(header.ItemID))+".json"
		}
		if !handoverItemID(header.CharUID) || !stockEditorPendingMode(mode) || mode != strings.ToUpper(mode) || !filenameValid {
			return summary, errLocalBackupInvalid
		}
		profile := header.CharUID + "\x00" + mode
		profiles[profile] = true
		path := filepath.Join(stage, filepath.FromSlash(file.Path))
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, file.Data, 0600) != nil {
			return summary, errLocalBackupInvalid
		}
		if dir == "local-progress" || dir == "market-fallback" {
			stock, err := localprogress.ValidateBackupStock(file.Data)
			if err != nil {
				return summary, errLocalBackupInvalid
			}
			if dir == "market-fallback" {
				var fields struct {
					MarketKinds map[string]json.RawMessage `json:"marketKinds"`
				}
				_ = json.Unmarshal(file.Data, &fields)
				if len(fields.MarketKinds) != 0 {
					return summary, errLocalBackupInvalid
				}
				for _, row := range stock.Items {
					if row.MarketRegularQuantity != 0 || row.MarketFIRQuantity != 0 {
						return summary, errLocalBackupInvalid
					}
				}
			}
			stocks[dir+"/"+profile] = stock
			var ledger struct {
				HandoverReceipts map[string][]localprogress.HandoverEntry `json:"handoverReceipts"`
				TransferReceipts map[string]localprogress.HandoverEntry   `json:"transferReceipts"`
			}
			_ = json.Unmarshal(file.Data, &ledger)
			ledgers[dir+"/"+profile] = ledger.HandoverReceipts
			transfers[dir+"/"+profile] = ledger.TransferReceipts
			for _, row := range stock.Items {
				items[profile+"\x00"+row.ItemID] = true
				if addBackupSummary(&summary.Available, row.LocalAvailableQuantity) != nil || addBackupSummary(&summary.Used, row.LocalUsedQuantity) != nil {
					return summary, errLocalBackupInvalid
				}
			}
			continue
		}
		if dir == "stock-usage" {
			if !localBackupJSONShape(file.Data, reflect.TypeOf(stockUsageHistory{}), 0) {
				return summary, errLocalBackupInvalid
			}
			history, err := readStockUsage(path, header.CharUID, mode)
			if err != nil {
				return summary, errLocalBackupInvalid
			}
			usages[profile] = history
			amounts, err := aggregateStockUsage(history)
			if err != nil {
				return summary, errLocalBackupInvalid
			}
			for id, q := range amounts {
				items[profile+"\x00"+id] = true
				quantity, err := stockEditorTotal(q)
				if err != nil || addBackupSummary(&summary.Used, quantity) != nil {
					return summary, errLocalBackupInvalid
				}
			}
			continue
		}
		summary.Pending++
		opts := ItemOptions{CharacterUID: header.CharUID, Mode: strings.ToLower(mode)}
		app := &ItemWindowApp{dir: stage}
		switch dir {
		case "handovers":
			if len(file.Data) > 256<<10 || !localBackupJSONShape(file.Data, reflect.TypeOf(handoverTransaction{}), 0) {
				return summary, errLocalBackupInvalid
			}
			tx, err := readHandover(path, header.CharUID, mode)
			if err != nil {
				return summary, errLocalBackupInvalid
			}
			transactions[profile] = tx
		case "stock-edits":
			if !localBackupJSONShape(file.Data, reflect.TypeOf(stockEditorPending{}), 0) {
				return summary, errLocalBackupInvalid
			}
			pending, err := app.readStockEditorPending(opts)
			if err != nil {
				return summary, errLocalBackupInvalid
			}
			stockEdits[profile] = pending
		case "market-fallback-sync":
			if !localBackupJSONShape(file.Data, reflect.TypeOf(marketFallbackSyncIntent{}), 0) {
				return summary, errLocalBackupInvalid
			}
			intent, err := readMarketFallbackSyncFile(path, opts)
			if err != nil || intent == nil {
				return summary, errLocalBackupInvalid
			}
			if syncIntents[profile] == nil {
				syncIntents[profile] = make(map[string]*marketFallbackSyncIntent)
			}
			if syncIntents[profile][intent.ItemID] != nil {
				return summary, errLocalBackupInvalid
			}
			syncIntents[profile][intent.ItemID] = intent
		default:
			return summary, errLocalBackupInvalid
		}
	}
	for profile, tx := range transactions {
		// A kézi edit és egy hand-in továbbra is egymást kizáró művelet.
		// A sync szándék mellett csak forrásonként független hand-in engedett.
		if edit := stockEdits[profile]; edit != nil {
			for _, op := range tx.Items {
				if op.ItemID == edit.ItemID {
					return summary, errLocalBackupInvalid
				}
			}
		}
		if !tx.LocalDone {
			for _, op := range tx.Items {
				if op.Stage != "" && !(tx.Version == 2 && op.Stage == "unsent") {
					return summary, errLocalBackupInvalid
				}
			}
		}
		hasDev, hasFallback := false, false
		for _, op := range tx.Items {
			hasDev = hasDev || handoverQuantity(op.Local) > 0
			hasFallback = hasFallback || handoverQuantity(op.Fallback) > 0
		}
		_, devRecorded := ledgers["local-progress/"+profile][tx.ID]
		_, fallbackRecorded := ledgers["market-fallback/"+profile][tx.ID]
		if hasDev && hasFallback && fallbackRecorded && !devRecorded {
			return summary, errLocalBackupInvalid
		}
		for _, source := range []struct {
			dir      string
			fallback bool
		}{{"local-progress", false}, {"market-fallback", true}} {
			expected := []localprogress.HandoverEntry{}
			for _, op := range tx.Items {
				entry := op.Local
				if source.fallback {
					entry = op.Fallback
				}
				if handoverQuantity(entry) > 0 {
					expected = append(expected, entry)
				}
			}
			receipt, exists := ledgers[source.dir+"/"+profile][tx.ID]
			if exists && !sameBackupEntries(receipt, expected) || tx.LocalDone && len(expected) > 0 && !exists {
				return summary, errLocalBackupInvalid
			}
			if !exists && len(expected) > 0 {
				stock := stocks[source.dir+"/"+profile]
				for _, entry := range expected {
					row, unique := stockEditorItem(stock, entry.ItemID)
					if !unique || entry.Regular > row.LocalRegularQuantity || entry.FIR > row.LocalFIRQuantity || entry.Unknown > row.LocalAvailableQuantity-row.LocalRegularQuantity-row.LocalFIRQuantity {
						return summary, errLocalBackupInvalid
					}
				}
			}
		}
		for _, op := range tx.Items {
			receipt, exists := usages[profile].Receipts[tx.ID][op.ItemID]
			if exists && (receipt != op.Market || op.Stage != "confirmed" && op.Stage != "done") {
				return summary, errLocalBackupInvalid
			}
		}
	}
	for profile, intents := range syncIntents {
		for _, intent := range intents {
			if edit := stockEdits[profile]; edit != nil && edit.ItemID == intent.ItemID {
				return summary, errLocalBackupInvalid
			}
			if tx := transactions[profile]; tx != nil {
				for _, op := range tx.Items {
					if op.ItemID == intent.ItemID && (handoverQuantity(op.Market) > 0 || handoverQuantity(op.Fallback) > 0) {
						return summary, errLocalBackupInvalid
					}
				}
			}
			stock, exists := stocks["market-fallback/"+profile]
			if !exists {
				return summary, errLocalBackupInvalid
			}
			row, unique := stockEditorItem(stock, intent.ItemID)
			if !unique {
				return summary, errLocalBackupInvalid
			}
			current := stockEditorLocalAmounts(row)
			if intent.Version == 2 {
				receipt, exists := transfers["market-fallback/"+profile][intent.OperationID]
				expected := localprogress.HandoverEntry{ItemID: intent.ItemID, Regular: intent.Fallback.Regular, FIR: intent.Fallback.FIR, Unknown: intent.Fallback.Unknown}
				if exists && receipt != expected || !exists && (current.Regular < intent.Fallback.Regular || current.FIR < intent.Fallback.FIR || current.Unknown < intent.Fallback.Unknown) {
					return summary, errLocalBackupInvalid
				}
			} else if current != (localprogress.StockAmounts{}) && current != intent.Fallback {
				return summary, errLocalBackupInvalid
			}
		}
	}
	summary.Profiles, summary.Items = len(profiles), len(items)
	return summary, nil
}
func sameBackupEntries(left, right []localprogress.HandoverEntry) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]localprogress.HandoverEntry{}, left...), append([]localprogress.HandoverEntry{}, right...)
	sort.Slice(a, func(i, j int) bool { return a[i].ItemID < a[j].ItemID })
	sort.Slice(b, func(i, j int) bool { return b[i].ItemID < b[j].ItemID })
	return reflect.DeepEqual(a, b)
}
func addBackupSummary(total *int64, quantity int64) error {
	if quantity < 0 || *total > math.MaxInt64-quantity {
		return errLocalBackupInvalid
	}
	*total += quantity
	return nil
}

// A JSON tagok pontosak: Go case-insensitive decoder aliasait nem fogadjuk el.
func localBackupJSONShape(data []byte, t reflect.Type, depth int) bool {
	if depth > 24 || !utf8.Valid(data) || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return false
		}
		fields := map[string]reflect.Type{}
		required := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f.Type
				optional := strings.Contains(f.Tag.Get("json"), ",omitempty") || t == reflect.TypeOf(handoverOperation{}) && (name == "fallback" || name == "marketRemaining")
				if !optional {
					required[name] = true
				}
			}
		}
		for name := range required {
			if _, exists := object[name]; !exists {
				return false
			}
		}
		for key, value := range object {
			field, exists := fields[key]
			if !exists || !localBackupJSONShape(value, field, depth+1) {
				return false
			}
		}
	case reflect.Map:
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return false
		}
		for _, value := range object {
			if !localBackupJSONShape(value, t.Elem(), depth+1) {
				return false
			}
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			var value []byte
			return json.Unmarshal(data, &value) == nil && value != nil
		}
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil || values == nil {
			return false
		}
		for _, value := range values {
			if !localBackupJSONShape(value, t.Elem(), depth+1) {
				return false
			}
		}
	default:
		value := reflect.New(t)
		if json.Unmarshal(data, value.Interface()) != nil {
			return false
		}
	}
	return true
}
func localBackupUniqueJSON(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var parse func(int) bool
	parse = func(depth int) bool {
		if depth > 24 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, structured := token.(json.Delim)
		if !structured {
			return true
		}
		if delim == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !parse(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		}
		if delim == '[' {
			for d.More() {
				if !parse(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !parse(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
