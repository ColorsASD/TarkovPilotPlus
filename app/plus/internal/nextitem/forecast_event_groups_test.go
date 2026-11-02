package nextitem

import (
	"encoding/json"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func eventGroupSnapshot() localprogress.Snapshot {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "ordinary", Name: "Ordinary quest", Status: "available"},
		{QuestID: "same-name", Name: "Halloween: Tightening the Quarantine", Completed: true},
	}
	snapshot.Items = []localprogress.Item{
		typedItem("medkit", 1, 0, 0, sharedSource("", "event-first", true), sharedSource("", "event-second", true)),
		typedItem("alternative", 0, 0, 0, sharedSource("", "event-first", true), sharedSource("", "event-second", true)),
		typedItem("ordinary-item", 0, 0, 0, fixedSource("ordinary", 2, false)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{
		{RequirementID: "event-first", QuestName: "Halloween: Tightening the Quarantine", Name: "Medkits", Amount: 2, RequiredQuantity: 2,
			AvailableQuantity: 1, RemainingQuantity: 1, FIR: true, ItemIDs: []string{"medkit", "alternative"}},
		{RequirementID: "event-second", QuestName: "Halloween: Tightening the Quarantine", Name: "Medkits", Amount: 3, RequiredQuantity: 3,
			RemainingQuantity: 3, FIR: true, ItemIDs: []string{"medkit", "alternative"}},
	}
	return snapshot
}

func TestForecastGroupsWithoutQuestIDKeepIndependentRequirementsAndOtherItems(t *testing.T) {
	snapshot := eventGroupSnapshot()
	before, _ := json.Marshal(snapshot)
	got := Calculate(snapshot, "medkit")
	if got.Status != "estimated" || got.Category != "Shared (FiR)" || len(got.Targets) != 2 {
		t.Fatalf("event requirements blocked the forecast: %+v", got)
	}
	for i, key := range []string{"quest::event-first", "quest::event-second"} {
		target := got.Targets[i]
		if target.GoalKey != key || target.GoalName != snapshot.MarketGroups[i].QuestName || target.Kind != "quest" ||
			target.RequiredQuantity != snapshot.MarketGroups[i].RequiredQuantity || target.AvailableQuantity != snapshot.MarketGroups[i].AvailableQuantity ||
			target.Completed || target.AllocationUnknown {
			t.Fatalf("requirements were merged by name or inherited unrelated completion: %+v", got.Targets)
		}
	}
	ordinary := Calculate(snapshot, "ordinary-item")
	if ordinary.Status != "estimated" || ordinary.Category != "Regular" || len(ordinary.Targets) != 1 || ordinary.Targets[0].GoalKey != "quest:ordinary" {
		t.Fatalf("unrelated event groups blocked an identified quest: %+v", ordinary)
	}
	graph, ok := buildForecastGraph(snapshot)
	if !ok {
		t.Fatal("event groups invalidated the complete graph")
	}
	for i, goal := range graph.goals {
		if goal.key != "quest:ordinary" && (!graph.unknown[i] || !goal.future) {
			t.Fatalf("missing quest metadata invented a known priority: %+v", goal)
		}
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("event forecast mutated stock, requirement identity or metadata")
	}
}

func TestTypedGroupsWithoutQuestIDKeepProjectionKeysAndDoNotDoubleAllocate(t *testing.T) {
	snapshot := eventGroupSnapshot()
	before, _ := json.Marshal(snapshot)
	got := CalculateTypedForDisplay(snapshot, "medkit", true)
	if !got.Known || !got.AddFIRAllowed || got.AddRegularAllowed || got.StockSummary == nil || len(got.Groups) != 2 {
		t.Fatalf("event groups disabled typed stock or accepted Regular for FiR: %+v", got)
	}
	var available, allocated int64
	for i, key := range []string{"quest::event-first", "quest::event-second"} {
		group, exists := got.Groups[key]
		if !exists || !group.FIR || group.RequiredQuantity != snapshot.MarketGroups[i].RequiredQuantity {
			t.Fatalf("typed group lost the Market projection identity: %+v", got.Groups)
		}
		if _, exists := got.StockSummary.Groups[key]; !exists {
			t.Fatalf("Stock summary group lost the Market projection identity: %+v", got.StockSummary.Groups)
		}
		available += group.AvailableQuantity
		allocated += group.AllocatedQuantity
	}
	if available != 1 || allocated != 1 || len(got.Forecast.Targets) != 2 {
		t.Fatalf("the same physical FiR item was allocated twice: %+v", got)
	}
	for _, target := range got.Forecast.Targets {
		if !target.QualityKnown || target.FIRQuantity != 1 || target.RegularQuantity != 0 || target.AvailableQuantity != 1 {
			t.Fatalf("independent target quality was lost: %+v", got.Forecast.Targets)
		}
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("typed event allocation mutated confirmed quantities or metadata")
	}
}

func TestForecastCompletedGroupWithoutQuestIDUsesOnlyMatchingExplicitSource(t *testing.T) {
	snapshot := eventGroupSnapshot()
	snapshot.MarketGroups[0].Completed, snapshot.MarketGroups[0].RequiredQuantity = true, 0
	snapshot.MarketGroups[0].AvailableQuantity, snapshot.MarketGroups[0].RemainingQuantity = 0, 0
	snapshot.Items[0].AvailableQuantity, snapshot.Items[0].MarketAvailableQuantity, snapshot.Items[0].FIRQuantity = 0, 0, 0
	snapshot.Items[0].Sources = append(snapshot.Items[0].Sources, snapshot.Items[0].Sources[0])
	got := Calculate(snapshot, "medkit")
	if got.Status != "estimated" || len(got.Targets) != 2 || !got.Targets[0].Completed || got.Targets[0].AvailableQuantity != 2 ||
		got.Targets[0].RequiredQuantity != 2 || got.Targets[1].Completed || got.Targets[1].RequiredQuantity != 3 {
		t.Fatalf("explicit completion was lost, duplicated or copied to another same-name group: %+v", got)
	}
	snapshot.Items[0].Sources = snapshot.Items[0].Sources[1:2]
	got = Calculate(snapshot, "medkit")
	if len(got.Targets) != 1 || got.Targets[0].GoalKey != "quest::event-second" {
		t.Fatalf("group membership alone invented completed progress: %+v", got.Targets)
	}
}

func TestForecastMissingQuestIDStillRequiresExplicitSharedGroup(t *testing.T) {
	snapshot := forecastSnapshot()
	source := fixedSource("", 2, true)
	source.RequirementID = "unmatched-requirement"
	snapshot.Items = []localprogress.Item{typedItem("item", 0, 0, 0, source)}
	if got := Calculate(snapshot, "item"); got.Status != "unavailable" {
		t.Fatalf("a source without explicit group data invented a quest: %+v", got)
	}
}
