package plus

import (
	"encoding/json"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

func TestTypedUnknownGroupClearsEveryCandidateCounter(t *testing.T) {
	view := &ItemTrackingState{AnyGroups: []ItemTrackingAnyGroup{{GroupID: "quest:missing:any", RequiredQuantity: 5, AvailableQuantity: 3,
		OtherStock: []nextitem.StockContribution{{ItemID: "other", Name: "Other", FIRQuantity: 2}},
		Sources: []ItemTrackingAnySource{{GroupID: "quest:missing:any", RequiredQuantity: 5, AvailableQuantity: 3,
			Items: []ItemTrackingAnyItem{{ItemID: "selected", AvailableQuantity: 1}, {ItemID: "other", AvailableQuantity: 2}}}},
	}}}
	row := localprogress.Item{ItemID: "selected", QualityKnown: true, AvailableQuantity: 1, MarketAvailableQuantity: 1}
	applyTypedTrackingView(view, row, localprogress.Snapshot{Items: []localprogress.Item{row}})
	group := view.AnyGroups[0]
	if !group.AllocationUnknown || group.AvailableQuantity != 0 || group.RequiredQuantity != 5 || len(group.OtherStock) != 0 {
		t.Fatalf("unknown typed group lost its raw demand: %+v", group)
	}
	for _, source := range group.Sources {
		if source.AvailableQuantity != 0 {
			t.Fatal("unknown source reused API group stock")
		}
		for _, candidate := range source.Items {
			if candidate.AvailableQuantity != 0 {
				t.Fatalf("unknown candidate reused untyped API stock: %+v", candidate)
			}
		}
	}
}

func TestTypedZibboTrackingDTOExplainsSharedCoverageFromOtherStock(t *testing.T) {
	const zibbo, fireklean, tp200, crickent = "zibbo", "fireklean", "tp200", "crickent"
	raw := market.TrackedSnapshot{CharUID: "zibbo-character", GameMode: "SEASON", Revision: "zibbo-revision", Items: []market.TrackedItem{
		{ItemID: zibbo, Name: "Zibbo lighter", RequiredQuantity: 1, RemainingQuantity: 1, Sources: []market.TrackedSource{
			{Type: "quest", QuestID: "golden-swag", Name: "Golden Swag", Amount: 1, RemainingRequiredQuantity: 1},
			{Type: "questGroup", QuestID: "fuel-shortage", RequirementID: "flammable", FIR: true},
		}},
		{ItemID: fireklean, Name: "#FireKlean gun lube", Icon: "https://cdn.tarkov-market.app/images/fireklean.png", AvailableQuantity: 2, RequiredQuantity: 1,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "collector", Name: "Collector", Amount: 1, RemainingRequiredQuantity: 1, FIR: true}}},
		{ItemID: tp200, Name: "TP-200 TNT brick", AvailableQuantity: 2, Sources: []market.TrackedSource{}},
		{ItemID: crickent, Name: "Crickent lighter", AvailableQuantity: 1, Sources: []market.TrackedSource{}},
	}, Groups: []market.TrackedGroup{{QuestID: "fuel-shortage", QuestName: "Fuel Shortage", RequirementID: "flammable", Name: "Flammable materials",
		FIR: true, Amount: 3, RequiredQuantity: 3, AvailableQuantity: 3, ItemIDs: []string{zibbo, fireklean, tp200, crickent}}}}
	snapshot := marketAnyProgressSnapshotForTest(t, raw,
		market.Quest{QuestID: "golden-swag", Name: "Golden Swag", Status: "locked"},
		market.Quest{QuestID: "fuel-shortage", Name: "Fuel Shortage", Status: "available"},
		market.Quest{QuestID: "collector", Name: "Collector", Status: "locked"})
	for i := range snapshot.Items {
		row := &snapshot.Items[i]
		row.QualityKnown, row.RegularQuantity = true, row.AvailableQuantity
		if row.ItemID == fireklean {
			row.FIRQuantity, row.RegularQuantity = 1, 1
		}
	}
	before, _ := json.Marshal(snapshot)
	view := localTrackedView(market.Item{UID: "zibbo-uid", BSGID: zibbo}, snapshot)
	if view.AvailableQuantity != 0 || view.RequiredQuantity != 3 || len(view.Requirements) != 1 ||
		view.Requirements[0].Kind != "regular" || view.Requirements[0].AvailableQuantity != 0 || view.Requirements[0].RequiredQuantity != 1 ||
		len(view.AnyGroups) != 1 || view.AnyGroups[0].AvailableQuantity != 1 || view.AnyGroups[0].RequiredQuantity != 3 ||
		!view.AddFIRAllowed || !view.AddRegularAllowed || view.RemoveFIRAllowed || view.RemoveRegularAllowed {
		t.Fatalf("Zibbo Stock 0/3, Regular 0/1 or Shared (FiR) 1/3 contract failed: %+v", view)
	}
	if view.NextItem == nil || len(view.NextItem.Targets) != 2 {
		t.Fatalf("Zibbo independent goals missing: %+v", view.NextItem)
	}
	golden, fuel := view.NextItem.Targets[0], view.NextItem.Targets[1]
	if golden.AvailableQuantity != 0 || golden.RequiredQuantity != 1 || len(golden.OtherStock) != 0 ||
		fuel.AvailableQuantity != 1 || fuel.RequiredQuantity != 3 || fuel.FIRQuantity != 1 || fuel.RegularQuantity != 0 {
		t.Fatalf("Zibbo goals lost their independent current coverage: %+v", view.NextItem.Targets)
	}
	for _, other := range [][]nextitem.StockContribution{view.AnyGroups[0].OtherStock, fuel.OtherStock} {
		if len(other) != 1 || other[0].ItemID != fireklean || other[0].Name != "#FireKlean gun lube" ||
			other[0].Icon != raw.Items[1].Icon || other[0].FIRQuantity != 1 || other[0].RegularQuantity != 0 {
			t.Fatalf("Zibbo DTO included unallocated, nonFiR or full alternative inventory: %+v", other)
		}
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("tracking DTO construction changed the source snapshot")
	}
}

