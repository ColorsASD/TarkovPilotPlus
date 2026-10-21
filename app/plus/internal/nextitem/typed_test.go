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

func TestTypedEarlierRegularGoalUsesFIRBeforeLaterFIRRequirements(t *testing.T) {
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
			wantFIR, wantRegular := min(max(0, fir-1), 2), int64(1)
			if !got.Known || len(got.Forecast.Targets) != 2 || got.Forecast.Targets[0].AvailableQuantity != wantRegular ||
				got.Forecast.Targets[1].AvailableQuantity != wantFIR || got.AddFIRAllowed != (fir < 3) ||
				got.AddRegularAllowed != (fir < 3) {
				t.Fatalf("later FiR fixed/Shared need overrode the earlier Regular goal: fir=%d shared=%v got=%+v targets=%+v", fir, shared, got, got.Forecast.Targets)
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

func TestTypedPhysicalVariantRowsKeepTheirOwnQualityAndFixedTargets(t *testing.T) {
	for _, sameName := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{
				{QuestID: "fir-variant", Name: "FiR variant goal", Status: "available"},
				{QuestID: "regular-variant", Name: "Regular variant goal", Status: "available"},
			}
			firRow := typedItem("same-item-id", 2, 0, 0, fixedSource("fir-variant", 3, true))
			regularRow := typedItem("same-item-id", 0, 1, 0, fixedSource("regular-variant", 2, false))
			firRow.Name, regularRow.Name = "MF-UNTAR FiR variant", "MF-UNTAR Regular variant"
			if sameName {
				firRow.Name, regularRow.Name = "MF-UNTAR", "MF-UNTAR"
			}
			snapshot.Items = []localprogress.Item{firRow, regularRow}
			if reverse {
				snapshot.Items[0], snapshot.Items[1] = snapshot.Items[1], snapshot.Items[0]
			}
			before, _ := json.Marshal(snapshot)
			for index, row := range snapshot.Items {
				view := CalculateTypedRow(snapshot, index, true)
				wantStock := localprogress.RequirementStock{Known: true, FIR: row.FIRQuantity, Regular: row.RegularQuantity}
				wantAmount := int64(2)
				if row.Sources[0].FIR {
					wantAmount = 3
				}
				if !view.Known || view.FixedStock != wantStock || view.AddFIRAllowed || view.AddRegularAllowed ||
					len(view.Forecast.Targets) != 1 || view.Forecast.Targets[0].GoalKey != "quest:"+row.Sources[0].QuestID ||
					view.Forecast.Targets[0].AvailableQuantity != row.AvailableQuantity || view.Forecast.Targets[0].RequiredQuantity != wantAmount {
					t.Fatalf("physical same-ID variant borrowed another row's stock or targets: sameName=%v reverse=%v index=%d view=%+v targets=%+v", sameName, reverse, index, view, view.Forecast.Targets)
				}
			}
			if view := CalculateTyped(snapshot, "same-item-id", true); view.Known || view.AddFIRAllowed || view.AddRegularAllowed {
				t.Fatalf("item-ID wrapper accepted an ambiguous API stock target: %+v", view)
			}
			after, _ := json.Marshal(snapshot)
			if string(before) != string(after) {
				t.Fatal("physical variant calculations mutated the input")
			}
		}
	}
}

func TestTypedPhysicalVariantSharedMembershipUsesItsOwnSources(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		snapshot := forecastSnapshot()
		snapshot.MarketQuests = []market.Quest{
			{QuestID: "shared", Name: "FiR shared goal", Status: "available"},
			{QuestID: "regular", Name: "Regular fixed goal", Status: "available"},
		}
		snapshot.Items = []localprogress.Item{
			typedItem("same-item-id", 2, 0, 0, sharedSource("shared", "any", true)),
			typedItem("same-item-id", 0, 1, 0, fixedSource("regular", 2, false)),
		}
		snapshot.Items[0].Name, snapshot.Items[1].Name = "Same name", "Same name"
		snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", FIR: true,
			Amount: 3, RequiredQuantity: 3, AvailableQuantity: 3, ItemIDs: []string{"same-item-id"}}}
		if reverse {
			snapshot.Items[0], snapshot.Items[1] = snapshot.Items[1], snapshot.Items[0]
		}
		for index, row := range snapshot.Items {
			view := CalculateTypedRow(snapshot, index, true)
			if !view.Known || len(view.Forecast.Targets) != 1 || view.AddFIRAllowed || view.AddRegularAllowed {
				t.Fatalf("variant-row shared view is unavailable or contains extra targets: %+v", view)
			}
			if row.Sources[0].FIR {
				group := view.Groups["quest:shared:any"]
				if group.AvailableQuantity != 2 || group.AllocatedQuantity != 2 || group.Contributions["same-item-id"] != 2 ||
					view.FixedStock.FIR != 0 || view.FixedStock.Regular != 0 || view.Forecast.Targets[0].AvailableQuantity != 2 {
					t.Fatalf("shared physical variant mixed in the fixed variant or API counter: %+v", view)
				}
			} else if len(view.Groups) != 0 || view.FixedStock.Regular != 1 || view.Forecast.Targets[0].GoalKey != "quest:regular" ||
				view.Forecast.Targets[0].AvailableQuantity != 1 {
				t.Fatalf("fixed physical variant inherited shared membership from its duplicate ID: %+v", view)
			}
		}
	}
}

