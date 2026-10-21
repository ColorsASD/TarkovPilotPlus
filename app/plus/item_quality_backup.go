package plus

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const trackerQualityBackupFormat = "tarkovpilotplus-item-quality"
const maxTrackerQualityBackupBytes = 8 << 20

type trackerQualityBackupRow struct {
	RowKey  string `json:"rowKey"`
	ItemID  string `json:"itemId"`
	Name    string `json:"name"`
	Total   int64  `json:"total"`
	FIR     int64  `json:"fir"`
	Regular int64  `json:"regular"`
}

// A hordozható fájl csak az aktuális karakter besorolását tartalmazza.
// API-kulcs, profilhash, függő írás és helyreállítási napló nem kerül bele.
type trackerQualityBackup struct {
	Format     string                    `json:"format"`
	Version    int                       `json:"version"`
	CharUID    string                    `json:"charUid"`
	GameMode   string                    `json:"gameMode"`
	ExportedAt time.Time                 `json:"exportedAt"`
	Rows       []trackerQualityBackupRow `json:"rows"`
}

type TrackerQualityBackupResult struct {
	Path     string `json:"path"`
	Exported int    `json:"exported"`
	Imported int    `json:"imported"`
	Skipped  int    `json:"skipped"`
	Canceled bool   `json:"canceled"`
	Error    string `json:"error"`
}

func (a *ItemWindowApp) trackerQualityBackupContextLocked(expectedProfile string) (localprogress.Snapshot, string, error) {
	snapshot, opts := a.confirmedProgress, a.state.ItemOptions
	profile := qualityProfile(a.apiKey, opts)
	if a.apiKey == "" || !a.state.HasAPIKey || opts.CharacterUID == "" || snapshot.CharUID != opts.CharacterUID ||
		!strings.EqualFold(snapshot.GameMode, opts.Mode) || snapshot.Revision == "" || !snapshot.MetadataComplete ||
		snapshot.Items == nil || snapshot.FIRInventorySeparate || a.localRevision != opts.revision ||
		a.state.Local == nil || a.state.Local.Loading || a.state.Local.Error != "" ||
		expectedProfile != "" && expectedProfile != profile {
		return localprogress.Snapshot{}, "", errors.New("Refresh the selected character's inventory before importing or exporting classifications.")
	}
	for _, entry := range a.pendingAddQueue {
		if entry.Key == a.apiKey && entry.Opts.CharacterUID == opts.CharacterUID && strings.EqualFold(entry.Opts.Mode, opts.Mode) {
			return localprogress.Snapshot{}, "", errors.New("Wait for the selected character's pending stock saves before importing or exporting classifications.")
		}
	}
	return snapshot, profile, nil
}

func (a *ItemWindowApp) ExportTrackerQuality() TrackerQualityBackupResult {
	return a.exportTrackerQuality(func(filename string) (string, error) {
		if a.wailsCtx == nil {
			return "", errors.New("File dialogs are unavailable.")
		}
		return wailsRuntime.SaveFileDialog(a.wailsCtx, wailsRuntime.SaveDialogOptions{
			Title: "Export Items Tracker classifications", DefaultFilename: filename, CanCreateDirectories: true,
			Filters: []wailsRuntime.FileFilter{{DisplayName: "JSON backup (*.json)", Pattern: "*.json"}},
		})
	})
}

