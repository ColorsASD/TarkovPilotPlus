package nextitem

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func targetByKeyForTest(t *testing.T, view TypedView, key string) Target {
	t.Helper()
	for _, target := range view.Forecast.Targets {
		if target.GoalKey == key {
			return target
		}
	}
	t.Fatalf("target %q is missing: %+v", key, view.Forecast.Targets)
	return Target{}
}

func assertTargetQualityForTest(t *testing.T, target Target, available, required, fir, regular int64) {
	t.Helper()
	if target.AvailableQuantity != available || target.RequiredQuantity != required || !target.QualityKnown ||
		target.FIRQuantity != fir || target.RegularQuantity != regular || fir+regular != available {
		t.Fatalf("independent target coverage or its physical quality breakdown is incorrect: %+v", target)
	}
}

func TestTypedIndependentComTacTargetsReuseStockAcrossGoalsAndPreferNewRegularStock(t *testing.T) {
	for _, regular := range []int64{1, 2} {
		snapshot := forecastSnapshot()
		snapshot.MarketQuests = []market.Quest{{QuestID: "hot-delivery", Name: "Hot Delivery", Status: "available"}, {QuestID: "scavenger", Name: "Scavenger", Status: "available"}}
		snapshot.Items = []localprogress.Item{typedItem("comtac", 1, regular, 0, fixedSource("hot-delivery", 2, false), sharedSource("scavenger", "headsets", true))}
		snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "scavenger", RequirementID: "headsets", FIR: true,
			Amount: 15, RequiredQuantity: 15, AvailableQuantity: 15, ItemIDs: []string{"comtac"}}}
		before, _ := json.Marshal(snapshot)
		gate := CalculateTyped(snapshot, "comtac", true)
		view := CalculateTypedForDisplay(snapshot, "comtac", true)
		assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:hot-delivery"), 2, 2, 2-regular, regular)
		assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:scavenger"), 1, 15, 1, 0)
		if view.RequiredQuantity != 17 || view.StockSummary == nil || view.StockSummary.FixedStock.Regular != regular ||
			view.StockSummary.Groups["quest:scavenger:headsets"].AvailableQuantity != 1 {
			t.Fatalf("independent target display changed Stock or its full collection denominator: %+v", view)
		}
		view.StockSummary = nil
		view.Forecast.Targets, gate.Forecast.Targets = nil, nil
		if !reflect.DeepEqual(gate, view) {
			t.Fatalf("display projection changed exclusive priority, Add gating or collection quota: gate=%+v display=%+v", gate, view)
		}
		after, _ := json.Marshal(snapshot)
		if string(before) != string(after) {
			t.Fatal("quality breakdown reclassified or changed the underlying inventory")
		}
	}
}

func TestTypedIndependentTargetsShareOnePoolWithinEachGoal(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "mixed", Status: "available"}, {QuestID: "separate", Status: "locked"}}
	snapshot.Items = []localprogress.Item{typedItem("selected", 1, 1, 4,
		sharedSource("mixed", "regular", false), sharedSource("mixed", "fir", true), sharedSource("separate", "fir", true))}
	snapshot.MarketGroups = []market.TrackedGroup{
		{QuestID: "mixed", RequirementID: "regular", Amount: 2, RequiredQuantity: 2, ItemIDs: []string{"selected"}},
		{QuestID: "mixed", RequirementID: "fir", FIR: true, Amount: 2, RequiredQuantity: 2, ItemIDs: []string{"selected"}},
		{QuestID: "separate", RequirementID: "fir", FIR: true, Amount: 3, RequiredQuantity: 3, ItemIDs: []string{"selected"}},
	}
	view := CalculateTypedForDisplay(snapshot, "selected", true)
	assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:mixed"), 2, 4, 1, 1)
	assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:separate"), 1, 3, 1, 0)
}

