package nextitem

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestTypedMedsSummaryReservesFIRAndReportsOnlyActualRegularFIRSurplus(t *testing.T) {
	for _, test := range []struct {
		name                                               string
		fir, regular, wantFIR, wantRegular, wantRegularFIR int64
		allowFIR                                           bool
	}{
		{name: "single-fir", fir: 1, wantFIR: 1, allowFIR: true},
		{name: "fir-surplus", fir: 21, wantFIR: 20, wantRegular: 1, wantRegularFIR: 1, allowFIR: true},
		{name: "nonfir-for-regular", fir: 20, regular: 1, wantFIR: 20, wantRegular: 1, allowFIR: true},
		{name: "prefer-nonfir-leave-fir-surplus", fir: 21, regular: 1, wantFIR: 20, wantRegular: 1, allowFIR: true},
		{name: "policy-disallows-fir-surplus", fir: 21, wantFIR: 20},
		{name: "policy-allows-nonfir", fir: 20, regular: 1, wantFIR: 20, wantRegular: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{
				{QuestID: "crisis", Name: "Crisis", Status: "locked"},
				{QuestID: "regular", Name: "Regular goal", Status: "available"},
			}
			snapshot.Items = []localprogress.Item{typedItem("meds", test.fir, test.regular, 0,
				fixedSource("crisis", 20, true), fixedSource("regular", 1, false))}
			input, _ := json.Marshal(snapshot)
			gate := CalculateTyped(snapshot, "meds", test.allowFIR)
			view := CalculateTypedForDisplay(snapshot, "meds", test.allowFIR)
			summary := view.StockSummary
			if !view.Known || summary == nil || summary.FixedStock.FIR != test.wantFIR || summary.FixedStock.Regular != test.wantRegular ||
				summary.RegularFIRQuantity != test.wantRegularFIR || view.RequiredQuantity != 21 {
				t.Fatalf("meds summary lost FiR reservation or fabricated Regular FiR: view=%+v summary=%+v", view, summary)
			}
			wantRegularGoalFIR := int64(0)
			if test.allowFIR {
				wantRegularGoalFIR = min(test.fir, max(0, 1-test.regular))
			}
			wantTargets := []Target{
				{GoalKey: "quest:crisis", GoalName: "Crisis", Kind: "quest", AvailableQuantity: min(test.fir, 20), RequiredQuantity: 20,
					FIRQuantity: min(test.fir, 20), QualityKnown: true},
				{GoalKey: "quest:regular", GoalName: "Regular goal", Kind: "quest", AvailableQuantity: min(test.regular, 1) + wantRegularGoalFIR, RequiredQuantity: 1,
					FIRQuantity: wantRegularGoalFIR, RegularQuantity: min(test.regular, 1), QualityKnown: true},
			}
			if !reflect.DeepEqual(view.Forecast.Targets, wantTargets) {
				t.Fatalf("Stock reservation changed the full independent target DTO: got=%+v want=%+v", view.Forecast.Targets, wantTargets)
			}
			view.StockSummary, view.Forecast.Targets, gate.Forecast.Targets = nil, nil, nil
			if !reflect.DeepEqual(gate, view) {
				t.Fatalf("display projection changed baseline stock, collection quota or Add gates: gate=%+v display=%+v", gate, view)
			}
			after, _ := json.Marshal(snapshot)
			if string(input) != string(after) {
				t.Fatal("Stock-only projection mutated the saved snapshot")
			}
		})
	}
}

func TestTypedSummaryFIRFixedAndSharedKeepOriginalGoalPriority(t *testing.T) {
	for _, test := range []struct {
		name                      string
		sharedStatus, fixedStatus string
		mixedRegular              bool
		wantShared, wantFixed     int64
	}{
		{name: "near-shared-before-future-fixed", sharedStatus: "available", fixedStatus: "locked", wantShared: 1},
		{name: "near-fixed-before-future-shared", sharedStatus: "locked", fixedStatus: "available", wantFixed: 1},
		{name: "mixed-goal-keeps-full-goal-priority", sharedStatus: "available", fixedStatus: "available", mixedRegular: true, wantShared: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{{QuestID: "fixed", Status: test.fixedStatus}, {QuestID: "shared", Status: test.sharedStatus}}
			fixedAmount := int64(20)
			if test.mixedRegular {
				fixedAmount = 1
			}
			snapshot.Items = []localprogress.Item{typedItem("selected", 1, 0, 0, fixedSource("fixed", fixedAmount, true), sharedSource("shared", "any", true))}
			if test.mixedRegular {
				snapshot.Items = append(snapshot.Items, typedItem("unrelated-regular", 0, 0, 0, fixedSource("fixed", 100, false)))
			}
			snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: 2, RequiredQuantity: 2, FIR: true, ItemIDs: []string{"selected"}}}
			view := CalculateTypedForDisplay(snapshot, "selected", true)
			summary := view.StockSummary
			if !view.Known || summary == nil || summary.FixedStock.FIR != test.wantFixed ||
				summary.Groups["quest:shared:any"].AvailableQuantity != test.wantShared || summary.RegularFIRQuantity != 0 {
				t.Fatalf("FiR tier reordered fixed/shared goals or used filtered goal totals: %+v", summary)
			}
		})
	}
}

func TestTypedSummaryOtherStockUsesDisplayReservationInsteadOfBaseline(t *testing.T) {
	snapshot := contributionSnapshotForTest(true, 2, 0, 0, 1, 0)
	snapshot.MarketQuests = append([]market.Quest{{QuestID: "earlier-regular", Status: "available"}}, snapshot.MarketQuests...)
	snapshot.MarketQuests[1].Status = "locked"
	snapshot.Items[1].Sources = append(snapshot.Items[1].Sources, fixedSource("earlier-regular", 1, false))
	view := CalculateTypedForDisplay(snapshot, "selected", true)
	baseline, summary := view.Groups["quest:shared:any"], view.StockSummary.Groups["quest:shared:any"]
	if baseline.AvailableQuantity != 0 || len(baseline.OtherStock) != 0 || summary.AvailableQuantity != 1 || view.RequiredQuantity != 2 {
		t.Fatalf("display reservation overwrote baseline or failed to protect another item's FiR: baseline=%+v summary=%+v", baseline, summary)
	}
	assertOtherStockForTest(t, summary.OtherStock, 1, 0)
	assertOtherStockForTest(t, targetByKeyForTest(t, view, "quest:shared").OtherStock, 1, 0)
}

func TestTypedSummaryRegularFIRMetadataKeepsPhysicalVariantsSeparate(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "regular-a", Status: "available"}, {QuestID: "fir-b", Status: "locked"}, {QuestID: "regular-b", Status: "locked"}}
	snapshot.Items = []localprogress.Item{
		typedItem("same-id", 0, 1, 0, fixedSource("regular-a", 1, false)),
		typedItem("same-id", 3, 0, 0, fixedSource("fir-b", 2, true), fixedSource("regular-b", 1, false)),
	}
	for index := range snapshot.Items {
		view := CalculateTypedRowForDisplay(snapshot, index, true)
		wantFIR, wantRegularFIR := int64(0), int64(0)
		if index == 1 {
			wantFIR, wantRegularFIR = 2, 1
		}
		if !view.Known || view.AddFIRAllowed || view.AddRegularAllowed || view.StockSummary.FixedStock.FIR != wantFIR ||
			view.StockSummary.FixedStock.Regular != 1 || view.StockSummary.RegularFIRQuantity != wantRegularFIR {
			t.Fatalf("variant summary borrowed a sibling's quality or API action: index=%d view=%+v", index, view)
		}
	}
}
