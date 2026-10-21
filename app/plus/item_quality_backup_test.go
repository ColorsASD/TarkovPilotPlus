package plus

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

func exportQualityBackupForTest(t *testing.T, a *ItemWindowApp) (string, trackerQualityBackup) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quality-backup.json")
	result := a.exportTrackerQuality(func(filename string) (string, error) {
		if !strings.HasPrefix(filename, "TarkovPilotPlus-items-tracker-") || !strings.HasSuffix(filename, ".json") {
			t.Fatalf("unexpected suggested filename: %q", filename)
		}
		return path, nil
	})
	if result.Error != "" || result.Canceled || result.Path != path {
		t.Fatalf("export failed: %+v", result)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := decodeTrackerQualityBackup(body)
	if err != nil || result.Exported != len(backup.Rows) {
		t.Fatalf("export did not round trip: result=%+v error=%v", result, err)
	}
	return path, backup
}

func writeQualityBackupForTest(t *testing.T, backup trackerQualityBackup) string {
	t.Helper()
	body, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "quality-backup.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTrackerQualityBackupRoundTripIsLocalAndKeepsCurrentAPITotal(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 2, 1)
	qualityStoreRemoteTotal(t, a, fake, 4, "one-uncategorized")
	path, backup := exportQualityBackupForTest(t, a)
	if len(backup.Rows) != 1 || backup.Rows[0].Total != 4 || backup.Rows[0].FIR != 2 || backup.Rows[0].Regular != 1 {
		t.Fatalf("export lost stock classification: %+v", backup)
	}
	body, _ := os.ReadFile(path)
	for _, secret := range []string{a.apiKey, qualityProfile(a.apiKey, a.state.ItemOptions), "profiles", "pending", "applied", "session", "apiKey"} {
		if secret != "" && bytes.Contains(body, []byte(secret)) {
			t.Fatalf("portable backup contains private runtime data: %q", secret)
		}
	}
	inv := a.GetTrackerInventory()
	a.SetTrackerItemQuality(inv.Profile, backup.Rows[0].RowKey, 4, 0, 4)
	result := a.importTrackerQuality(func() (string, error) { return path, nil })
	if result.Error != "" || result.Imported != 1 || result.Skipped != 0 {
		t.Fatalf("import failed: %+v", result)
	}
	if row := qualityStoreInventoryRow(t, a); row.Total != 4 || row.FIR != 2 || row.Regular != 1 || row.Uncategorized != 1 {
		t.Fatalf("import changed API total or lost quality: %+v", row)
	}
	a.qualities = nil
	if row := qualityStoreInventoryRow(t, a); row.FIR != 2 || row.Regular != 1 || row.Uncategorized != 1 {
		t.Fatalf("import did not survive a store reload: %+v", row)
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestTrackerQualityImportConservativelyReconcilesCurrentAPIStock(t *testing.T) {
	for _, test := range []struct {
		name                         string
		total, fir, regular, unknown int64
	}{{"unchanged", 3, 2, 1, 0}, {"external growth", 5, 2, 1, 2}, {"one removed", 2, 1, 0, 1}, {"two removed", 1, 0, 0, 1}, {"empty stock", 0, 0, 0, 0}} {
		t.Run(test.name, func(t *testing.T) {
			a, fake := newTypedActionTestApp(t, 2, 1)
			path, _ := exportQualityBackupForTest(t, a)
			qualityStoreRemoteTotal(t, a, fake, test.total, "current-api-stock")
			result := a.importTrackerQuality(func() (string, error) { return path, nil })
			if result.Error != "" || result.Imported != 1 {
				t.Fatalf("import failed: %+v", result)
			}
			a.mu.Lock()
			row := a.confirmedProgress.Items[0]
			a.mu.Unlock()
			if row.MarketAvailableQuantity != test.total || row.FIRQuantity != test.fir || row.RegularQuantity != test.regular || test.total-row.FIRQuantity-row.RegularQuantity != test.unknown {
				t.Fatalf("import invented stock or quality: %+v", row)
			}
			assertQualityStoreNoWrites(t, fake)
		})
	}
}

func TestTrackerQualityBackupRejectsMalformedFilesWithoutChangingLocalState(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	_, valid := exportQualityBackupForTest(t, a)
	validBody, _ := json.Marshal(valid)
	mutated := func(change func(*trackerQualityBackup)) []byte {
		copy := valid
		copy.Rows = append([]trackerQualityBackupRow(nil), valid.Rows...)
		change(&copy)
		body, _ := json.Marshal(copy)
		return body
	}
	tests := map[string][]byte{
		"empty": nil, "invalid JSON": []byte("{"), "null": []byte("null"), "trailing value": append(append([]byte(nil), validBody...), []byte(" {}")...),
		"unknown field":    bytes.Replace(validBody, []byte(`"version":1`), []byte(`"version":1,"apiKey":"forbidden"`), 1),
		"fraction":         bytes.Replace(validBody, []byte(`"fir":1`), []byte(`"fir":0.5`), 1),
		"overflow":         bytes.Replace(validBody, []byte(`"total":2`), []byte(`"total":9223372036854775808`), 1),
		"wrong format":     mutated(func(b *trackerQualityBackup) { b.Format = "other" }),
		"future version":   mutated(func(b *trackerQualityBackup) { b.Version = 2 }),
		"no character":     mutated(func(b *trackerQualityBackup) { b.CharUID = "" }),
		"invalid mode":     mutated(func(b *trackerQualityBackup) { b.GameMode = "unknown" }),
		"null rows":        mutated(func(b *trackerQualityBackup) { b.Rows = nil }),
		"duplicate rows":   mutated(func(b *trackerQualityBackup) { b.Rows = append(b.Rows, b.Rows[0]) }),
		"invalid identity": mutated(func(b *trackerQualityBackup) { b.Rows[0].RowKey = "row:" + strings.Repeat("x", 64) }),
		"no item":          mutated(func(b *trackerQualityBackup) { b.Rows[0].ItemID = "" }),
		"no name":          mutated(func(b *trackerQualityBackup) { b.Rows[0].Name = " " }),
		"negative":         mutated(func(b *trackerQualityBackup) { b.Rows[0].FIR = -1 }),
		"overclassified":   mutated(func(b *trackerQualityBackup) { b.Rows[0].Regular = 2 }),
		"quantity limit":   mutated(func(b *trackerQualityBackup) { b.Rows[0].Total = localprogress.MaxQuantity + 1 }),
		"oversize":         bytes.Repeat([]byte(" "), maxTrackerQualityBackupBytes+1),
	}
	before, _ := os.ReadFile(filepath.Join(a.dir, "item-quality.json"))
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			result := a.importTrackerQuality(func() (string, error) { return path, nil })
			if result.Error == "" || result.Imported != 0 || result.Canceled {
				t.Fatalf("invalid file accepted: %+v", result)
			}
			after, _ := os.ReadFile(filepath.Join(a.dir, "item-quality.json"))
			if !bytes.Equal(before, after) || a.GetState().Local.Error != "" {
				t.Fatal("invalid import changed classifications or poisoned inventory state")
			}
		})
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestTrackerQualityBackupGuardsDialogContextAndPendingWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ItemWindowApp)
	}{
		{"loading", func(a *ItemWindowApp) { a.state.Local.Loading = true }},
		{"refresh error", func(a *ItemWindowApp) { a.state.Local.Error = "temporary read failure" }},
		{"no API key", func(a *ItemWindowApp) { a.apiKey = "" }},
		{"character changed", func(a *ItemWindowApp) { a.state.ItemOptions.CharacterUID += "-other" }},
		{"mode changed", func(a *ItemWindowApp) { a.state.ItemOptions.Mode = "PVE" }},
		{"revision changed", func(a *ItemWindowApp) { a.localRevision++ }},
		{"metadata missing", func(a *ItemWindowApp) { a.confirmedProgress.MetadataComplete = false }},
		{"pending save", func(a *ItemWindowApp) {
			a.pendingAddQueue = append(a.pendingAddQueue, pendingMarketAdd{Key: a.apiKey, Opts: a.state.ItemOptions})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake := newTypedActionTestApp(t, 1, 1)
			path, _ := exportQualityBackupForTest(t, a)
			before, _ := os.ReadFile(filepath.Join(a.dir, "item-quality.json"))
			result := a.importTrackerQuality(func() (string, error) { a.mu.Lock(); test.change(a); a.mu.Unlock(); return path, nil })
			if result.Error == "" {
				t.Fatalf("context change during import dialog accepted: %+v", result)
			}
			after, _ := os.ReadFile(filepath.Join(a.dir, "item-quality.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("invalid context wrote local quality")
			}
			called := false
			result = a.exportTrackerQuality(func(string) (string, error) { called = true; return path, nil })
			if result.Error == "" || called {
				t.Fatalf("invalid context opened export dialog: %+v", result)
			}
			assertQualityStoreNoWrites(t, fake)
		})
	}
}