func (a *ItemWindowApp) exportTrackerQuality(selectFile func(string) (string, error)) TrackerQualityBackupResult {
	a.mu.Lock()
	snapshot, profile, err := a.trackerQualityBackupContextLocked("")
	if err == nil {
		snapshot, err = a.qualityReconcileSnapshotLocked(a.apiKey, a.state.ItemOptions, snapshot)
	}
	a.mu.Unlock()
	if err != nil {
		return TrackerQualityBackupResult{Error: err.Error()}
	}
	backup := trackerQualityBackup{Format: trackerQualityBackupFormat, Version: 1, CharUID: snapshot.CharUID,
		GameMode: snapshot.GameMode, ExportedAt: time.Now().UTC(), Rows: []trackerQualityBackupRow{}}
	keys, identities := qualitySnapshotKeys(snapshot)
	for i, row := range snapshot.Items {
		if row.MarketAvailableQuantity <= 0 || identities[keys[i]] != 1 {
			continue
		}
		backup.Rows = append(backup.Rows, trackerQualityBackupRow{RowKey: keys[i], ItemID: row.ItemID, Name: row.Name,
			Total: row.MarketAvailableQuantity, FIR: row.FIRQuantity, Regular: row.RegularQuantity})
	}
	body, err := json.MarshalIndent(backup, "", "  ")
	if err != nil || len(body) > maxTrackerQualityBackupBytes {
		return TrackerQualityBackupResult{Error: "The classification backup is too large to export."}
	}
	path, err := selectFile("TarkovPilotPlus-items-tracker-" + strings.ToLower(snapshot.GameMode) + "-" + time.Now().Format("20060102-150405") + ".json")
	if err != nil {
		return TrackerQualityBackupResult{Error: "The export file could not be selected."}
	}
	if path == "" {
		return TrackerQualityBackupResult{Canceled: true}
	}
	a.mu.Lock()
	_, _, err = a.trackerQualityBackupContextLocked(profile)
	a.mu.Unlock()
	if err != nil {
		return TrackerQualityBackupResult{Error: err.Error()}
	}
	if a.isTrackerRuntimePath(path) {
		return TrackerQualityBackupResult{Error: "Choose a separate backup file outside the app's runtime files."}
	}
	if err := writePrivateFile(path, append(body, '\n')); err != nil {
		return TrackerQualityBackupResult{Error: "The classification backup could not be saved."}
	}
	return TrackerQualityBackupResult{Path: path, Exported: len(backup.Rows)}
}

func (a *ItemWindowApp) isTrackerRuntimePath(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	for _, name := range []string{"item-quality.json", "item-quality.json.tmp", "item-settings.json", "api-key.bin"} {
		runtimePath, err := filepath.Abs(filepath.Join(a.dir, name))
		if err == nil && strings.EqualFold(absolute, runtimePath) {
			return true
		}
	}
	return false
}

func decodeTrackerQualityBackup(body []byte) (trackerQualityBackup, error) {
	var backup trackerQualityBackup
	invalid := errors.New("The selected file is not a valid Items Tracker classification backup.")
	if len(body) > maxTrackerQualityBackupBytes {
		return backup, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&backup) != nil || backup.Format != trackerQualityBackupFormat || backup.Version != 1 ||
		backup.CharUID == "" || len(backup.CharUID) > 128 || backup.ExportedAt.IsZero() || backup.Rows == nil || len(backup.Rows) > 100000 ||
		(backup.GameMode != "PVP" && backup.GameMode != "PVE" && backup.GameMode != "SEASON") {
		return backup, invalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return backup, invalid
	}
	seen := make(map[string]bool)
	for _, row := range backup.Rows {
		if len(row.RowKey) != 68 || !strings.HasPrefix(row.RowKey, "row:") || seen[row.RowKey] || row.ItemID == "" || len(row.ItemID) > 128 ||
			strings.TrimSpace(row.Name) == "" || len(row.Name) > 1024 || !validQualityRecord(qualityRecord{Total: row.Total, FIR: row.FIR, Regular: row.Regular}) {
			return backup, invalid
		}
		if _, err := hex.DecodeString(row.RowKey[4:]); err != nil {
			return backup, invalid
		}
		seen[row.RowKey] = true
	}
	return backup, nil
}

