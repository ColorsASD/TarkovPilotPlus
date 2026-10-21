package nextitem

import (
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestTypedComTacHotDeliveryUsesEligibleFIRBeforeLaterScavenger(t *testing.T) {
	for fir := int64(0); fir <= 5; fir++ {
		for regular := int64(0); regular <= 3; regular++ {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{
				{QuestID: "hot-delivery", Name: "Hot Delivery", Status: "available"},
				{QuestID: "scavenger", Name: "Scavenger", Status: "available"},
			}
			snapshot.Items = []localprogress.Item{typedItem("comtac-ii", fir, regular, 0,
				fixedSource("hot-delivery", 2, false), sharedSource("scavenger", "headsets", true))}
			snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "scavenger", RequirementID: "headsets", FIR: true,
				Amount: 15, RequiredQuantity: 15, AvailableQuantity: 15, ItemIDs: []string{"comtac-ii"}}}
			view := CalculateTyped(snapshot, "comtac-ii", true)
			wantRegular := min(regular+fir, 2)
			wantShared := min(max(0, fir-max(0, 2-regular)), 15)
			group := view.Groups["quest:scavenger:headsets"]
			if !view.Known || view.FixedStock.Regular != wantRegular || view.FixedStock.FIR != 0 || group.AvailableQuantity != wantShared || group.AllocatedQuantity != wantShared {
				t.Fatalf("goal order or quality eligibility changed: FiR=%d Regular=%d view=%+v group=%+v", fir, regular, view, group)
			}
			if len(view.Forecast.Targets) != 2 || view.Forecast.Targets[0].GoalKey != "quest:hot-delivery" ||
				view.Forecast.Targets[0].AvailableQuantity != wantRegular || view.Forecast.Targets[0].RequiredQuantity != 2 ||
				view.Forecast.Targets[1].GoalKey != "quest:scavenger" || view.Forecast.Targets[1].AvailableQuantity != wantShared ||
				view.Forecast.Targets[1].RequiredQuantity != 15 || wantRegular+wantShared > regular+fir {
				t.Fatalf("summary and goal ratios reused stock or disagreed: FiR=%d Regular=%d stock=%+v group=%+v targets=%+v", fir, regular, view.FixedStock, group, view.Forecast.Targets)
			}
		}
	}
}

func TestTypedMixedQualitiesPreserveExistingQuestAndHideoutPriority(t *testing.T) {
	for _, test := range []struct {
		name, regularStatus, firStatus string
		regularAmount, firAmount       int64
		regularModule, firModule       bool
		wantRegular                    bool
	}{
		{"available Regular before locked FiR", "available", "locked", 2, 1, false, false, true},
		{"available FiR before locked Regular", "locked", "available", 1, 2, false, false, false},
		{"smaller Regular goal first", "available", "available", 1, 2, false, false, true},
		{"smaller FiR goal first", "available", "available", 2, 1, false, false, false},
		{"stable equal-goal metadata tie", "available", "available", 1, 1, false, false, true},
		{"Regular hideout before equal quest", "available", "available", 1, 1, true, false, true},
		{"FiR hideout before equal quest", "available", "available", 1, 1, false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{{QuestID: "regular", Name: "Regular goal", Status: test.regularStatus}, {QuestID: "fir", Name: "FiR goal", Status: test.firStatus}}
			regularSource, firSource := fixedSource("regular", test.regularAmount, false), fixedSource("fir", test.firAmount, true)
			if test.regularModule {
				regularSource.Type, regularSource.QuestID, regularSource.UpgradeKey = "hideout", "", "Regular_1"
				snapshot.MarketHideout = append(snapshot.MarketHideout, market.HideoutUpgrade{UpgradeKey: "Regular_1", Name: "Regular module", Level: 1, CanComplete: true})
			}
			if test.firModule {
				firSource.Type, firSource.QuestID, firSource.UpgradeKey = "hideout", "", "FIR_1"
				snapshot.MarketHideout = append(snapshot.MarketHideout, market.HideoutUpgrade{UpgradeKey: "FIR_1", Name: "FiR module", Level: 1, CanComplete: true})
			}
			snapshot.Items = []localprogress.Item{typedItem("selected", 1, 0, 0, regularSource, firSource)}
			view := CalculateTyped(snapshot, "selected", true)
			wantRegular := int64(0)
			if test.wantRegular {
				wantRegular = 1
			}
			if !view.Known || view.FixedStock.Regular != wantRegular || view.FixedStock.FIR != 1-wantRegular || len(view.Forecast.Targets) != 2 {
				t.Fatalf("quality flag overrode the original goal priority: stock=%+v targets=%+v", view.FixedStock, view.Forecast.Targets)
			}
			var allocated int64
			for _, target := range view.Forecast.Targets {
				allocated += target.AvailableQuantity
			}
			if allocated != 1 {
				t.Fatalf("one FiR piece was allocated to multiple goals: %+v", view.Forecast.Targets)
			}
		})
	}
}