func TestTrackerQualityBackupCancellationErrorsAndRuntimeProtection(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	for name, result := range map[string]TrackerQualityBackupResult{
		"export cancel": a.exportTrackerQuality(func(string) (string, error) { return "", nil }),
		"import cancel": a.importTrackerQuality(func() (string, error) { return "", nil }),
	} {
		if !result.Canceled || result.Error != "" {
			t.Fatalf("%s failed: %+v", name, result)
		}
	}
	for name, result := range map[string]TrackerQualityBackupResult{
		"export selection":  a.exportTrackerQuality(func(string) (string, error) { return "", errors.New("dialog unavailable") }),
		"import selection":  a.importTrackerQuality(func() (string, error) { return "", errors.New("dialog unavailable") }),
		"missing file":      a.importTrackerQuality(func() (string, error) { return filepath.Join(t.TempDir(), "missing.json"), nil }),
		"directory":         a.importTrackerQuality(func() (string, error) { return t.TempDir(), nil }),
		"unwritable export": a.exportTrackerQuality(func(string) (string, error) { return filepath.Join(t.TempDir(), "missing-parent", "backup.json"), nil }),
	} {
		if result.Error == "" || result.Canceled {
			t.Fatalf("%s accepted: %+v", name, result)
		}
	}
	for _, name := range []string{"item-quality.json", "item-quality.json.tmp", "item-settings.json", "api-key.bin"} {
		result := a.exportTrackerQuality(func(string) (string, error) { return filepath.Join(a.dir, name), nil })
		if result.Error == "" {
			t.Fatalf("export could overwrite runtime file %s", name)
		}
	}
	if a.GetState().Local.Error != "" {
		t.Fatal("backup operation poisoned inventory state")
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestTrackerQualityImportRequiresExactProfileAndRowIdentity(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	_, original := exportQualityBackupForTest(t, a)
	for _, test := range []struct {
		name   string
		change func(*trackerQualityBackup)
	}{
		{"character", func(b *trackerQualityBackup) { b.CharUID += "-other" }},
		{"mode", func(b *trackerQualityBackup) { b.GameMode = "PVE" }},
		{"different row", func(b *trackerQualityBackup) { b.Rows[0].RowKey = "row:" + strings.Repeat("0", 64) }},
		{"forged item", func(b *trackerQualityBackup) { b.Rows[0].ItemID = "other" }},
		{"forged name", func(b *trackerQualityBackup) { b.Rows[0].Name = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			backup := original
			backup.Rows = append([]trackerQualityBackupRow(nil), original.Rows...)
			test.change(&backup)
			path := writeQualityBackupForTest(t, backup)
			if result := a.importTrackerQuality(func() (string, error) { return path, nil }); result.Error == "" {
				t.Fatalf("foreign backup accepted: %+v", result)
			}
			if row := qualityStoreInventoryRow(t, a); row.FIR != 1 || row.Regular != 1 {
				t.Fatalf("rejected import changed quality: %+v", row)
			}
		})
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestTrackerQualityImportFailureKeepsPriorDiskAndMemoryClassification(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	_, backup := exportQualityBackupForTest(t, a)
	backup.Rows[0].FIR, backup.Rows[0].Regular = 2, 0
	path := writeQualityBackupForTest(t, backup)
	store := a.qualities
	store.path = filepath.Join(t.TempDir(), "blocked-target")
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(store.data)
	result := a.importTrackerQuality(func() (string, error) { return path, nil })
	after, _ := json.Marshal(store.data)
	if result.Error == "" || !bytes.Equal(before, after) {
		t.Fatalf("failed disk write committed in-memory import: %+v", result)
	}
	if row := qualityStoreInventoryRow(t, a); row.FIR != 1 || row.Regular != 1 {
		t.Fatalf("failed import changed snapshot: %+v", row)
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestTrackerQualityBackupKeepsSameIDVariantsSeparateAcrossReorder(t *testing.T) {
	raw := variantArmorProgressFixture()
	raw.Items[0].AvailableQuantity, raw.Items[0].RemainingQuantity = 1, 0
	a, fake := newVariantQualityEditTestApp(t, raw)
	inv := a.GetTrackerInventory()
	first := variantQualityEditorRowForTest(t, inv, raw.Items[0].Name)
	second := variantQualityEditorRowForTest(t, inv, raw.Items[1].Name)
	a.SetTrackerItemQuality(inv.Profile, first.RowKey, 1, 0, 1)
	a.SetTrackerItemQuality(inv.Profile, second.RowKey, 2, 2, 0)
	path, backup := exportQualityBackupForTest(t, a)
	if len(backup.Rows) != 2 {
		t.Fatalf("variant export lost rows: %+v", backup)
	}
	a.SetTrackerItemQuality(inv.Profile, first.RowKey, 1, 1, 0)
	a.SetTrackerItemQuality(inv.Profile, second.RowKey, 2, 0, 2)
	raw.Items[0], raw.Items[1] = raw.Items[1], raw.Items[0]
	raw.Revision = "backup-reordered-variants"
	applyVariantQualitySnapshotForTest(t, a, raw)
	result := a.importTrackerQuality(func() (string, error) { return path, nil })
	if result.Error != "" || result.Imported != 2 {
		t.Fatalf("variant import failed: %+v", result)
	}
	inv = a.GetTrackerInventory()
	first = variantQualityEditorRowForTest(t, inv, raw.Items[1].Name)
	second = variantQualityEditorRowForTest(t, inv, raw.Items[0].Name)
	if first.FIR != 0 || first.Regular != 1 || second.FIR != 2 || second.Regular != 0 {
		t.Fatalf("variant classifications crossed: first=%+v second=%+v", first, second)
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestTrackerQualityImportSkipsMissingRowsAndKeepsUnrelatedClassifications(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	_, backup := exportQualityBackupForTest(t, a)
	missing := backup.Rows[0]
	missing.RowKey, missing.ItemID, missing.Name = "row:"+strings.Repeat("0", 64), "removed-item", "Removed item"
	backup.Rows = append(backup.Rows, missing)
	backup.Rows[0].FIR, backup.Rows[0].Regular = 2, 0
	if err := a.qualities.update(func(data *qualityData) error {
		qualityRows(data, "unrelated-profile")["unrelated-row"] = qualityRecord{Total: 8, FIR: 3, Regular: 4}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := writeQualityBackupForTest(t, backup)
	result := a.importTrackerQuality(func() (string, error) { return path, nil })
	if result.Error != "" || result.Imported != 1 || result.Skipped != 1 {
		t.Fatalf("partial import failed: %+v", result)
	}
	if got := a.qualities.data.Profiles["unrelated-profile"]["unrelated-row"]; got != (qualityRecord{Total: 8, FIR: 3, Regular: 4}) {
		t.Fatalf("import changed another profile: %+v", got)
	}
	if row := qualityStoreInventoryRow(t, a); row.FIR != 2 || row.Regular != 0 {
		t.Fatalf("matching row was not imported: %+v", row)
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestTrackerQualityExportRejectsProfileChangeDuringSaveDialog(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	path := filepath.Join(t.TempDir(), "backup.json")
	result := a.exportTrackerQuality(func(string) (string, error) {
		a.mu.Lock()
		a.apiKey += "-replaced-while-dialog-open"
		a.mu.Unlock()
		return path, nil
	})
	if result.Error == "" {
		t.Fatalf("changed profile exported stale data: %+v", result)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid export created a file: %v", err)
	}
	assertQualityStoreNoWrites(t, fake)
}