func TestTypedIndependentRegularBreakdownUsesNonFIRThroughResidualReassignment(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "same-goal", Status: "available"}}
	snapshot.Items = []localprogress.Item{
		typedItem("A", 0, 1, 0, sharedSource("same-goal", "first", false), sharedSource("same-goal", "second", false)),
		typedItem("B", 0, 1, 0, sharedSource("same-goal", "first", false)),
		typedItem("C", 1, 0, 0, sharedSource("same-goal", "second", false)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{
		{QuestID: "same-goal", RequirementID: "first", Amount: 1, RequiredQuantity: 1, ItemIDs: []string{"A", "B"}},
		{QuestID: "same-goal", RequirementID: "second", Amount: 1, RequiredQuantity: 1, ItemIDs: []string{"A", "C"}},
	}
	view := CalculateTypedForDisplay(snapshot, "A", true)
	assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:same-goal"), 2, 2, 0, 2)
}

func TestTypedIndependentTargetsUseAllSharedAlternativesAndRespectQualityPolicy(t *testing.T) {
	for _, allowFIR := range []bool{false, true} {
		snapshot := forecastSnapshot()
		snapshot.MarketQuests = []market.Quest{{QuestID: "fir", Status: "available"}, {QuestID: "regular", Status: "available"}}
		snapshot.Items = []localprogress.Item{
			typedItem("selected", 0, 0, 5, sharedSource("fir", "any", true), sharedSource("regular", "any", false)),
			typedItem("other", 2, 1, 3, sharedSource("fir", "any", true), sharedSource("regular", "any", false)),
		}
		snapshot.MarketGroups = []market.TrackedGroup{
			{QuestID: "fir", RequirementID: "any", FIR: true, Amount: 4, RequiredQuantity: 4, AvailableQuantity: 4, ItemIDs: []string{"selected", "other"}},
			{QuestID: "regular", RequirementID: "any", Amount: 4, RequiredQuantity: 4, AvailableQuantity: 4, ItemIDs: []string{"selected", "other"}},
		}
		view := CalculateTypedForDisplay(snapshot, "selected", allowFIR)
		assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:fir"), 2, 4, 2, 0)
		wantFIR := int64(0)
		if allowFIR {
			wantFIR = 2
		}
		assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:regular"), 1+wantFIR, 4, wantFIR, 1)
	}
}

func TestTypedIndependentHideoutAndVariantTargetsUseTheirOwnPhysicalRows(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		snapshot := forecastSnapshot()
		snapshot.MarketQuests = []market.Quest{{QuestID: "regular", Status: "available"}}
		snapshot.MarketHideout = []market.HideoutUpgrade{{UpgradeKey: "Module_2", Name: "Module", Level: 2, CanComplete: true}}
		hideout := localprogress.Source{Origin: localprogress.OriginMarket, TrackedSource: market.TrackedSource{
			Type: "hideout", UpgradeKey: "Module_2", Amount: 4, RemainingRequiredQuantity: 4, FIR: true}}
		snapshot.Items = []localprogress.Item{typedItem("same-id", 1, 1, 0, fixedSource("regular", 2, false)), typedItem("same-id", 3, 0, 0, hideout)}
		snapshot.Items[0].Name, snapshot.Items[1].Name = "Regular variant", "FiR variant"
		if reverse {
			snapshot.Items[0], snapshot.Items[1] = snapshot.Items[1], snapshot.Items[0]
		}
		for index, row := range snapshot.Items {
			view := CalculateTypedRowForDisplay(snapshot, index, true)
			if len(view.Forecast.Targets) != 1 || view.AddFIRAllowed || view.AddRegularAllowed {
				t.Fatalf("same-ID variant borrowed another physical row or enabled ambiguous writes: %+v", view)
			}
			if row.Name == "Regular variant" {
				assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:regular"), 2, 2, 1, 1)
			} else {
				assertTargetQualityForTest(t, targetByKeyForTest(t, view, "module:Module_2"), 3, 4, 3, 0)
			}
		}
	}
}

