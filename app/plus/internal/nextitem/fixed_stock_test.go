package nextitem

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestForecastFixedStockFollowsCurrentGoalOrderForMixedRequirements(t *testing.T) {
	for _, ids := range [][3]string{{"vodka", "booze", "bitter-victory"}, {"arbitrary-item", "large-goal", "small-goal"}} {
		for _, smallerFIR := range []bool{false, true} {
			for _, quantity := range []int64{0, 1, 3, 4, 5, 14, 17} {
				t.Run(ids[0]+"/fir-small="+strconv.FormatBool(smallerFIR)+"/stock="+strconv.FormatInt(quantity, 10), func(t *testing.T) {
					snapshot := forecastSnapshot()
					snapshot.MarketQuests = []market.Quest{
						{QuestID: ids[1], Name: "Large goal", Status: "locked"},
						{QuestID: ids[2], Name: "Small goal", Status: "locked"},
					}
					snapshot.Items = []localprogress.Item{forecastItem(ids[0], quantity,
						fixedSource(ids[1], 10, !smallerFIR), fixedSource(ids[2], 4, smallerFIR))}
					got := Calculate(snapshot, ids[0])
					small, large := min(quantity, 4), min(max(0, quantity-4), 10)
					want := localprogress.RequirementStock{Regular: small, FIR: large, Known: true}
					category := "Regular"
					if smallerFIR {
						want.FIR, want.Regular = small, large
						category = "Found in Raid"
					}
					if quantity >= 4 && quantity < 14 {
						if smallerFIR {
							category = "Regular"
						} else {
							category = "Found in Raid"
						}
					} else if quantity >= 14 {
						category = "Stock only"
					}
					if got.Status != "estimated" || got.Category != category || got.FixedStock == nil || *got.FixedStock != want {
						t.Fatalf("current category stock must use the same goal order as the unchanged next estimate: got=%+v stock=%+v want=%+v", got, got.FixedStock, want)
					}
					if len(got.Targets) != 2 || got.Targets[0].GoalKey != "quest:"+ids[1] || got.Targets[0].AvailableQuantity != large ||
						got.Targets[0].RequiredQuantity != 10 || got.Targets[1].GoalKey != "quest:"+ids[2] ||
						got.Targets[1].AvailableQuantity != small || got.Targets[1].RequiredQuantity != 4 {
						t.Fatalf("internal category stock changed current goal ratios: %+v", got.Targets)
					}
				})
			}
		}
	}
}

func TestForecastFixedStockDoesNotReuseSharedReservationOrOtherRows(t *testing.T) {
	for _, fir := range []bool{false, true} {
		snapshot := forecastSnapshot()
		snapshot.MarketQuests = []market.Quest{
			{QuestID: "shared", Name: "Shared goal", Status: "available"},
			{QuestID: "fixed", Name: "Fixed goal", Status: "locked"},
			{QuestID: "other", Name: "Other goal", Status: "available"},
		}
		snapshot.Items = []localprogress.Item{
			forecastItem("selected", 3, sharedSource("shared", "any", fir), fixedSource("fixed", 4, fir)),
			forecastItem("other", 7, fixedSource("other", 7, !fir)),
		}
		snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: 2, RequiredQuantity: 2,
			AvailableQuantity: 2, FIR: fir, ItemIDs: []string{"selected"}}}
		got := Calculate(snapshot, "selected")
		want := localprogress.RequirementStock{Regular: 1, Known: true}
		if fir {
			want.FIR, want.Regular = 1, 0
		}
		if got.Status != "estimated" || got.FixedStock == nil || *got.FixedStock != want {
			t.Fatalf("Shared stock or another row was reused as this item's fixed category stock: got=%+v stock=%+v want=%+v", got, got.FixedStock, want)
		}
		if len(got.Targets) != 2 || got.Targets[0].AvailableQuantity != 2 || got.Targets[0].RequiredQuantity != 2 ||
			got.Targets[1].AvailableQuantity != 1 || got.Targets[1].RequiredQuantity != 4 {
			t.Fatalf("Shared and fixed target counters changed: %+v", got.Targets)
		}
	}
}

func TestForecastFixedStockAtQuantityLimitUsesSupportedCurrentAllocation(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "fixed", Name: "Fixed goal", Status: "available"}}
	snapshot.Items = []localprogress.Item{forecastItem("selected", localprogress.MaxQuantity, fixedSource("fixed", 4, false))}
	got := Calculate(snapshot, "selected")
	if got.Status != "unavailable" || got.Category != "" || got.FixedStock == nil ||
		*got.FixedStock != (localprogress.RequirementStock{Regular: 4, Known: true}) {
		t.Fatalf("quantity cap lost the supported current category stock: %+v stock=%+v", got, got.FixedStock)
	}
}

func TestForecastFixedStockRequiresAnAcceptedCurrentModel(t *testing.T) {
	base := forecastSnapshot()
	base.MarketQuests = []market.Quest{{QuestID: "fixed", Name: "Fixed goal", Status: "available"}}
	base.Items = []localprogress.Item{forecastItem("selected", 1, fixedSource("fixed", 4, false))}
	for _, test := range []struct {
		name   string
		change func(*localprogress.Snapshot)
	}{
		{"incomplete metadata", func(s *localprogress.Snapshot) { s.MetadataComplete = false }},
		{"pending stock", func(s *localprogress.Snapshot) { s.Items[0].AvailableQuantity++ }},
		{"missing goal", func(s *localprogress.Snapshot) { s.MarketQuests = nil }},
		{"no active goal", func(s *localprogress.Snapshot) { s.Items[0].Sources[0].Completed = true }},
		{"counter mismatch", func(s *localprogress.Snapshot) {
			s.Items[0].Sources = []localprogress.Source{sharedSource("fixed", "any", false)}
			s.MarketGroups = []market.TrackedGroup{{QuestID: "fixed", RequirementID: "any", Amount: 2, RequiredQuantity: 2,
				AvailableQuantity: 2, ItemIDs: []string{"selected"}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := localprogress.Clone(base)
			test.change(&snapshot)
			if got := Calculate(snapshot, "selected"); got.FixedStock != nil {
				t.Fatalf("unsupported or unmatched allocation fabricated category stock: %+v", got.FixedStock)
			}
		})
	}
}

func TestForecastFixedStockIsIndependentAndExcludedFromJSON(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "fixed", Name: "Fixed goal", Status: "available"}}
	snapshot.Items = []localprogress.Item{forecastItem("selected", 1, fixedSource("fixed", 4, false))}
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got := Calculate(snapshot, "selected")
	if got.FixedStock == nil || *got.FixedStock != (localprogress.RequirementStock{Regular: 1, Known: true}) {
		t.Fatalf("current category stock was not captured: %+v", got.FixedStock)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	got.FixedStock.FIR, got.FixedStock.Regular, got.FixedStock.Known = 99, 88, false
	changed, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	withoutStock := got
	withoutStock.FixedStock = nil
	without, err := json.Marshal(withoutStock)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(changed) || string(encoded) != string(without) {
		t.Fatalf("internal category stock changed the forecast JSON DTO: before=%s changed=%s nil=%s", encoded, changed, without)
	}
	after, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("forecast or returned category stock mutated the input snapshot")
	}
	repeated := Calculate(snapshot, "selected")
	if repeated.FixedStock == got.FixedStock || repeated.FixedStock == nil ||
		*repeated.FixedStock != (localprogress.RequirementStock{Regular: 1, Known: true}) {
		t.Fatalf("independent calculations shared mutable category stock: %+v", repeated.FixedStock)
	}
}
