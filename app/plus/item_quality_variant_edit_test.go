package plus

import (
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func applyVariantQualitySnapshotForTest(t *testing.T, a *ItemWindowApp, raw market.TrackedSnapshot) {
	t.Helper()
	merged := marketAnyProgressSnapshotForTest(t, raw,
		market.Quest{QuestID: "fishing-gear", Name: "Fishing Gear", Status: "available"},
		market.Quest{QuestID: "scavenger", Name: "Scavenger", Status: "available"},
		market.Quest{QuestID: "collector", Name: "Collector", Status: "available"})
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, raw, merged, "")
	a.mu.Unlock()
	if state := a.GetState(); state.Local == nil || state.Local.Error != "" {
		t.Fatalf("variant fixture failed to load quality: %+v", state.Local)
	}
}

func newVariantQualityEditTestApp(t *testing.T, raw market.TrackedSnapshot) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newTrackingTestApp(t)
	a.qualityTracking = true
	a.state.CharacterUID = raw.CharUID
	a.state.Item = &market.Item{UID: "variant-quality-market-uid", BSGID: variantArmorID, Name: raw.Items[1].Name}
	applyVariantQualitySnapshotForTest(t, a, raw)
	return a, fake
}

func variantQualityEditorRowForTest(t *testing.T, inventory TrackerQualityInventory, name string) TrackerQualityRow {
	t.Helper()
	if inventory.Error != "" {
		t.Fatalf("variant editor could not load: %s", inventory.Error)
	}
	var result TrackerQualityRow
	found := 0
	for _, row := range inventory.Rows {
		if row.Name == name {
			result, found = row, found+1
		}
	}
	if found != 1 {
		t.Fatalf("expected one editor row named %q, got %d: %+v", name, found, inventory.Rows)
	}
	return result
}

func TestVariantLocalQualityEditKeepsSameIDStocksSeparateAndSurvivesRowReorder(t *testing.T) {
	raw := variantArmorProgressFixture()
	raw.Items[0].AvailableQuantity, raw.Items[0].RemainingQuantity = 1, 0
	// A forrássorrend és a progress mezők nem lehetnek a variáns azonossága.
	raw.Items[1].Sources = append(raw.Items[1].Sources,
		market.TrackedSource{Type: "quest", QuestID: "collector", Name: "Collector", Amount: 1, RemainingRequiredQuantity: 1, FIR: true})
	a, fake := newVariantQualityEditTestApp(t, raw)
	inv := a.GetTrackerInventory()
	first := variantQualityEditorRowForTest(t, inv, raw.Items[0].Name)
	second := variantQualityEditorRowForTest(t, inv, raw.Items[1].Name)
	if first.ReadOnly || second.ReadOnly || first.ItemID != second.ItemID || first.RowKey == "" || second.RowKey == "" || first.RowKey == second.RowKey {
		t.Fatalf("distinct same-ID variants did not receive separate writable local identities: first=%+v second=%+v", first, second)
	}
	state := a.SetTrackerItemQuality(inv.Profile, second.RowKey, 2, 1, 1)
	if state.Local.Error != "" {
		t.Fatalf("two-piece variant could not be classified: %s", state.Local.Error)
	}
	inv = a.GetTrackerInventory()
	first = variantQualityEditorRowForTest(t, inv, raw.Items[0].Name)
	second = variantQualityEditorRowForTest(t, inv, raw.Items[1].Name)
	if first.Total != 1 || first.FIR != 0 || first.Regular != 0 || first.Uncategorized != 1 || second.Total != 2 || second.FIR != 1 || second.Regular != 1 || second.Uncategorized != 0 {
		t.Fatalf("editing two-piece variant changed another same-ID stock: first=%+v second=%+v", first, second)
	}
	state = a.SetTrackerItemQuality(inv.Profile, first.RowKey, 1, 0, 1)
	if state.Local.Error != "" {
		t.Fatalf("one-piece variant could not be classified independently: %s", state.Local.Error)
	}
	firstKey, secondKey := first.RowKey, second.RowKey
	raw.Items[1].Sources[0], raw.Items[1].Sources[1] = raw.Items[1].Sources[1], raw.Items[1].Sources[0]
	raw.Items[1].Sources[0].RemainingRequiredQuantity = 0
	raw.Items[1].Sources[0].Completed = true
	raw.Items[0], raw.Items[1] = raw.Items[1], raw.Items[0]
	raw.Revision = "reordered-variant-quality-progress"
	a.qualities = nil
	applyVariantQualitySnapshotForTest(t, a, raw)
	inv = a.GetTrackerInventory()
	first = variantQualityEditorRowForTest(t, inv, raw.Items[1].Name)
	second = variantQualityEditorRowForTest(t, inv, raw.Items[0].Name)
	if first.RowKey != firstKey || second.RowKey != secondKey || first.Total != 1 || first.FIR != 0 || first.Regular != 1 || second.Total != 2 || second.FIR != 1 || second.Regular != 1 {
		t.Fatalf("row/source reorder or progress update moved persisted variant qualities: first=%+v second=%+v", first, second)
	}
	for _, variant := range a.GetState().Tracking.Variants {
		if variant.AddAllowed || variant.RemoveAllowed || variant.AddFIRAllowed || variant.AddRegularAllowed || variant.RemoveFIRAllowed || variant.RemoveRegularAllowed {
			t.Fatalf("local variant editor enabled an ambiguous remote stock action: %+v", variant)
		}
	}
	state = a.SetTrackerItemQuality(inv.Profile, variantArmorID, 2, 1, 1)
	if state.Local == nil || state.Local.Error == "" {
		t.Fatal("raw item ID bypassed distinct variant row identity")
	}
	a.AddFIRTrackedItem(a.state.Item.UID)
	a.RemoveRegularTrackedItem(a.state.Item.UID)
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 0 {
		t.Fatalf("local variant edits or ambiguous stock actions wrote API stock: %+v", writes)
	}
}

