package nextitem

import (
	"encoding/json"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func typedItem(id string, fir, regular, uncategorized int64, sources ...localprogress.Source) localprogress.Item {
	row := forecastItem(id, fir+regular+uncategorized, sources...)
	row.QualityKnown, row.FIRQuantity, row.RegularQuantity = true, fir, regular
	return row
}

func TestTypedStockUsesRecordedVodkaQuality(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "booze", Name: "Booze", Status: "locked"},
		{QuestID: "bitter-victory", Name: "A Bitter Victory", Status: "locked"},
	}
	snapshot.Items = []localprogress.Item{typedItem("vodka", 0, 1, 0,
		fixedSource("booze", 10, true), fixedSource("bitter-victory", 4, false))}
	got := CalculateTyped(snapshot, "vodka", true)
	if !got.Known || got.FixedStock != (localprogress.RequirementStock{Known: true, Regular: 1}) ||
		!got.AddFIRAllowed || !got.AddRegularAllowed {
		t.Fatalf("recorded Regular vodka was not allocated as Regular: %+v", got)
	}
	if len(got.Forecast.Targets) != 2 || got.Forecast.Targets[0].AvailableQuantity != 0 ||
		got.Forecast.Targets[0].RequiredQuantity != 10 || got.Forecast.Targets[1].AvailableQuantity != 1 ||
		got.Forecast.Targets[1].RequiredQuantity != 4 {
		t.Fatalf("quest ratios diverged from typed stock: %+v", got.Forecast.Targets)
	}
}

func TestTypedNonFIRAndUncategorizedCannotCoverFIRRequirements(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "fixed", Name: "Fixed", Status: "available"},
		{QuestID: "shared", Name: "Shared", Status: "available"},
	}
	snapshot.Items = []localprogress.Item{
		typedItem("selected", 0, 5, 10, fixedSource("fixed", 2, true), sharedSource("shared", "any", true)),
		typedItem("other", 0, 4, 3, sharedSource("shared", "any", true)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", FIR: true,
		Amount: 2, RequiredQuantity: 2, AvailableQuantity: 2, ItemIDs: []string{"selected", "other"}}}
	got := CalculateTyped(snapshot, "selected", true)
	if !got.Known || got.FixedStock.FIR != 0 || got.FixedStock.Regular != 0 ||
		!got.AddFIRAllowed || got.AddRegularAllowed {
		t.Fatalf("nonFiR or uncategorized stock fulfilled a FiR requirement: %+v", got)
	}
	if group := got.Groups["quest:shared:any"]; group.AvailableQuantity != 0 || group.AllocatedQuantity != 0 {
		t.Fatalf("API Shared counter fabricated a typed FiR quantity: %+v", group)
	}
	for _, target := range got.Forecast.Targets {
		if target.AvailableQuantity != 0 || target.AllocationUnknown {
			t.Fatalf("unknown stock or API Shared progress entered a typed target: %+v", target)
		}
	}
}

func TestTypedFIRRequirementsReserveStockBeforeRegularGoals(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, fir := range []int64{1, 2, 3, 4} {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{
				{QuestID: "regular", Name: "Small Regular goal", Status: "available"},
				{QuestID: "fir", Name: "Larger FiR goal", Status: "locked"},
			}
			firSource := fixedSource("fir", 2, true)
			if shared {
				firSource = sharedSource("fir", "any", true)
				snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "fir", RequirementID: "any", FIR: true,
					Amount: 2, RequiredQuantity: 2, ItemIDs: []string{"selected"}}}
			}
			snapshot.Items = []localprogress.Item{typedItem("selected", fir, 0, 0,
				fixedSource("regular", 1, false), firSource)}
			got := CalculateTyped(snapshot, "selected", true)
			wantFIR, wantRegular := min(fir, 2), min(max(0, fir-2), 1)
			if !got.Known || len(got.Forecast.Targets) != 2 || got.Forecast.Targets[0].AvailableQuantity != wantRegular ||
				got.Forecast.Targets[1].AvailableQuantity != wantFIR || got.AddFIRAllowed != (fir < 3) ||
				got.AddRegularAllowed != (fir < 3) {
				t.Fatalf("FiR stock must be reserved for FiR fixed/Shared needs: fir=%d shared=%v got=%+v targets=%+v", fir, shared, got, got.Forecast.Targets)
			}
		}
	}
}

