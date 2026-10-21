package nextitem

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func contributionSnapshotForTest(fir bool, amount int64, ownFIR, ownRegular, otherFIR, otherRegular int64) localprogress.Snapshot {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "shared", Name: "Shared goal", Status: "available"}}
	snapshot.Items = []localprogress.Item{
		typedItem("selected", ownFIR, ownRegular, 0, sharedSource("shared", "any", fir)),
		typedItem("other", otherFIR, otherRegular, 0, sharedSource("shared", "any", fir)),
	}
	snapshot.Items[1].Name = "Other item"
	snapshot.Items[1].Icon = "https://cdn.tarkov-market.app/images/other.png"
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: amount,
		RequiredQuantity: amount, FIR: fir, ItemIDs: []string{"selected", "other"}}}
	return snapshot
}

func assertOtherStockForTest(t *testing.T, got []StockContribution, fir, regular int64) {
	t.Helper()
	if fir+regular == 0 {
		if len(got) != 0 {
			t.Fatalf("a goal without other allocated stock acquired details: %+v", got)
		}
		return
	}
	if len(got) != 1 || got[0].ItemID != "other" || got[0].Name != "Other item" ||
		got[0].Icon != "https://cdn.tarkov-market.app/images/other.png" || got[0].FIRQuantity != fir || got[0].RegularQuantity != regular {
		t.Fatalf("allocated other-item details lost their identity, icon or physical quality: %+v", got)
	}
}

func TestTypedZibboStockDetailsUseSamePriorityAsItsCollectionTarget(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "golden-swag", Name: "Golden Swag", Status: "locked"},
		{QuestID: "fuel-shortage", Name: "Fuel Shortage", Status: "available"},
		{QuestID: "collector", Name: "Collector", Status: "locked"},
	}
	snapshot.Items = []localprogress.Item{
		typedItem("zibbo", 0, 0, 0, fixedSource("golden-swag", 1, false), sharedSource("fuel-shortage", "flammable", true)),
		typedItem("fireklean", 1, 1, 0, fixedSource("collector", 1, true), sharedSource("fuel-shortage", "flammable", true)),
		typedItem("tp-200", 0, 2, 0, sharedSource("fuel-shortage", "flammable", true)),
		typedItem("crickent", 0, 1, 0, sharedSource("fuel-shortage", "flammable", true)),
		typedItem("missing-collectibles", 0, 0, 0, fixedSource("collector", 10, true)),
	}
	snapshot.Items[0].Name, snapshot.Items[1].Name = "Zibbo lighter", "#FireKlean gun lube"
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "fuel-shortage", RequirementID: "flammable", Amount: 3,
		RequiredQuantity: 3, AvailableQuantity: 3, FIR: true, ItemIDs: []string{"zibbo", "fireklean", "tp-200", "crickent"}}}
	view := CalculateTypedForDisplay(snapshot, "zibbo", true)
	group := view.StockSummary.Groups["quest:fuel-shortage:flammable"]
	golden, fuel := targetByKeyForTest(t, view, "quest:golden-swag"), targetByKeyForTest(t, view, "quest:fuel-shortage")
	if !view.Known || view.RequiredQuantity != 3 || view.StockSummary.FixedStock.Regular != 0 ||
		group.AvailableQuantity != 1 || group.RequiredQuantity != 3 || group.AllocatedQuantity != 0 ||
		golden.AvailableQuantity != 0 || golden.RequiredQuantity != 1 || fuel.AvailableQuantity != 1 || fuel.RequiredQuantity != 3 ||
		!view.AddFIRAllowed || !view.AddRegularAllowed {
		t.Fatalf("Zibbo stock denominator, global Shared coverage and independent goals disagree: view=%+v group=%+v", view, group)
	}
	for _, details := range [][]StockContribution{group.OtherStock, fuel.OtherStock} {
		if len(details) != 1 || details[0].ItemID != "fireklean" || details[0].FIRQuantity != 1 || details[0].RegularQuantity != 0 {
			t.Fatalf("Zibbo borrowed full, unclassified or Collector-reserved FireKlean stock: %+v", details)
		}
	}
	if len(golden.OtherStock) != 0 || !reflect.DeepEqual(view.Groups, view.StockSummary.Groups) || view.FixedStock != view.StockSummary.FixedStock {
		t.Fatal("Stock details introduced a second global allocation or attributed another item to Golden Swag")
	}
}

func TestTypedOtherStockContainsOnlyAllocatedEligiblePieces(t *testing.T) {
	for _, test := range []struct {
		name                                                                     string
		fir                                                                      bool
		amount, ownFIR, ownRegular, otherFIR, otherRegular, wantFIR, wantRegular int64
	}{
		{name: "own-only", fir: true, amount: 2, ownFIR: 2},
		{name: "mixed", fir: true, amount: 3, ownFIR: 1, otherFIR: 5, wantFIR: 2},
		{name: "only-other", fir: true, amount: 3, otherFIR: 5, wantFIR: 3},
		{name: "regular-prefers-nonfir-and-uses-fir-surplus", amount: 3, otherFIR: 2, otherRegular: 2, wantFIR: 1, wantRegular: 2},
		{name: "nonfir-does-not-cover-fir", fir: true, amount: 3, otherRegular: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := contributionSnapshotForTest(test.fir, test.amount, test.ownFIR, test.ownRegular, test.otherFIR, test.otherRegular)
			view := CalculateTypedForDisplay(snapshot, "selected", true)
			if !view.Known {
				t.Fatalf("valid typed allocation unavailable: %+v", view)
			}
			assertOtherStockForTest(t, view.StockSummary.Groups["quest:shared:any"].OtherStock, test.wantFIR, test.wantRegular)
			assertOtherStockForTest(t, targetByKeyForTest(t, view, "quest:shared").OtherStock, test.wantFIR, test.wantRegular)
		})
	}
}

