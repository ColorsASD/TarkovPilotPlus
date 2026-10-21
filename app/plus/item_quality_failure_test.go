package plus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func blockQualitySaveForTest(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	path := filepath.Join(a.dir, "item-quality.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func assertFailedQualityKeepsFreshUnknownStock(t *testing.T, state ItemState, total int64) {
	t.Helper()
	view := state.Tracking
	if state.Local == nil || state.Local.Error == "" || view == nil || view.Error == "" {
		t.Fatalf("local quality failure was not reported: local=%+v tracking=%+v", state.Local, view)
	}
	if view.AvailableQuantity != total || view.MarketAvailableQuantity != total || view.FIRQuantity != 0 || view.RegularQuantity != 0 || view.UncategorizedQuantity != total {
		t.Fatalf("quality save failure replaced fresh API total or invented a quality: %+v", view)
	}
	if view.AddFIRAllowed || view.AddRegularAllowed || view.RemoveFIRAllowed || view.RemoveRegularAllowed {
		t.Fatalf("quality save failure left a typed action enabled: %+v", view)
	}
	if len(view.Requirements) != 2 {
		t.Fatalf("quality save failure dropped the two requirement denominators: %+v", view.Requirements)
	}
	for _, row := range view.Requirements {
		if !row.AllocationUnknown || row.AvailableQuantity != 0 || row.RequiredQuantity != 2 {
			t.Fatalf("quality save failure rendered an untyped allocation as known: %+v", row)
		}
	}
	if view.NextItem == nil || view.NextItem.Status != "unavailable" {
		t.Fatalf("quality save failure fell back to an untyped forecast: %+v", view.NextItem)
	}
}

func TestQualityDiskFailureApplyPublishesFreshAPITotalWithUnknownAllocation(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 0)
	blockQualitySaveForTest(t, a)
	setPendingACKExternalStock(fake, 3, "fresh-stock-before-apply-quality-failure")
	remote := cloneTrackedSnapshot(fake.snapshot)
	merged := marketAnyProgressSnapshotForTest(t, remote, typedActionQuests(remote).Quests...)
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, remote, merged, "")
	a.mu.Unlock()
	assertFailedQualityKeepsFreshUnknownStock(t, a.GetState(), 3)
	if a.confirmedProgress.Revision != remote.Revision {
		t.Fatalf("failed local save kept a stale confirmed API revision: %s", a.confirmedProgress.Revision)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 0 {
		t.Fatalf("read-side quality failure wrote API stock: %+v", writes)
	}
}

func TestQualityDiskFailureRefreshPublishesFreshAPITotalWithUnknownAllocation(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 0)
	blockQualitySaveForTest(t, a)
	setPendingACKExternalStock(fake, 3, "fresh-stock-before-refresh-quality-failure")
	a.mu.Lock()
	a.localAt = time.Time{}
	key, opts, item := a.apiKey, a.state.ItemOptions, *a.state.Item
	a.mu.Unlock()
	if err := a.refreshLocalProgress(a.ctx, key, opts, item); err == nil {
		t.Fatal("refresh did not report the failed local quality save")
	}
	assertFailedQualityKeepsFreshUnknownStock(t, a.GetState(), 3)
	if a.confirmedProgress.Revision != "fresh-stock-before-refresh-quality-failure" {
		t.Fatalf("refresh kept a stale confirmed API revision: %s", a.confirmedProgress.Revision)
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 0 {
		t.Fatalf("refresh quality failure wrote API stock: %+v", writes)
	}
}

func TestTypedDuplicateVariantsKeepOwnAPITotalsAndUnknownReadOnlyAllocation(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	raw := variantArmorProgressFixture()
	a.qualityTracking = true
	a.state.CharacterUID = raw.CharUID
	a.state.Item = &market.Item{UID: "variant-market-uid", BSGID: variantArmorID, Name: raw.Items[1].Name}
	merged := marketAnyProgressSnapshotForTest(t, raw,
		market.Quest{QuestID: "fishing-gear", Name: "Fishing Gear", Status: "available"},
		market.Quest{QuestID: "scavenger", Name: "Scavenger", Status: "available"})
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, raw, merged, "")
	a.mu.Unlock()
	state := a.GetState()
	if state.Tracking == nil || len(state.Tracking.Variants) != 2 {
		t.Fatalf("duplicate variants lost their separate panels: %+v", state.Tracking)
	}
	for i, variant := range state.Tracking.Variants {
		if variant.AvailableQuantity != raw.Items[i].AvailableQuantity || variant.MarketAvailableQuantity != raw.Items[i].AvailableQuantity || variant.UncategorizedQuantity != raw.Items[i].AvailableQuantity || variant.FIRQuantity != 0 || variant.RegularQuantity != 0 {
			t.Fatalf("variant %d lost its own API total or invented quality: %+v", i, variant)
		}
		if variant.Error == "" || variant.AddAllowed || variant.RemoveAllowed || variant.AddFIRAllowed || variant.AddRegularAllowed || variant.RemoveFIRAllowed || variant.RemoveRegularAllowed {
			t.Fatalf("ambiguous variant %d remained writable: %+v", i, variant)
		}
		for _, requirement := range variant.Requirements {
			if !requirement.AllocationUnknown || requirement.AvailableQuantity != 0 {
				t.Fatalf("variant fixed quality was rendered as known: %+v", requirement)
			}
		}
		for _, group := range variant.AnyGroups {
			if !group.AllocationUnknown || group.AvailableQuantity != 0 || group.RequiredQuantity != 15 {
				t.Fatalf("variant Shared quality was rendered as known or dropped its goal: %+v", group)
			}
		}
	}
	if len(state.Tracking.Variants[0].Requirements) != 1 || len(state.Tracking.Variants[1].AnyGroups) != 1 {
		t.Fatalf("duplicate variants dropped their fixed or Shared requirement: %+v", state.Tracking.Variants)
	}
	inv := a.GetTrackerInventory()
	if inv.Error != "" || len(inv.Rows) != 1 || !inv.Rows[0].ReadOnly || inv.Rows[0].Total != 2 || inv.Rows[0].Uncategorized != 2 {
		t.Fatalf("duplicate variant editor did not preserve read-only API stock: %+v", inv)
	}
	state = a.SetTrackerItemQuality(inv.Profile, variantArmorID, 2, 1, 1)
	if state.Local == nil || state.Local.Error == "" {
		t.Fatal("editor accepted a quality assignment for duplicate item IDs")
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 0 {
		t.Fatalf("duplicate variant display or editor wrote API stock: %+v", writes)
	}
}