func TestTypedRegularNeedsPreferNonFIRAcrossSharedCandidates(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "shared", Name: "Regular group", Status: "available"}}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: 1,
		RequiredQuantity: 1, ItemIDs: []string{"fir-first", "regular-second"}}}
	snapshot.Items = []localprogress.Item{
		typedItem("fir-first", 1, 0, 0, sharedSource("shared", "any", false)),
		typedItem("regular-second", 0, 1, 0, sharedSource("shared", "any", false)),
	}
	got := CalculateTyped(snapshot, "fir-first", true)
	if !got.Known || got.Groups["quest:shared:any"].AvailableQuantity != 1 || got.Groups["quest:shared:any"].AllocatedQuantity != 0 {
		t.Fatalf("Regular group used FiR before an available nonFiR alternative: %+v", got.Groups)
	}
}

func TestTypedSharedAndFixedAllocationsCannotReuseTheSameStock(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "shared", Name: "Shared", Status: "available"},
		{QuestID: "fixed", Name: "Fixed", Status: "locked"},
	}
	snapshot.Items = []localprogress.Item{
		typedItem("selected", 1, 1, 0, sharedSource("shared", "any", true), fixedSource("fixed", 2, false)),
		typedItem("other", 1, 0, 0, sharedSource("shared", "any", true)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", FIR: true,
		Amount: 2, RequiredQuantity: 2, AvailableQuantity: 0, ItemIDs: []string{"selected", "other"}}}
	got := CalculateTyped(snapshot, "selected", true)
	group := got.Groups["quest:shared:any"]
	if !got.Known || group.AvailableQuantity != 2 || group.AllocatedQuantity != 1 || got.FixedStock.Regular != 1 ||
		got.FixedStock.FIR != 0 || !got.AddFIRAllowed || !got.AddRegularAllowed {
		t.Fatalf("Shared and fixed allocation lost quality or reused stock: %+v group=%+v", got, group)
	}
	var contributions int64
	for _, quantity := range group.Contributions {
		contributions += quantity
	}
	if group.Contributions["selected"] != 1 || group.Contributions["other"] != 1 || contributions != group.AvailableQuantity {
		t.Fatalf("shared contributions must preserve every candidate and reconcile to the group total: %+v", group)
	}
	if len(got.Forecast.Targets) != 2 || got.Forecast.Targets[0].AvailableQuantity != 2 || got.Forecast.Targets[1].AvailableQuantity != 1 {
		t.Fatalf("target progress did not follow the same single allocation: %+v", got.Forecast.Targets)
	}
}

func TestTypedAddEligibilityUsesUsefulQualityAndInventoryCap(t *testing.T) {
	for _, test := range []struct {
		name                  string
		fir, regular, unknown int64
		needFIR               bool
		wantFIR, wantRegular  bool
	}{
		{"FiR missing", 0, 3, 0, true, true, false},
		{"FiR complete", 2, 0, 0, true, false, false},
		{"Regular missing", 0, 1, 0, false, true, true},
		{"Regular complete", 0, 2, 0, false, false, false},
		{"unknown is not progress", 0, 0, 4, false, true, true},
		{"API stock cap", 0, 0, localprogress.MaxQuantity, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{{QuestID: "goal", Status: "available"}}
			snapshot.Items = []localprogress.Item{typedItem("selected", test.fir, test.regular, test.unknown, fixedSource("goal", 2, test.needFIR))}
			got := CalculateTyped(snapshot, "selected", true)
			if !got.Known || got.AddFIRAllowed != test.wantFIR || got.AddRegularAllowed != test.wantRegular {
				t.Fatalf("wrong quality add eligibility: %+v", got)
			}
		})
	}
}

