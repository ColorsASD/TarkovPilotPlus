package nextitem

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestTypedStockSummaryKeepsQualityCategoriesAndGoalForecastSeparate(t *testing.T) {
	for fir := int64(0); fir <= 18; fir++ {
		for regular := int64(0); regular <= 3; regular++ {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{
				{QuestID: "hot-delivery", Name: "Hot Delivery", Status: "available"},
				{QuestID: "scavenger", Name: "Scavenger", Status: "available"},
			}
			snapshot.Items = []localprogress.Item{typedItem("comtac-ii", fir, regular, 2,
				fixedSource("hot-delivery", 2, false), sharedSource("scavenger", "headsets", true))}
			snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "scavenger", RequirementID: "headsets", FIR: true,
				Amount: 15, RequiredQuantity: 15, AvailableQuantity: 15, ItemIDs: []string{"comtac-ii"}}}
			input, _ := json.Marshal(snapshot)
			gate := CalculateTyped(snapshot, "comtac-ii", true)
			view := CalculateTypedForDisplay(snapshot, "comtac-ii", true)
			if !view.Known || view.StockSummary == nil || gate.StockSummary != nil {
				t.Fatalf("the display projection is missing or was needlessly built for the action gate: %+v", view)
			}
			summary := view.StockSummary
			wantFIR, wantRegular := min(fir, 15), min(regular+max(0, fir-15), 2)
			group := summary.Groups["quest:scavenger:headsets"]
			if summary.FixedStock.FIR != 0 || summary.FixedStock.Regular != wantRegular || group.AvailableQuantity != wantFIR ||
				group.AllocatedQuantity != wantFIR || group.RequiredQuantity != 15 || group.Contributions["comtac-ii"] != wantFIR ||
				wantFIR+wantRegular > fir+regular {
				t.Fatalf("Stock category projection lost quality, surplus or reused stock: fir=%d regular=%d summary=%+v group=%+v", fir, regular, summary, group)
			}
			view.StockSummary = nil
			if !reflect.DeepEqual(gate, view) || view.RequiredQuantity != 17 {
				t.Fatalf("display summary changed goal forecast, collection quota or add eligibility: gate=%+v display=%+v", gate, view)
			}
			after, _ := json.Marshal(snapshot)
			if string(input) != string(after) {
				t.Fatal("display summary modified API stock or local quality input")
			}
		}
	}
}