func TestTypedIndependentTargetsKeepCompletedRatiosWithoutInventingHistoricalQuality(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "done", Status: "completed", Completed: true}, {QuestID: "partial", Status: "available"}}
	done, partial := fixedSource("done", 4, true), fixedSource("partial", 3, true)
	done.Completed, done.RemainingRequiredQuantity = true, 0
	partial.Completed, partial.RemainingRequiredQuantity = true, 0
	snapshot.Items = []localprogress.Item{typedItem("selected", 1, 1, 0, done, partial, fixedSource("partial", 2, false))}
	view := CalculateTypedForDisplay(snapshot, "selected", true)
	for _, test := range []struct {
		key      string
		quantity int64
		done     bool
	}{{"quest:done", 4, true}, {"quest:partial", 5, false}} {
		target := targetByKeyForTest(t, view, test.key)
		if target.AvailableQuantity != test.quantity || target.RequiredQuantity != test.quantity || target.Completed != test.done ||
			target.QualityKnown || target.FIRQuantity != 0 || target.RegularQuantity != 0 {
			t.Fatalf("historical completed quantity acquired a fabricated current quality: %+v", target)
		}
	}
}

func TestTypedIndependentUncategorizedStockDoesNotContributeToGoals(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "fir", Status: "available"}, {QuestID: "regular", Status: "available"}}
	snapshot.Items = []localprogress.Item{typedItem("selected", 0, 0, 5, fixedSource("fir", 2, true), fixedSource("regular", 2, false))}
	view := CalculateTypedForDisplay(snapshot, "selected", true)
	assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:fir"), 0, 2, 0, 0)
	assertTargetQualityForTest(t, targetByKeyForTest(t, view, "quest:regular"), 0, 2, 0, 0)
}

func TestRegularPreferredFlowPreservesCoverageAndMaximizesNonFIRAgainstExhaustiveAssignments(t *testing.T) {
	random := rand.New(rand.NewSource(91402))
	for sample := 0; sample < 100; sample++ {
		stock := make([]int64, 6)
		for i := range stock {
			stock[i] = int64(random.Intn(2))
		}
		needs := make([]need, 3)
		for i := range needs {
			n := need{typed: true, shared: true, amount: int64(random.Intn(3)), fir: random.Intn(3) == 0}
			for bucket := range stock {
				if (!n.fir || bucket%2 == 1) && random.Intn(2) == 0 {
					n.rows = append(n.rows, bucket)
				}
			}
			needs[i] = n
		}
		original := engineAllocateNeeds(needs, stock)
		for i := range needs {
			needs[i].amount = engineAllocationTotal(original[i])
		}
		preferred := engineRegularPreferredFlow(needs, stock)
		var regular int64
		used := make([]int64, len(stock))
		for i := range preferred {
			if engineAllocationTotal(preferred[i]) != needs[i].amount {
				t.Fatalf("quality preference changed an existing need's coverage: sample=%d need=%+v allocation=%+v", sample, needs[i], preferred[i])
			}
			for bucket, amount := range preferred[i] {
				if !engineTestContains(needs[i].rows, bucket) {
					t.Fatal("quality preference used ineligible stock")
				}
				used[bucket] += amount
				if bucket%2 == 0 {
					regular += amount
				}
			}
		}
		for bucket := range used {
			if used[bucket] > stock[bucket] {
				t.Fatal("quality preference reused a physical piece within one goal")
			}
		}
		remaining := make([]int64, len(needs))
		for i := range needs {
			remaining[i] = needs[i].amount
		}
		best := int64(-1)
		var enumerate func(int, int64)
		enumerate = func(bucket int, haveRegular int64) {
			if bucket == len(stock) {
				for _, left := range remaining {
					if left != 0 {
						return
					}
				}
				best = max(best, haveRegular)
				return
			}
			enumerate(bucket+1, haveRegular)
			if stock[bucket] == 0 {
				return
			}
			for i := range needs {
				if remaining[i] > 0 && engineTestContains(needs[i].rows, bucket) {
					remaining[i]--
					increment := int64(0)
					if bucket%2 == 0 {
						increment = 1
					}
					enumerate(bucket+1, haveRegular+increment)
					remaining[i]++
				}
			}
		}
		enumerate(0, 0)
		if regular != best {
			t.Fatalf("goal display used FiR despite a feasible nonFiR residual replacement: sample=%d regular=%d optimal=%d stock=%v needs=%+v allocation=%+v", sample, regular, best, stock, needs, preferred)
		}
	}
}