func (a *ItemWindowApp) ImportTrackerQuality() TrackerQualityBackupResult {
	return a.importTrackerQuality(func() (string, error) {
		if a.wailsCtx == nil {
			return "", errors.New("File dialogs are unavailable.")
		}
		return wailsRuntime.OpenFileDialog(a.wailsCtx, wailsRuntime.OpenDialogOptions{
			Title:   "Import Items Tracker classifications",
			Filters: []wailsRuntime.FileFilter{{DisplayName: "JSON backup (*.json)", Pattern: "*.json"}},
		})
	})
}

func (a *ItemWindowApp) importTrackerQuality(selectFile func() (string, error)) TrackerQualityBackupResult {
	a.mu.Lock()
	_, profile, err := a.trackerQualityBackupContextLocked("")
	a.mu.Unlock()
	if err != nil {
		return TrackerQualityBackupResult{Error: err.Error()}
	}
	path, err := selectFile()
	if err != nil {
		return TrackerQualityBackupResult{Error: "The import file could not be selected."}
	}
	if path == "" {
		return TrackerQualityBackupResult{Canceled: true}
	}
	file, err := os.Open(path)
	if err != nil {
		return TrackerQualityBackupResult{Error: "The selected classification backup could not be read."}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxTrackerQualityBackupBytes {
		return TrackerQualityBackupResult{Error: "The selected classification backup is not a supported JSON file."}
	}
	body, err := io.ReadAll(io.LimitReader(file, maxTrackerQualityBackupBytes+1))
	if err != nil {
		return TrackerQualityBackupResult{Error: "The selected classification backup could not be read."}
	}
	backup, err := decodeTrackerQualityBackup(body)
	if err != nil {
		return TrackerQualityBackupResult{Error: err.Error()}
	}
	return a.applyTrackerQualityBackup(profile, path, backup)
}

func (a *ItemWindowApp) applyTrackerQualityBackup(profile, path string, backup trackerQualityBackup) TrackerQualityBackupResult {
	a.mu.Lock()
	snapshot, _, err := a.trackerQualityBackupContextLocked(profile)
	if err == nil && (snapshot.CharUID != backup.CharUID || snapshot.GameMode != backup.GameMode) {
		err = errors.New("This backup belongs to another character or game mode.")
	}
	if err != nil {
		a.mu.Unlock()
		return TrackerQualityBackupResult{Error: err.Error()}
	}
	keys, identities := qualitySnapshotKeys(snapshot)
	current := make(map[string]localprogress.Item)
	for i, row := range snapshot.Items {
		if identities[keys[i]] == 1 {
			current[keys[i]] = row
		}
	}
	updates := make(map[string]qualityRecord)
	result := TrackerQualityBackupResult{Path: path}
	for _, saved := range backup.Rows {
		row, found := current[saved.RowKey]
		if !found || row.ItemID != saved.ItemID || !strings.EqualFold(strings.TrimSpace(row.Name), strings.TrimSpace(saved.Name)) {
			result.Skipped++
			continue
		}
		updates[saved.RowKey] = clampQuality(qualityRecord{Total: saved.Total, FIR: saved.FIR, Regular: saved.Regular}, row.MarketAvailableQuantity)
		result.Imported++
	}
	if len(backup.Rows) > 0 && result.Imported == 0 {
		a.mu.Unlock()
		return TrackerQualityBackupResult{Error: "No classifications in this backup match the selected character's current inventory."}
	}
	store, err := a.qualityStoreLocked()
	if err == nil {
		err = store.update(func(data *qualityData) error {
			rows := qualityRows(data, profile)
			for key, record := range updates {
				rows[key] = record
			}
			return nil
		})
	}
	if err == nil {
		a.confirmedProgress, err = a.qualityReconcileSnapshotLocked(a.apiKey, a.state.ItemOptions, snapshot)
		if err == nil {
			a.rebuildPendingViewLocked()
		}
	}
	a.mu.Unlock()
	if err != nil {
		return TrackerQualityBackupResult{Error: "The imported classifications could not be saved locally."}
	}
	a.configureTrackedAddHotkey()
	a.emit()
	return result
}