func TestTypedStockSummaryUsesOneGlobalPoolAcrossFixedAndSharedAlternatives(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "regular-fixed", Name: "Earlier Regular", Status: "available"},
		{QuestID: "fir-fixed", Name: "FiR fixed", Status: "locked"},
		{QuestID: "fir-shared", Name: "FiR shared", Status: "locked"},
		{QuestID: "regular-shared", Name: "Regular shared", Status: "locked"},
	}
	snapshot.Items = []localprogress.Item{
		typedItem("selected", 1, 1, 4, fixedSource("regular-fixed", 2, false),
			sharedSource("fir-shared", "any", true), sharedSource("regular-shared", "any", false)),
		typedItem("other", 1, 1, 3, fixedSource("fir-fixed", 1, true),
			sharedSource("fir-shared", "any", true), sharedSource("regular-shared", "any", false)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{
		{QuestID: "fir-shared", RequirementID: "any", FIR: true, Amount: 2, RequiredQuantity: 2, AvailableQuantity: 2, ItemIDs: []string{"selected", "other"}},
		{QuestID: "regular-shared", RequirementID: "any", Amount: 2, RequiredQuantity: 2, AvailableQuantity: 2, ItemIDs: []string{"selected", "other"}},
	}
	selected := CalculateTypedForDisplay(snapshot, "selected", true)
	other := CalculateTypedForDisplay(snapshot, "other", true)
	if !selected.Known || !other.Known || selected.StockSummary == nil || other.StockSummary == nil {
		t.Fatalf("valid cross-item Stock projection unavailable: selected=%+v other=%+v", selected, other)
	}
	firGroup, regularGroup := selected.StockSummary.Groups["quest:fir-shared:any"], selected.StockSummary.Groups["quest:regular-shared:any"]
	if selected.StockSummary.FixedStock.Regular != 1 || selected.StockSummary.FixedStock.FIR != 0 ||
		other.StockSummary.FixedStock.FIR != 1 || other.StockSummary.FixedStock.Regular != 0 ||
		firGroup.AvailableQuantity != 1 || firGroup.Contributions["selected"] != 1 || firGroup.Contributions["other"] != 0 ||
		regularGroup.AvailableQuantity != 1 || regularGroup.Contributions["selected"] != 0 || regularGroup.Contributions["other"] != 1 {
		t.Fatalf("the same physical quality was reused between fixed/shared categories: selected=%+v other=%+v fir=%+v regular=%+v", selected.StockSummary, other.StockSummary, firGroup, regularGroup)
	}
	for _, group := range []TypedGroupStock{firGroup, regularGroup} {
		var total int64
		for _, quantity := range group.Contributions {
			total += quantity
		}
		if total != group.AvailableQuantity || group.RequiredQuantity != 2 {
			t.Fatalf("Shared alternative credit or its denominator was lost: %+v", group)
		}
	}
	allocated := selected.StockSummary.FixedStock.Regular + other.StockSummary.FixedStock.FIR + firGroup.AvailableQuantity + regularGroup.AvailableQuantity
	if allocated != 4 || firGroup.AllocatedQuantity != 1 || regularGroup.AllocatedQuantity != 0 {
		t.Fatalf("global or selected-row allocation counted an alternative twice: total=%d fir=%+v regular=%+v", allocated, firGroup, regularGroup)
	}
}

func TestTypedStockSummaryVariantRowsKeepTheirOwnSourcesAndQualities(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		snapshot := forecastSnapshot()
		snapshot.MarketQuests = []market.Quest{
			{QuestID: "regular-fixed", Status: "available"}, {QuestID: "fir-fixed", Status: "available"},
			{QuestID: "fir-shared", Status: "available"}, {QuestID: "regular-shared", Status: "available"},
		}
		snapshot.Items = []localprogress.Item{
			typedItem("same-id", 1, 1, 0, fixedSource("regular-fixed", 2, false), sharedSource("fir-shared", "any", true)),
			typedItem("same-id", 2, 1, 0, fixedSource("fir-fixed", 2, true), sharedSource("regular-shared", "any", false)),
		}
		snapshot.Items[0].Name, snapshot.Items[1].Name = "Regular variant", "FiR variant"
		snapshot.MarketGroups = []market.TrackedGroup{
			{QuestID: "fir-shared", RequirementID: "any", FIR: true, Amount: 3, RequiredQuantity: 3, ItemIDs: []string{"same-id"}},
			{QuestID: "regular-shared", RequirementID: "any", Amount: 2, RequiredQuantity: 2, ItemIDs: []string{"same-id"}},
		}
		if reverse {
			snapshot.Items[0], snapshot.Items[1] = snapshot.Items[1], snapshot.Items[0]
		}
		for index, row := range snapshot.Items {
			view := CalculateTypedRowForDisplay(snapshot, index, true)
			if !view.Known || view.StockSummary == nil || view.AddFIRAllowed || view.AddRegularAllowed || len(view.StockSummary.Groups) != 1 {
				t.Fatalf("variant Stock projection acquired another physical row or API actions: %+v", view)
			}
			if row.Name == "Regular variant" {
				group := view.StockSummary.Groups["quest:fir-shared:any"]
				if view.StockSummary.FixedStock.Regular != 1 || view.StockSummary.FixedStock.FIR != 0 || group.AvailableQuantity != 1 || group.AllocatedQuantity != 1 || group.Contributions["same-id"] != 1 {
					t.Fatalf("Regular variant borrowed FiR from its same-ID sibling: %+v", view.StockSummary)
				}
			} else {
				group := view.StockSummary.Groups["quest:regular-shared:any"]
				if view.StockSummary.FixedStock.FIR != 2 || view.StockSummary.FixedStock.Regular != 0 || group.AvailableQuantity != 1 || group.AllocatedQuantity != 1 || group.Contributions["same-id"] != 1 {
					t.Fatalf("FiR variant inherited its sibling's Shared membership: %+v", view.StockSummary)
				}
			}
		}
	}
}

func TestTypedStockSummaryRegularCategoryUsesOnlyPermittedFIRSurplus(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "regular", Status: "available"}, {QuestID: "fir", Status: "locked"}}
	snapshot.Items = []localprogress.Item{typedItem("selected", 3, 1, 0, fixedSource("regular", 3, false), fixedSource("fir", 2, true))}
	for _, allowFIR := range []bool{false, true} {
		view := CalculateTypedForDisplay(snapshot, "selected", allowFIR)
		wantRegular := int64(1)
		if allowFIR {
			wantRegular = 2
		}
		if !view.Known || view.StockSummary == nil || view.StockSummary.FixedStock.FIR != 2 || view.StockSummary.FixedStock.Regular != wantRegular {
			t.Fatalf("Regular Stock category did not respect FiR reservation or the explicit quality policy: allowFIR=%v summary=%+v", allowFIR, view.StockSummary)
		}
	}
}