func TestVariantLocalQualityEditorRejectsIndistinguishableStableIdentities(t *testing.T) {
	raw := variantArmorProgressFixture()
	raw.Groups = nil
	sources := []market.TrackedSource{
		{Type: "quest", QuestID: "fishing-gear", Amount: 2, RemainingRequiredQuantity: 2},
		{Type: "quest", QuestID: "collector", Amount: 2, RemainingRequiredQuantity: 2, FIR: true},
	}
	for i := 0; i < 2; i++ {
		raw.Items[i].Name = "MF-UNTAR indistinguishable variant"
		raw.Items[i].AvailableQuantity = int64(i + 1)
		raw.Items[i].RequiredQuantity, raw.Items[i].RemainingQuantity = 4, 4-int64(i+1)
		raw.Items[i].Sources = append([]market.TrackedSource(nil), sources...)
	}
	raw.Items[1].Sources[0], raw.Items[1].Sources[1] = raw.Items[1].Sources[1], raw.Items[1].Sources[0]
	raw.Items[1].Sources[0].Amount, raw.Items[1].Sources[0].RemainingRequiredQuantity = 3, 3
	a, fake := newVariantQualityEditTestApp(t, raw)
	inv := a.GetTrackerInventory()
	if inv.Error != "" || len(inv.Rows) != 2 || inv.Rows[0].RowKey != inv.Rows[1].RowKey || !inv.Rows[0].ReadOnly || !inv.Rows[1].ReadOnly {
		t.Fatalf("quantity or source order incorrectly distinguished duplicate identities: %+v", inv)
	}
	for _, row := range inv.Rows {
		state := a.SetTrackerItemQuality(inv.Profile, row.RowKey, row.Total, row.Total, 0)
		if state.Local == nil || state.Local.Error == "" {
			t.Fatalf("editor accepted an indistinguishable variant row: %+v", row)
		}
	}
	if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 0 {
		t.Fatalf("ambiguous local classification wrote API stock: %+v", writes)
	}
}