func TestTrackingCopyOwnsOtherStockDetailsIncludingVariants(t *testing.T) {
	contribution := nextitem.StockContribution{ItemID: "other", Name: "Other", FIRQuantity: 1}
	source := &ItemTrackingState{
		AnyGroups: []ItemTrackingAnyGroup{{OtherStock: []nextitem.StockContribution{contribution}}},
		NextItem:  &nextitem.Forecast{Targets: []nextitem.Target{{OtherStock: []nextitem.StockContribution{contribution}}}},
		Variants: []*ItemTrackingState{{AnyGroups: []ItemTrackingAnyGroup{{OtherStock: []nextitem.StockContribution{contribution}}},
			NextItem: &nextitem.Forecast{Targets: []nextitem.Target{{OtherStock: []nextitem.StockContribution{contribution}}}}}},
	}
	copy := cloneTrackingState(source)
	copy.AnyGroups[0].OtherStock[0].Name = "Changed group"
	copy.NextItem.Targets[0].OtherStock[0].FIRQuantity = 99
	copy.Variants[0].AnyGroups[0].OtherStock[0].RegularQuantity = 99
	copy.Variants[0].NextItem.Targets[0].OtherStock[0].Name = "Changed variant"
	if source.AnyGroups[0].OtherStock[0] != contribution || source.NextItem.Targets[0].OtherStock[0] != contribution ||
		source.Variants[0].AnyGroups[0].OtherStock[0] != contribution || source.Variants[0].NextItem.Targets[0].OtherStock[0] != contribution {
		t.Fatal("cloned backend/cache DTO shares mutable contribution slices")
	}
}

func TestTypedGroupOtherStockMergesOnlyAllocatedPositiveContributions(t *testing.T) {
	contribution := nextitem.StockContribution{ItemID: "other", Name: "Other", FIRQuantity: 1}
	view := &ItemTrackingState{AnyGroups: []ItemTrackingAnyGroup{{RequiredQuantity: 3, Sources: []ItemTrackingAnySource{
		{GroupID: "quest:first:any", RequiredQuantity: 1}, {GroupID: "quest:second:any", RequiredQuantity: 2},
	}}}}
	typed := nextitem.TypedView{Known: true, StockSummary: &nextitem.TypedStockSummary{Groups: map[string]nextitem.TypedGroupStock{
		"quest:first:any":  {AvailableQuantity: 1, RequiredQuantity: 1, OtherStock: []nextitem.StockContribution{contribution}},
		"quest:second:any": {AvailableQuantity: 2, RequiredQuantity: 2, OtherStock: []nextitem.StockContribution{contribution}},
	}}}
	applyTypedTrackingResult(view, localprogress.Item{}, localprogress.Snapshot{}, typed)
	if len(view.AnyGroups[0].OtherStock) != 1 || view.AnyGroups[0].OtherStock[0].FIRQuantity != 2 {
		t.Fatalf("merged Shared bucket repeated or lost another item's actual contributions: %+v", view.AnyGroups[0])
	}
	delete(typed.StockSummary.Groups, "quest:second:any")
	applyTypedTrackingResult(view, localprogress.Item{}, localprogress.Snapshot{}, typed)
	if !view.AnyGroups[0].AllocationUnknown || len(view.AnyGroups[0].OtherStock) != 0 {
		t.Fatal("partially unknown Shared bucket retained a misleading other-stock detail")
	}
}

