package plus

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func qualityStoreRemoteTotal(t *testing.T, a *ItemWindowApp, fake *fakeTrackingAPI, total int64, revision string) {
	t.Helper()
	fake.mu.Lock()
	fake.snapshot.Revision = revision
	fake.snapshot.Items[0].AvailableQuantity = total
	fake.snapshot.Items[0].RemainingQuantity = max(0, fake.snapshot.Items[0].RequiredQuantity-total)
	remote := cloneTrackedSnapshot(fake.snapshot)
	fake.mu.Unlock()
	merged, err := localprogress.Merge(remote, typedActionQuests(remote), market.HideoutSnapshot{
		CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Modules: []market.HideoutUpgrade{},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, remote, merged, "")
	a.mu.Unlock()
}

func qualityStoreInventoryRow(t *testing.T, a *ItemWindowApp) TrackerQualityRow {
	t.Helper()
	inv := a.GetTrackerInventory()
	if inv.Error != "" || len(inv.Rows) != 1 {
		t.Fatalf("expected one editable inventory row: %+v", inv)
	}
	return inv.Rows[0]
}

func assertQualityStoreNoWrites(t *testing.T, fake *fakeTrackingAPI) {
	t.Helper()
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 0 {
		t.Fatalf("local quality work sent a Market stock write: %+v", writes)
	}
}

func TestQualityStoreStartsUncategorizedAndEditorPersistsWithoutChangingMarketStock(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	qualityStoreRemoteTotal(t, a, fake, 3, "external-initial-stock")
	row := qualityStoreInventoryRow(t, a)
	if row.Total != 3 || row.FIR != 0 || row.Regular != 0 || row.Uncategorized != 3 || row.ReadOnly {
		t.Fatalf("initial API stock acquired an invented quality: %+v", row)
	}
	inv := a.GetTrackerInventory()
	state := a.SetTrackerItemQuality(inv.Profile, row.ItemID, row.Total, 1, 1)
	if state.Local == nil || state.Local.Error != "" {
		t.Fatalf("local editor failed: %+v", state.Local)
	}
	row = qualityStoreInventoryRow(t, a)
	if row.Total != 3 || row.FIR != 1 || row.Regular != 1 || row.Uncategorized != 1 {
		t.Fatalf("editor did not keep one uncategorized piece: %+v", row)
	}
	fake.mu.Lock()
	apiTotal := fake.snapshot.Items[0].AvailableQuantity
	fake.mu.Unlock()
	if apiTotal != 3 || a.confirmedProgress.Items[0].MarketAvailableQuantity != 3 {
		t.Fatal("quality editor changed the API total")
	}
	reloaded := &ItemWindowApp{dir: a.dir, apiKey: a.apiKey, qualityTracking: true, state: a.GetState(),
		confirmedProgress: localprogress.Clone(a.confirmedProgress)}
	if got := qualityStoreInventoryRow(t, reloaded); got != row {
		t.Fatalf("fresh store reload lost quality: before=%+v after=%+v", row, got)
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestQualityStoreExternalChangesKeepOnlyKnownQualityMinimum(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	qualityStoreRemoteTotal(t, a, fake, 4, "external-growth")
	if row := qualityStoreInventoryRow(t, a); row.Total != 4 || row.FIR != 1 || row.Regular != 1 || row.Uncategorized != 2 {
		t.Fatalf("external growth invented a stock quality: %+v", row)
	}
	qualityStoreRemoteTotal(t, a, fake, 3, "external-decrease-unknown-type")
	if row := qualityStoreInventoryRow(t, a); row.Total != 3 || row.FIR != 0 || row.Regular != 0 || row.Uncategorized != 3 {
		t.Fatalf("external decrease assumed which quality remained: %+v", row)
	}
	inv := a.GetTrackerInventory()
	a.SetTrackerItemQuality(inv.Profile, trackingBSGID, 3, 2, 1)
	qualityStoreRemoteTotal(t, a, fake, 2, "external-decrease-safe-minimum")
	if row := qualityStoreInventoryRow(t, a); row.Total != 2 || row.FIR != 1 || row.Regular != 0 || row.Uncategorized != 1 {
		t.Fatalf("external decrease lost the guaranteed FiR minimum: %+v", row)
	}
	inv = a.GetTrackerInventory()
	a.SetTrackerItemQuality(inv.Profile, trackingBSGID, 2, 1, 1)
	qualityStoreRemoteTotal(t, a, fake, 1, "external-one-of-two-removed")
	if row := qualityStoreInventoryRow(t, a); row.Total != 1 || row.FIR != 0 || row.Regular != 0 || row.Uncategorized != 1 {
		t.Fatalf("one remaining piece cannot be distinguished as FiR or Regular: %+v", row)
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestQualityStoreProfilesSeparateAPIKeysCharactersAndModes(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	originalKey, originalOpts := a.apiKey, a.state.ItemOptions
	originalSnapshot := localprogress.Clone(a.confirmedProgress)
	for _, test := range []struct {
		name string
		key  string
		opts ItemOptions
	}{
		{"API key", originalKey + "-different", originalOpts},
		{"character", originalKey, func() ItemOptions { opts := originalOpts; opts.CharacterUID += "-different"; return opts }()},
		{"game mode", originalKey, func() ItemOptions {
			opts := originalOpts
			if opts.Mode == "PVE" {
				opts.Mode = "PVP"
			} else {
				opts.Mode = "PVE"
			}
			return opts
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			other := &ItemWindowApp{dir: a.dir, apiKey: test.key, qualityTracking: true, state: a.GetState(),
				confirmedProgress: localprogress.Clone(originalSnapshot)}
			other.state.ItemOptions = test.opts
			other.confirmedProgress.CharUID, other.confirmedProgress.GameMode = test.opts.CharacterUID, test.opts.Mode
			row := qualityStoreInventoryRow(t, other)
			if row.FIR != 0 || row.Regular != 0 || row.Uncategorized != 2 {
				t.Fatalf("another profile borrowed existing quality: %+v", row)
			}
		})
	}
	if row := qualityStoreInventoryRow(t, a); row.FIR != 1 || row.Regular != 1 || row.Uncategorized != 0 {
		t.Fatalf("other profile reconciliation changed the original profile: %+v", row)
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestQualityStoreRejectsOverflowAndPreservesInvalidFile(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	inv := a.GetTrackerInventory()
	before, err := os.ReadFile(filepath.Join(a.dir, "item-quality.json"))
	if err != nil {
		t.Fatal(err)
	}
	state := a.SetTrackerItemQuality(inv.Profile, trackingBSGID, 2, math.MaxInt64, math.MaxInt64)
	after, err := os.ReadFile(filepath.Join(a.dir, "item-quality.json"))
	if err != nil || state.Local == nil || state.Local.Error == "" || !bytes.Equal(before, after) {
		t.Fatalf("overflowing edit was accepted or changed the quality file: err=%v local=%+v", err, state.Local)
	}
	for _, content := range [][]byte{
		[]byte("{broken-json"),
		[]byte(`{"version":1,"profiles":{"profile":{"item":{"total":1,"fir":9223372036854775807,"regular":9223372036854775807}}},"pending":{},"applied":{}}`),
		[]byte(`{"version":1,"profiles":{},"pending":{"bad":{"Profile":"p","Item":"item","BeforeRevision":"r","Before":{"total":1,"fir":1,"regular":0},"Delta":2,"FIR":true}},"applied":{}}`),
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "item-quality.json")
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		other := &ItemWindowApp{dir: dir, apiKey: a.apiKey, qualityTracking: true, state: a.GetState(),
			confirmedProgress: localprogress.Clone(a.confirmedProgress)}
		got := other.GetTrackerInventory()
		preserved, err := os.ReadFile(path)
		if got.Error == "" || err != nil || !bytes.Equal(content, preserved) {
			t.Fatalf("invalid quality file was accepted or overwritten: error=%q read=%v", got.Error, err)
		}
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestQualityStoreOrphanJournalNeverInfersAnOwnWriteFromAChangedAPITotal(t *testing.T) {
	for _, test := range []struct {
		name, revision string
		total          int64
		wantFIR        int64
		wantRegular    int64
	}{
		{"unchanged before snapshot", "journal-before", 2, 1, 1},
		{"exact target but no own ACK", "journal-after", 3, 0, 0},
		{"different external quantity", "external-after", 4, 0, 0},
		{"same quantity new revision", "external-same-total", 2, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake := newTypedActionTestApp(t, 1, 1)
			a.mu.Lock()
			profile := qualityProfile(a.apiKey, a.state.ItemOptions)
			store, err := a.qualityStoreLocked()
			if err == nil {
				err = store.update(func(data *qualityData) error {
					data.Pending["old-session:42"] = qualityOperation{Profile: profile, Item: trackingBSGID,
						BeforeRevision: "journal-before", Before: qualityRecord{Total: 2, FIR: 1, Regular: 1}, Delta: 1, FIR: true}
					return nil
				})
			}
			a.qualities = nil
			a.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			qualityStoreRemoteTotal(t, a, fake, test.total, test.revision)
			row := qualityStoreInventoryRow(t, a)
			if row.Total != test.total || row.FIR != test.wantFIR || row.Regular != test.wantRegular ||
				row.Uncategorized != test.total-test.wantFIR-test.wantRegular {
				t.Fatalf("orphan journal guessed a stock quality: %+v", row)
			}
			body, err := os.ReadFile(filepath.Join(a.dir, "item-quality.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved qualityData
			if err := json.Unmarshal(body, &saved); err != nil || len(saved.Pending) != 0 {
				t.Fatalf("orphan journal was not cleared after read-only recovery: %v %+v", err, saved.Pending)
			}
			a.qualities = nil
			if reloaded := qualityStoreInventoryRow(t, a); reloaded != row {
				t.Fatalf("journal recovery was applied again after reload: before=%+v after=%+v", row, reloaded)
			}
			assertQualityStoreNoWrites(t, fake)
		})
	}
}