func TestTypedHideoutFIRRequirementRejectsRegularAndRegularAcceptsFIR(t *testing.T) {
	for _, test := range []struct {
		name               string
		needFIR            bool
		fir, regular, want int64
		wantRegularAdd     bool
	}{
		{"FiR module rejects nonFiR", true, 0, 3, 0, false},
		{"FiR module uses only FiR", true, 1, 3, 1, false},
		{"Regular module accepts both qualities", false, 2, 1, 3, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketHideout = []market.HideoutUpgrade{{UpgradeKey: "Module_1", Name: "Module", Level: 1, CanComplete: true}}
			source := localprogress.Source{Origin: localprogress.OriginMarket, TrackedSource: market.TrackedSource{Type: "hideout", UpgradeKey: "Module_1", Amount: 4, RemainingRequiredQuantity: 4, FIR: test.needFIR}}
			snapshot.Items = []localprogress.Item{typedItem("selected", test.fir, test.regular, 0, source)}
			view := CalculateTyped(snapshot, "selected", true)
			if !view.Known || len(view.Forecast.Targets) != 1 || view.Forecast.Targets[0].AvailableQuantity != test.want || view.Forecast.Targets[0].RequiredQuantity != 4 || !view.AddFIRAllowed || view.AddRegularAllowed != test.wantRegularAdd {
				t.Fatalf("hideout quality eligibility or ratio is wrong: %+v targets=%+v", view, view.Forecast.Targets)
			}
		})
	}
}

func TestTypedResidualFlowCannotTakeStockAwayFromEarlierRegularNeed(t *testing.T) {
	for _, laterShared := range []bool{false, true} {
		needs := []need{
			{key: "earlier-Regular", rows: []int{0, 1}, amount: 2, typed: true},
			{key: "later-FiR", rows: []int{1}, amount: 15, typed: true, fir: true, shared: laterShared},
		}
		allocations := engineAllocateNeeds(needs, []int64{1, 1})
		if allocations[0][0] != 1 || allocations[0][1] != 1 || engineAllocationTotal(allocations[0]) != 2 || engineAllocationTotal(allocations[1]) != 0 {
			t.Fatalf("a later FiR sink took stock away from the earlier Regular goal: shared=%v allocations=%+v", laterShared, allocations)
		}
		// A később hozzáadott nonFiR felszabadíthat FiR készletet, de a
		// korábbi cél 2/2 aránya ekkor sem csökkenhet egyetlen darabbal sem.
		allocations = engineAllocateNeeds(needs, []int64{2, 1})
		if allocations[0][0] != 2 || allocations[0][1] != 0 || allocations[1][1] != 1 || engineAllocationTotal(allocations[0]) != 2 {
			t.Fatalf("compatible nonFiR replacement reduced earlier progress or duplicated FiR: shared=%v allocations=%+v", laterShared, allocations)
		}
	}
}