func TestTypedMedsTrackingReservesFIRAndReportsRegularFIRUse(t *testing.T) {
	for _, test := range []struct {
		fir, regular, wantFIR, wantRegular, wantRegularFIR int64
	}{{1, 0, 1, 0, 0}, {21, 0, 20, 1, 1}, {20, 1, 20, 1, 0}, {21, 1, 20, 1, 0}} {
		raw := market.TrackedSnapshot{CharUID: "meds-character", GameMode: "SEASON", Revision: "meds-revision", Items: []market.TrackedItem{{
			ItemID: "meds", Name: "Pile of meds", AvailableQuantity: test.fir + test.regular, RequiredQuantity: 21,
			Sources: []market.TrackedSource{
				{Type: "quest", QuestID: "crisis", Name: "Crisis", Amount: 20, RemainingRequiredQuantity: 20, FIR: true},
				{Type: "quest", QuestID: "regular", Name: "Regular goal", Amount: 1, RemainingRequiredQuantity: 1},
			},
		}}}
		snapshot := marketAnyProgressSnapshotForTest(t, raw,
			market.Quest{QuestID: "crisis", Name: "Crisis", Status: "locked"},
			market.Quest{QuestID: "regular", Name: "Regular goal", Status: "available"})
		snapshot.Items[0].QualityKnown, snapshot.Items[0].FIRQuantity, snapshot.Items[0].RegularQuantity = true, test.fir, test.regular
		view := localTrackedView(market.Item{UID: "meds-uid", BSGID: "meds"}, snapshot)
		if view.AvailableQuantity != test.fir+test.regular || view.RequiredQuantity != 21 || len(view.Requirements) != 2 ||
			view.Requirements[0].Kind != "fir" || view.Requirements[0].RequiredQuantity != 20 || view.Requirements[0].AvailableQuantity != test.wantFIR ||
			view.Requirements[0].FIRQuantity != 0 || view.Requirements[1].Kind != "regular" || view.Requirements[1].RequiredQuantity != 1 ||
			view.Requirements[1].AvailableQuantity != test.wantRegular || view.Requirements[1].FIRQuantity != test.wantRegularFIR {
			t.Fatalf("meds Stock category DTO or Regular FiR marker is wrong: %+v", view)
		}
		body, err := json.Marshal(view.Requirements[1])
		if err != nil {
			t.Fatal(err)
		}
		var dto map[string]any
		if err := json.Unmarshal(body, &dto); err != nil {
			t.Fatal(err)
		}
		_, markerPresent := dto["firQuantity"]
		if markerPresent != (test.wantRegularFIR > 0) {
			t.Fatalf("zero Regular FiR metadata was emitted or actual use disappeared: %s", body)
		}
	}
}

func TestTypedUnknownAllocationClearsRegularFIRMetadata(t *testing.T) {
	for _, markOnly := range []bool{false, true} {
		view := &ItemTrackingState{Requirements: []ItemTrackingRequirement{{Kind: "regular", RequiredQuantity: 3, AvailableQuantity: 2, FIRQuantity: 2}}}
		if markOnly {
			markTypedAllocationUnknown(view)
		} else {
			applyTypedTrackingView(view, localprogress.Item{ItemID: "missing"}, localprogress.Snapshot{})
		}
		if !view.Requirements[0].AllocationUnknown || view.Requirements[0].FIRQuantity != 0 {
			t.Fatalf("unknown allocation retained a stale Regular FiR marker: %+v", view.Requirements)
		}
	}
}