func TestTypedTargetOtherStockExcludesUnrelatedIngredientsOfSameQuest(t *testing.T) {
	snapshot := contributionSnapshotForTest(true, 2, 0, 0, 3, 0)
	snapshot.Items = append(snapshot.Items, typedItem("unrelated", 8, 0, 0, fixedSource("shared", 5, true)))
	view := CalculateTypedForDisplay(snapshot, "selected", true)
	target := targetByKeyForTest(t, view, "quest:shared")
	assertOtherStockForTest(t, target.OtherStock, 2, 0)
	if target.AvailableQuantity != 2 || target.RequiredQuantity != 2 || target.FIRQuantity != 2 {
		t.Fatalf("another ingredient leaked into the selected item's quest ratio: %+v", target)
	}
}

func TestTypedOtherStockExcludesEverySelectedIDVariant(t *testing.T) {
	snapshot := contributionSnapshotForTest(true, 3, 0, 0, 1, 0)
	snapshot.Items = append(snapshot.Items, typedItem("selected", 2, 0, 0, sharedSource("shared", "any", true)))
	snapshot.Items[2].Name = "Sibling variant"
	view := CalculateTypedRowForDisplay(snapshot, 0, true)
	if !view.Known || view.AddFIRAllowed || view.AddRegularAllowed {
		t.Fatalf("same-ID variant acquired a writable API target: %+v", view)
	}
	group, target := view.StockSummary.Groups["quest:shared:any"], targetByKeyForTest(t, view, "quest:shared")
	if group.AvailableQuantity != 3 || target.AvailableQuantity != 3 {
		t.Fatalf("excluding sibling details lost valid physical coverage: group=%+v target=%+v", group, target)
	}
	assertOtherStockForTest(t, group.OtherStock, 1, 0)
	assertOtherStockForTest(t, target.OtherStock, 1, 0)
}

func TestTypedOtherStockDoesNotInventUnknownOrHistoricalContributions(t *testing.T) {
	for _, state := range []string{"unknown", "completed", "canceled", "optional"} {
		t.Run(state, func(t *testing.T) {
			snapshot := contributionSnapshotForTest(true, 3, 0, 0, 3, 0)
			switch state {
			case "unknown":
				snapshot.Items[1].QualityKnown = false
			case "completed":
				snapshot.MarketGroups[0].Completed = true
				snapshot.MarketQuests[0].Status, snapshot.MarketQuests[0].Completed = "completed", true
				for i := range snapshot.Items {
					snapshot.Items[i].Sources[0].Completed = true
				}
			case "canceled":
				snapshot.MarketGroups[0].Canceled = true
			case "optional":
				snapshot.MarketGroups[0].Optional = true
			}
			view := CalculateTypedForDisplay(snapshot, "selected", true)
			if state == "unknown" && view.Known {
				t.Fatal("unknown physical quality was accepted")
			}
			for _, target := range view.Forecast.Targets {
				if len(target.OtherStock) != 0 || target.QualityKnown {
					t.Fatalf("excluded or historical goal acquired current other-item quality: %+v", target)
				}
			}
			for _, group := range view.Groups {
				if len(group.OtherStock) != 0 {
					t.Fatalf("excluded group acquired another item's allocation: %+v", group)
				}
			}
		})
	}
}

func TestTypedOtherStockSanitizesIconsAndOwnsOutputSlices(t *testing.T) {
	snapshot := contributionSnapshotForTest(false, 3, 0, 0, 1, 2)
	snapshot.Items[1].Icon = "javascript:alert(1)"
	before, _ := json.Marshal(snapshot)
	view := CalculateTypedForDisplay(snapshot, "selected", true)
	group := view.StockSummary.Groups["quest:shared:any"]
	if len(group.OtherStock) != 1 || group.OtherStock[0].Icon != "" {
		t.Fatalf("unsafe icon reached the DTO: %+v", group.OtherStock)
	}
	group.OtherStock[0].Name = "Changed summary"
	view.Forecast.Targets[0].OtherStock[0].FIRQuantity = 99
	if view.Groups["quest:shared:any"].OtherStock[0].Name == "Changed summary" {
		t.Fatal("independent display results share mutable contribution storage")
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("display contribution generation or output edits mutated input inventory")
	}
	again := CalculateTypedForDisplay(snapshot, "selected", true)
	if again.Forecast.Targets[0].OtherStock[0].FIRQuantity != 1 {
		t.Fatal("a repeated display inherited a mutated contribution")
	}
	legacy := Calculate(snapshot, "selected")
	for _, target := range legacy.Targets {
		if len(target.OtherStock) != 0 || target.QualityKnown || target.FIRQuantity != 0 || target.RegularQuantity != 0 {
			t.Fatalf("legacy untyped display fabricated physical quality: %+v", target)
		}
	}
}