func TestTypedStockCannotExceedTotalAndCalculationDoesNotMutateInput(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "goal", Status: "available"}}
	snapshot.Items = []localprogress.Item{typedItem("selected", 1, 1, 1, fixedSource("goal", 5, true))}
	before, _ := json.Marshal(snapshot)
	got := CalculateTyped(snapshot, "selected", true)
	after, _ := json.Marshal(snapshot)
	if !got.Known || string(before) != string(after) {
		t.Fatal("typed calculation rejected valid unknown stock or mutated the input")
	}
	for _, change := range []func(*localprogress.Item){
		func(item *localprogress.Item) { item.QualityKnown = false },
		func(item *localprogress.Item) { item.FIRQuantity = -1 },
		func(item *localprogress.Item) { item.RegularQuantity = localprogress.MaxQuantity },
	} {
		invalid := localprogress.Clone(snapshot)
		change(&invalid.Items[0])
		if view := CalculateTyped(invalid, "selected", true); view.Known || view.AddFIRAllowed || view.AddRegularAllowed {
			t.Fatalf("unsupported quality was accepted: %+v", view)
		}
	}
}

func TestTypedCompletedSourcesRemainVisibleWithoutConsumingStock(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "done", Name: "Completed goal", Status: "completed", Completed: true}}
	source := fixedSource("done", 3, true)
	source.Completed, source.RemainingRequiredQuantity = true, 0
	snapshot.Items = []localprogress.Item{typedItem("selected", 0, 0, 0, source)}
	got := CalculateTyped(snapshot, "selected", true)
	if !got.Known || got.AddFIRAllowed || got.AddRegularAllowed || len(got.Forecast.Targets) != 1 ||
		got.Forecast.Targets[0].AvailableQuantity != 3 || got.Forecast.Targets[0].RequiredQuantity != 3 ||
		!got.Forecast.Targets[0].Completed || got.FixedStock.FIR != 0 {
		t.Fatalf("completed requirement must keep its truthful completed ratio: %+v targets=%+v", got, got.Forecast.Targets)
	}
}

func TestTypedSameQualityKeepsExistingGoalPriority(t *testing.T) {
	for _, fir := range []bool{false, true} {
		for _, test := range []struct {
			name, firstStatus, secondStatus string
			firstAmount, secondAmount       int64
			wantFirst                       bool
		}{
			{"available before locked", "available", "locked", 3, 1, true},
			{"locked after available", "locked", "available", 1, 3, false},
			{"smaller requirement first", "available", "available", 3, 2, false},
			{"stable metadata tie", "available", "available", 2, 2, true},
		} {
			t.Run(test.name+"/fir="+map[bool]string{false: "false", true: "true"}[fir], func(t *testing.T) {
				snapshot := forecastSnapshot()
				snapshot.MarketQuests = []market.Quest{
					{QuestID: "first", Name: "First goal", Status: test.firstStatus},
					{QuestID: "second", Name: "Second goal", Status: test.secondStatus},
				}
				row := typedItem("selected", 0, 1, 0, fixedSource("first", test.firstAmount, fir), fixedSource("second", test.secondAmount, fir))
				if fir {
					row.FIRQuantity, row.RegularQuantity = 1, 0
				}
				snapshot.Items = []localprogress.Item{row}
				view := CalculateTyped(snapshot, "selected", true)
				if !view.Known || len(view.Forecast.Targets) != 2 {
					t.Fatalf("priority calculation lost a goal: %+v", view)
				}
				first, second := view.Forecast.Targets[0], view.Forecast.Targets[1]
				wantFirst := int64(0)
				if test.wantFirst {
					wantFirst = 1
				}
				if first.GoalKey != "quest:first" || second.GoalKey != "quest:second" || first.AvailableQuantity != wantFirst ||
					second.AvailableQuantity != 1-wantFirst || first.RequiredQuantity != test.firstAmount || second.RequiredQuantity != test.secondAmount ||
					view.FixedStock.FIR+view.FixedStock.Regular != 1 {
					t.Fatalf("same-quality goal priority changed or one piece was counted twice: stock=%+v targets=%+v", view.FixedStock, view.Forecast.Targets)
				}
			})
		}
	}
}