func TestTypedPhysicalRowIndexRequiresAnExistingTrackedRecord(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.Items = []localprogress.Item{typedItem("item", 0, 0, 0)}
	for _, index := range []int{-1, 1} {
		if view := CalculateTypedRow(snapshot, index, true); view.Known || view.AddFIRAllowed || view.AddRegularAllowed {
			t.Fatalf("invalid physical row was accepted: index=%d view=%+v", index, view)
		}
	}
	snapshot.Items[0].MarketTracked = false
	if view := CalculateTypedRow(snapshot, 0, true); view.Known || view.AddFIRAllowed || view.AddRegularAllowed {
		t.Fatalf("untracked physical row was accepted: %+v", view)
	}
}

func TestTypedRequiredQuantityUsesLocalSharedCoverageAndKeepsOwnAllocation(t *testing.T) {
	for _, test := range []struct {
		name                         string
		own, other, fixed, apiShared int64
		wantRequired                 int64
		wantAdd                      bool
	}{
		{"own two and other six complete eight", 2, 6, 0, 8, 2, false},
		{"API completed but local stock unclassified", 0, 0, 0, 8, 8, true},
		{"own two is not subtracted twice", 2, 0, 0, 0, 8, true},
		{"fixed demand and remaining shared demand", 2, 3, 4, 8, 9, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{
				{QuestID: "shared", Name: "Shared", Status: "available"},
				{QuestID: "fixed", Name: "Fixed", Status: "locked"},
			}
			sources := []localprogress.Source{sharedSource("shared", "any", false)}
			if test.fixed > 0 {
				sources = append(sources, fixedSource("fixed", test.fixed, false))
			}
			snapshot.Items = []localprogress.Item{
				typedItem("selected", 0, test.own, 0, sources...),
				typedItem("other", 0, test.other, 0, sharedSource("shared", "any", false)),
			}
			if test.apiShared > 0 && test.own == 0 {
				// A teljes API-készlet ismert, de a minősége még nincs besorolva.
				snapshot.Items[0].AvailableQuantity, snapshot.Items[0].MarketAvailableQuantity = test.apiShared, test.apiShared
			}
			snapshot.Items[0].CollectionCalculated = true
			snapshot.Items[0].CollectionRequiredQuantity = 0
			snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: 8,
				RequiredQuantity: 8, AvailableQuantity: test.apiShared, ItemIDs: []string{"selected", "other"}}}
			view := CalculateTyped(snapshot, "selected", true)
			if !view.Known || view.RequiredQuantity != test.wantRequired || view.AddRegularAllowed != test.wantAdd || view.AddFIRAllowed != test.wantAdd {
				t.Fatalf("typed collection quota used API counters or subtracted own stock twice: %+v", view)
			}
		})
	}
}

func TestTypedRequiredQuantityIncludesBothFixedQualitiesAndCapsTheTotal(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "fir", Name: "FiR", Status: "available"},
		{QuestID: "regular", Name: "Regular", Status: "available"},
	}
	snapshot.Items = []localprogress.Item{typedItem("vodka", 0, 1, 0,
		fixedSource("fir", 10, true), fixedSource("regular", 4, false))}
	if view := CalculateTyped(snapshot, "vodka", true); !view.Known || view.RequiredQuantity != 14 {
		t.Fatalf("fixed quality requirements lost the full vodka denominator: %+v", view)
	}
	snapshot.Items[0].Sources = []localprogress.Source{
		fixedSource("fir", localprogress.MaxQuantity, true), fixedSource("regular", localprogress.MaxQuantity, false),
	}
	if view := CalculateTyped(snapshot, "vodka", true); !view.Known || view.RequiredQuantity != localprogress.MaxQuantity {
		t.Fatalf("typed collection quota exceeded the supported quantity limit: %+v", view)
	}
}
