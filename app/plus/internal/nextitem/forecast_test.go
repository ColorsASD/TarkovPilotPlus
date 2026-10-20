package nextitem

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func forecastSnapshot() localprogress.Snapshot {
	return localprogress.Snapshot{CharUID: "fixture-character", GameMode: "PVP", Revision: "fixture-revision", MetadataComplete: true,
		Items: []localprogress.Item{}, MarketQuests: []market.Quest{}, MarketHideout: []market.HideoutUpgrade{}}
}

func fixedSource(quest string, amount int64, fir bool) localprogress.Source {
	return localprogress.Source{Origin: localprogress.OriginMarket, TrackedSource: market.TrackedSource{
		Type: "quest", QuestID: quest, Amount: amount, RemainingRequiredQuantity: amount, FIR: fir}}
}

func forecastItem(id string, quantity int64, sources ...localprogress.Source) localprogress.Item {
	return localprogress.Item{ItemID: id, Name: id, MarketTracked: true, AvailableQuantity: quantity, MarketAvailableQuantity: quantity, Sources: sources}
}

func sharedSource(quest, requirement string, fir bool) localprogress.Source {
	source := fixedSource(quest, 0, fir)
	source.Type, source.RequirementID = "questGroup", requirement
	return source
}

func TestForecastUsesNewQuestAndHideoutSourcesWithoutItemSpecificRules(t *testing.T) {
	for _, test := range []struct {
		name, category, goal string
		module               bool
	}{
		{"new-quest", "Found in Raid", "A newly returned quest", false},
		{"new-hideout", "Regular", "A newly returned module 2", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{{QuestID: "old", Name: "Old large goal", Status: "available"}}
			sources := []localprogress.Source{fixedSource("old", 4, !test.module)}
			if test.module {
				snapshot.MarketHideout = []market.HideoutUpgrade{{UpgradeKey: "Fixture_2", Name: "A newly returned module", Level: 2, CanComplete: true}}
				sources = append(sources, localprogress.Source{Origin: localprogress.OriginMarket, TrackedSource: market.TrackedSource{
					Type: "hideout", UpgradeKey: "Fixture_2", Amount: 1, RemainingRequiredQuantity: 1}})
			} else {
				snapshot.MarketQuests = append(snapshot.MarketQuests, market.Quest{QuestID: "new", QuestIDAliases: []string{"source-alias"}, Name: test.goal, Status: "available"})
				sources = append(sources, fixedSource("source-alias", 1, true))
			}
			snapshot.Items = []localprogress.Item{forecastItem("arbitrary-item", 0, sources...)}
			before, _ := json.Marshal(snapshot)
			got := Calculate(snapshot, "arbitrary-item")
			if got.Status != "estimated" || got.Category != test.category || got.GoalName != test.goal || len(got.Changes) != 1 || got.Changes[0].Delta != 1 {
				t.Fatalf("new metadata did not control the generic estimate: %+v", got)
			}
			after, _ := json.Marshal(snapshot)
			if string(after) != string(before) {
				t.Fatal("forecast mutated stock, requirements, aliases or source metadata")
			}
		})
	}
}

func TestForecastRyeLikeUnknownHorizonsRetainBothMatchingCategories(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "shared", Name: "Shared food goal", Status: "locked"},
		{QuestID: "regular", Name: "Regular food goal", Status: "locked"},
	}
	snapshot.Items = []localprogress.Item{
		forecastItem("primary-food", 0, fixedSource("regular", 4, false), sharedSource("shared", "food", true)),
		forecastItem("other-food", 1, sharedSource("shared", "food", true)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "food", QuestName: "Shared food goal", Name: "Food",
		Amount: 5, RequiredQuantity: 5, AvailableQuantity: 1, RemainingQuantity: 4, FIR: true, ItemIDs: []string{"primary-food", "other-food"}}}
	before, _ := json.Marshal(snapshot)
	got := Calculate(snapshot, "primary-food")
	if got.Status != "estimated" || !containsCategory(got.PossibleCategories, "Regular") || !containsCategory(got.PossibleCategories, "Shared (FiR)") ||
		!strings.Contains(got.Reason, "different categories") || !strings.Contains(got.Reason, "Estimated") {
		t.Fatalf("both baseline-compatible next categories must remain visible: %+v", got)
	}
	after, _ := json.Marshal(snapshot)
	if string(after) != string(before) {
		t.Fatal("alternative horizon probes modified the confirmed input")
	}
}

func TestForecastReportsAllNetCountersWhenOneNewItemReassignsAnOldItem(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "shared", Name: "Shared goal", Status: "available"}, {QuestID: "regular", Name: "Regular goal", Status: "available"}}
	snapshot.Items = []localprogress.Item{
		forecastItem("x", 1, fixedSource("regular", 1, false), sharedSource("shared", "any", true)),
		forecastItem("y", 1, fixedSource("regular", 1, false)),
		forecastItem("z", 0, sharedSource("shared", "any", true)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: 2, RequiredQuantity: 2,
		FIR: true, ItemIDs: []string{"x", "z"}}}
	got := Calculate(snapshot, "x")
	if got.Status != "estimated" || got.Category != "Reallocated" {
		t.Fatalf("+1 which reallocates a prior x cannot be described as a simple category: %+v", got)
	}
	if changeDelta(got, "quest:shared", "Shared (FiR)") != 2 || changeDelta(got, "quest:regular", "Regular") != -1 {
		t.Fatalf("the actual goal/category counter changes were lost: %+v", got)
	}
}

func TestForecastPreservesOtherDuplicateVariantsWithoutBorrowingTheirStock(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "group", Name: "Shared goal", Status: "available"}, {QuestID: "other", Name: "Other goal", Status: "available"}}
	snapshot.Items = []localprogress.Item{
		forecastItem("target", 0, sharedSource("group", "requirement", true)),
		forecastItem("same-id", 1, sharedSource("group", "requirement", true)),
		forecastItem("same-id", 7, fixedSource("other", 7, false)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "group", RequirementID: "requirement", Amount: 2, RequiredQuantity: 2,
		AvailableQuantity: 1, RemainingQuantity: 1, FIR: true, ItemIDs: []string{"target", "same-id"}}}
	before, _ := json.Marshal(snapshot)
	got := Calculate(snapshot, "target")
	if got.Status != "estimated" || got.Category != "Shared (FiR)" || changeDelta(got, "quest:group", "Shared (FiR)") != 1 {
		t.Fatalf("a non-eligible same-ID variant contaminated the Shared estimate: %+v", got)
	}
	after, _ := json.Marshal(snapshot)
	if string(after) != string(before) {
		t.Fatal("variant stock changed during a virtual addition")
	}
	if got := Calculate(snapshot, "same-id"); got.Status != "unavailable" {
		t.Fatalf("ambiguous write targets must not receive a Next estimate: %+v", got)
	}
}

func TestForecastWaitsForConfirmedCompleteSupportedInput(t *testing.T) {
	base := forecastSnapshot()
	base.MarketQuests = []market.Quest{{QuestID: "quest", Name: "Goal", Status: "available"}}
	base.Items = []localprogress.Item{forecastItem("item", 0, fixedSource("quest", 2, false))}
	for _, test := range []struct {
		name   string
		change func(*localprogress.Snapshot)
	}{
		{"missing metadata", func(s *localprogress.Snapshot) { s.MetadataComplete = false }},
		{"pending addition", func(s *localprogress.Snapshot) { s.Items[0].AvailableQuantity++ }},
		{"pending removal", func(s *localprogress.Snapshot) { s.Items[0].MarketAvailableQuantity++ }},
		{"separate inventory", func(s *localprogress.Snapshot) { s.FIRInventorySeparate = true }},
		{"quantity limit", func(s *localprogress.Snapshot) {
			s.Items[0].AvailableQuantity, s.Items[0].MarketAvailableQuantity = localprogress.MaxQuantity, localprogress.MaxQuantity
		}},
		{"missing quest", func(s *localprogress.Snapshot) { s.MarketQuests = nil }},
		{"invalid quantity", func(s *localprogress.Snapshot) { s.Items[0].MarketAvailableQuantity = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := localprogress.Clone(base)
			test.change(&snapshot)
			if got := Calculate(snapshot, "item"); got.Status != "unavailable" || got.Category != "" || got.Reason == "" {
				t.Fatalf("unsupported input must not pretend to predict a category: %+v", got)
			}
		})
	}
}

func TestForecastRejectsEveryModelIfReportedSharedCannotBeCovered(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "shared", Name: "Shared", Status: "available"}}
	snapshot.Items = []localprogress.Item{forecastItem("item", 0, sharedSource("shared", "group", false))}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "group", Amount: 2, RequiredQuantity: 2,
		AvailableQuantity: 1, RemainingQuantity: 1, ItemIDs: []string{"item"}}}
	if got := Calculate(snapshot, "item"); got.Status != "uncertain" || got.Category != "" {
		t.Fatalf("a counter mismatch cannot become an estimated allocation: %+v", got)
	}
}

func TestForecastTargetsUseCurrentSharedCounterIncludingOtherCandidates(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "regular", Name: "Current regular goal", Status: "available"},
		{QuestID: "shared", Name: "Current shared goal", Status: "locked"},
	}
	snapshot.Items = []localprogress.Item{
		forecastItem("item", 0, fixedSource("regular", 4, false), sharedSource("shared", "any", true)),
		forecastItem("other", 3, sharedSource("shared", "any", true)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: 5, RequiredQuantity: 5,
		AvailableQuantity: 3, RemainingQuantity: 2, FIR: true, ItemIDs: []string{"item", "other"}}}
	before, _ := json.Marshal(snapshot)
	got := Calculate(snapshot, "item")
	if got.Status != "estimated" || got.Category != "Regular" || len(got.Targets) != 2 {
		t.Fatalf("all current item goals must accompany the next category: %+v", got)
	}
	if regular, shared := got.Targets[0], got.Targets[1]; regular.GoalName != "Current regular goal" || regular.AvailableQuantity != 0 || regular.RequiredQuantity != 4 ||
		shared.GoalName != "Current shared goal" || shared.AvailableQuantity != 3 || shared.RequiredQuantity != 5 || shared.AllocationUnknown {
		t.Fatalf("targets must use before stock and the whole API Shared counter: %+v", got.Targets)
	}
	after, _ := json.Marshal(snapshot)
	if string(after) != string(before) {
		t.Fatal("building target ratios modified confirmed data")
	}
}

func TestForecastTargetsListEveryQuestBeforeEveryHideoutInMetadataOrder(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "first", Name: "First quest", Status: "available"},
		{QuestID: "new", Name: "New quest", Status: "available"},
	}
	snapshot.MarketHideout = []market.HideoutUpgrade{
		{UpgradeKey: "First_2", Name: "First module", Level: 2, CanComplete: true},
		{UpgradeKey: "New_1", Name: "New module", Level: 1, CanComplete: true},
	}
	moduleSource := func(key string, amount int64) localprogress.Source {
		return localprogress.Source{Origin: localprogress.OriginMarket, TrackedSource: market.TrackedSource{
			Type: "hideout", UpgradeKey: key, Amount: amount, RemainingRequiredQuantity: amount}}
	}
	optional, canceled, zero := fixedSource("first", 10, false), fixedSource("first", 10, false), fixedSource("first", 0, false)
	optional.Optional, canceled.Canceled = true, true
	snapshot.Items = []localprogress.Item{forecastItem("item", 8,
		moduleSource("New_1", 2), fixedSource("new", 3, true), moduleSource("First_2", 4), fixedSource("first", 5, false),
		optional, canceled, zero)}
	got := Calculate(snapshot, "item")
	wantedKeys := []string{"quest:first", "quest:new", "module:First_2", "module:New_1"}
	wantedRequired := []int64{5, 3, 4, 2}
	if got.Status != "estimated" || len(got.Targets) != len(wantedKeys) {
		t.Fatalf("active quests and modules were lost or closed sources included: %+v", got)
	}
	var have int64
	for i, target := range got.Targets {
		if target.GoalKey != wantedKeys[i] || target.RequiredQuantity != wantedRequired[i] || target.AllocationUnknown ||
			(i < 2 && target.Kind != "quest") || (i >= 2 && target.Kind != "module") {
			t.Fatalf("targets order/kind/amount does not follow current API goals: %+v", got.Targets)
		}
		have += target.AvailableQuantity
	}
	if have != 8 || got.Targets[2].GoalName != "First module 2" || got.Targets[3].GoalName != "New module 1" {
		t.Fatalf("target ratios used virtual +1 or lost module identity: %+v", got.Targets)
	}
}

func TestForecastTargetsIncludeCompletedQuestGroupAndPrebuiltModuleWithoutStock(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "fixed", Name: "Completed quest", Status: "completed"},
		{QuestID: "group", QuestIDAliases: []string{"group-alias"}, Name: "Completed Shared quest", Completed: true},
	}
	snapshot.MarketHideout = []market.HideoutUpgrade{{UpgradeKey: "Prebuilt_2", Name: "Prebuilt module", Level: 2, Prebuilt: true}}
	snapshot.Items = []localprogress.Item{forecastItem("item", 0,
		fixedSource("fixed", 4, true), sharedSource("group-alias", "any", true),
		localprogress.Source{Origin: localprogress.OriginMarket, TrackedSource: market.TrackedSource{Type: "hideout", UpgradeKey: "Prebuilt_2", Amount: 2}})}
	snapshot.Items[0].SpentQuantity = 731 // This unrelated aggregate must not enter a completed ratio.
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "group", RequirementID: "any", Amount: 5, Completed: true,
		FIR: true, ItemIDs: []string{"item"}}}
	before, _ := json.Marshal(snapshot)
	got := Calculate(snapshot, "item")
	if got.Status != "uncertain" || got.Category != "" || len(got.Targets) != 3 {
		t.Fatalf("completed-only data needs a target list even without an active +1 category: %+v", got)
	}
	for i, amount := range []int64{4, 5, 2} {
		if got.Targets[i].AvailableQuantity != amount || got.Targets[i].RequiredQuantity != amount || got.Targets[i].AllocationUnknown || !got.Targets[i].Completed {
			t.Fatalf("completed source/group Amount must yield its own x/x ratio: %+v", got.Targets)
		}
	}
	if got.Targets[0].GoalKey != "quest:fixed" || got.Targets[1].GoalKey != "quest:group" || got.Targets[2].GoalName != "Prebuilt module 2" || got.Targets[2].Kind != "module" {
		t.Fatalf("completed target aliases/order/names were lost: %+v", got.Targets)
	}
	after, _ := json.Marshal(snapshot)
	if string(after) != string(before) {
		t.Fatal("completed target display mutated original progress")
	}
	snapshot.Items[0].AvailableQuantity, snapshot.Items[0].MarketAvailableQuantity = localprogress.MaxQuantity, localprogress.MaxQuantity
	if capped := Calculate(snapshot, "item"); len(capped.Targets) != 3 || capped.Category != "" {
		t.Fatalf("quantity cap must not suppress informational completed targets: %+v", capped)
	}
}

func TestForecastTargetsMergeActiveAndCompletedNeedsIntoOneGoal(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "quest", QuestIDAliases: []string{"alias"}, Name: "Mixed progress", Status: "available"}}
	closed := fixedSource("alias", 3, true)
	closed.Completed, closed.RemainingRequiredQuantity, closed.RequirementID = true, 0, "closed-fixed"
	groupSource := sharedSource("alias", "closed-group", false)
	snapshot.Items = []localprogress.Item{
		forecastItem("item", 1, fixedSource("quest", 2, false), closed, groupSource, groupSource),
		forecastItem("other", 0, fixedSource("quest", 99, true)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "quest", RequirementID: "closed-group", Amount: 5, Completed: true, ItemIDs: []string{"item", "other"}}}
	got := Calculate(snapshot, "item")
	if got.Status != "estimated" || len(got.Targets) != 1 || got.Targets[0].GoalKey != "quest:quest" ||
		got.Targets[0].AvailableQuantity != 9 || got.Targets[0].RequiredQuantity != 10 || got.Targets[0].AllocationUnknown || got.Targets[0].Completed {
		t.Fatalf("active current 1/2 plus completed 3/3 and explicit 5/5 must appear once as 9/10: %+v", got)
	}
	if got.Category != "Regular" || changeDelta(got, "quest:quest", "Regular") != 1 {
		t.Fatalf("completed display requirements leaked into the active allocation engine: %+v", got)
	}
	snapshot.Items[0].AvailableQuantity, snapshot.Items[0].MarketAvailableQuantity = 2, 2
	covered := Calculate(snapshot, "item")
	if len(covered.Targets) != 1 || covered.Targets[0].AvailableQuantity != 10 || covered.Targets[0].RequiredQuantity != 10 || covered.Targets[0].Completed {
		t.Fatalf("stock-covered active parts must keep a mixed goal active even at 10/10: %+v", covered)
	}
}

func TestForecastTargetsAggregateRepeatedFixedSourcesBeforeAndAfterCompletion(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "quest", Name: "Repeated fixed sources", Status: "available"}}
	first, second := fixedSource("quest", 3, true), fixedSource("quest", 5, true)
	first.RequirementID, second.RequirementID = "same-requirement", "same-requirement"
	snapshot.Items = []localprogress.Item{forecastItem("item", 8, first, second)}
	active := Calculate(snapshot, "item")
	if len(active.Targets) != 1 || active.Targets[0].AvailableQuantity != 8 || active.Targets[0].RequiredQuantity != 8 || active.Targets[0].Completed {
		t.Fatalf("all API fixed source amounts must enter the active ratio: %+v", active)
	}
	snapshot.MarketQuests[0].Completed = true
	snapshot.Items[0].AvailableQuantity, snapshot.Items[0].MarketAvailableQuantity = 0, 0
	completed := Calculate(snapshot, "item")
	if len(completed.Targets) != 1 || completed.Targets[0].AvailableQuantity != 8 || completed.Targets[0].RequiredQuantity != 8 || !completed.Targets[0].Completed {
		t.Fatalf("completion must preserve the same fixed source aggregate: %+v", completed)
	}
	encoded, err := json.Marshal(completed.Targets[0])
	if err != nil || !strings.Contains(string(encoded), `"completed":true`) {
		t.Fatalf("completed source state must reach the frontend explicitly: %s %v", encoded, err)
	}
}

func TestForecastTargetsZeroRemainingDoesNotProveAllSourcesCompleted(t *testing.T) {
	for _, test := range []struct {
		name              string
		amount, remaining int64
	}{
		{"positive original amount", 5, 0},
		{"positive remaining without original amount", 0, 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forecastSnapshot()
			snapshot.MarketQuests = []market.Quest{{QuestID: "quest", Name: "Mixed source state", Status: "available"}}
			closed, alive := fixedSource("quest", 2, false), fixedSource("quest", test.amount, true)
			closed.Completed, closed.RemainingRequiredQuantity = true, 0
			alive.RemainingRequiredQuantity = test.remaining
			snapshot.Items = []localprogress.Item{forecastItem("item", 0, closed, alive)}
			got := Calculate(snapshot, "item")
			if len(got.Targets) != 1 || got.Targets[0].Completed || got.Targets[0].AvailableQuantity != 2 || got.Targets[0].RequiredQuantity != 2+test.remaining {
				t.Fatalf("an unclosed positive source must prevent Completed without inventing a new displayed amount: %+v", got)
			}
		})
	}
}

func TestForecastTargetsExcludedGroupDoesNotPreventCompletedSources(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		snapshot := forecastSnapshot()
		snapshot.MarketQuests = []market.Quest{{QuestID: "quest", Name: "Completed plus excluded group", Status: "available"}}
		closed, excluded := fixedSource("quest", 2, false), sharedSource("quest", "excluded", true)
		closed.Completed, closed.RemainingRequiredQuantity, excluded.Amount = true, 0, 5
		snapshot.Items = []localprogress.Item{forecastItem("item", 0, closed, excluded)}
		snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "quest", RequirementID: "excluded", Amount: 5,
			Optional: !canceled, Canceled: canceled, FIR: true, ItemIDs: []string{"item"}}}
		got := Calculate(snapshot, "item")
		if len(got.Targets) != 1 || !got.Targets[0].Completed || got.Targets[0].AvailableQuantity != 2 || got.Targets[0].RequiredQuantity != 2 {
			t.Fatalf("optional/canceled explicit groups must not block completed-only display: %+v", got)
		}
	}
}

func TestForecastTargetsDoNotGuessCompletedGoalsFromNameStockOrGroupMembership(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "quest", Name: "Same as item", Completed: true}}
	snapshot.Items = []localprogress.Item{forecastItem("item", 8)}
	snapshot.Items[0].Name, snapshot.Items[0].SpentQuantity = "Same as item", 81
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "quest", RequirementID: "actual", Amount: 5, Completed: true, ItemIDs: []string{"item"}}}
	if got := Calculate(snapshot, "item"); len(got.Targets) != 0 {
		t.Fatalf("a completed goal requires this physical row's matching source: %+v", got)
	}
	wrong := sharedSource("quest", "different-requirement", false)
	wrong.Completed, wrong.Amount = true, 9
	snapshot.Items[0].Sources = []localprogress.Source{wrong}
	if got := Calculate(snapshot, "item"); len(got.Targets) != 0 {
		t.Fatalf("a different Shared requirement must not borrow the explicit completed amount: %+v", got)
	}
}

func TestForecastTargetsExcludeCanceledOptionalAndZeroCompletedSources(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "done", Name: "Done", Completed: true}, {QuestID: "failed", Name: "Failed", Status: "failed"}}
	optional, canceled := fixedSource("done", 7, false), fixedSource("done", 9, true)
	optional.Completed, optional.Optional, canceled.Completed, canceled.Canceled = true, true, true, true
	zero := fixedSource("done", 0, false)
	zero.Completed = true
	snapshot.Items = []localprogress.Item{forecastItem("item", 0,
		fixedSource("done", 2, false), optional, canceled, zero, fixedSource("failed", 11, true), sharedSource("done", "optional", true))}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "done", RequirementID: "optional", Amount: 13, Completed: true, Optional: true,
		FIR: true, ItemIDs: []string{"item"}}}
	got := Calculate(snapshot, "item")
	if len(got.Targets) != 1 || got.Targets[0].AvailableQuantity != 2 || got.Targets[0].RequiredQuantity != 2 {
		t.Fatalf("canceled, optional or zero sources must not enter the completed ratio: %+v", got)
	}
}

func TestForecastTargetsKeepCompletedWhileActiveBaselineIsUnknown(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{
		{QuestID: "active", Name: "Active", Status: "available"},
		{QuestID: "done", Name: "Done", Completed: true},
	}
	snapshot.Items = []localprogress.Item{forecastItem("item", 0, fixedSource("active", 2, false), sharedSource("active", "any", true), fixedSource("done", 4, false))}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "active", RequirementID: "any", Amount: 5, RequiredQuantity: 5,
		AvailableQuantity: 1, FIR: true, ItemIDs: []string{"item"}}}
	got := Calculate(snapshot, "item")
	if got.Status != "uncertain" || len(got.Targets) != 2 || !got.Targets[0].AllocationUnknown ||
		got.Targets[1].AllocationUnknown || got.Targets[1].AvailableQuantity != 4 || got.Targets[1].RequiredQuantity != 4 {
		t.Fatalf("unknown active allocation must preserve independently completed requirements: %+v", got)
	}
}

func TestForecastTargetsAggregateOnlySelectedItemNeedsOncePerGoal(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "quest", Name: "Mixed requirements", Status: "available"}}
	snapshot.Items = []localprogress.Item{
		forecastItem("item", 1, fixedSource("quest", 2, false), sharedSource("quest", "any", true)),
		forecastItem("other", 2, sharedSource("quest", "any", true)),
		forecastItem("unrelated", 0, fixedSource("quest", 99, true)),
	}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "quest", RequirementID: "any", Amount: 5, RequiredQuantity: 5,
		AvailableQuantity: 2, RemainingQuantity: 3, FIR: true, ItemIDs: []string{"item", "other"}}}
	got := Calculate(snapshot, "item")
	if got.Status != "estimated" || len(got.Targets) != 1 || got.Targets[0].AvailableQuantity != 3 || got.Targets[0].RequiredQuantity != 7 || got.Targets[0].AllocationUnknown {
		t.Fatalf("fixed plus Shared must be aggregated once, without unrelated goal needs: %+v", got)
	}
}

func TestForecastTargetsKeepExplicitSharedKnownWhenNoBaselineMatches(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "regular", Name: "Regular", Status: "available"}, {QuestID: "shared", Name: "Shared", Status: "available"}}
	snapshot.Items = []localprogress.Item{forecastItem("item", 0, fixedSource("regular", 4, false), sharedSource("shared", "any", true))}
	snapshot.MarketGroups = []market.TrackedGroup{{QuestID: "shared", RequirementID: "any", Amount: 5, RequiredQuantity: 5,
		AvailableQuantity: 2, RemainingQuantity: 3, FIR: true, ItemIDs: []string{"item"}}}
	got := Calculate(snapshot, "item")
	if got.Status != "uncertain" || len(got.Targets) != 2 || !got.Targets[0].AllocationUnknown || got.Targets[0].AvailableQuantity != 0 || got.Targets[0].RequiredQuantity != 4 ||
		got.Targets[1].AllocationUnknown || got.Targets[1].AvailableQuantity != 2 || got.Targets[1].RequiredQuantity != 5 {
		t.Fatalf("no matching baseline may invent a fixed ratio or erase an explicit Shared counter: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(encoded), `"allocationUnknown":true`) || !strings.Contains(string(encoded), `"targets"`) || !strings.Contains(string(encoded), `"requiredQuantity"`) {
		t.Fatalf("target JSON contract changed: %s %v", encoded, err)
	}
}

func TestForecastStockOnlyAndSharedReplacementAreDistinct(t *testing.T) {
	snapshot := forecastSnapshot()
	snapshot.MarketQuests = []market.Quest{{QuestID: "quest", Name: "Covered", Status: "available"}}
	snapshot.Items = []localprogress.Item{forecastItem("item", 1, fixedSource("quest", 1, false))}
	if got := Calculate(snapshot, "item"); got.Status != "estimated" || got.Category != "Stock only" || len(got.Changes) != 0 {
		t.Fatalf("an unused extra item should be reported as Stock only: %+v", got)
	}
	// An unchanged Shared ratio can conceal a candidate replacement. The
	// newly freed other item then goes to a Regular goal; calling the added
	// target simply Regular would describe the wrong physical allocation.
	before := []goal{
		{key: "shared", name: "Shared", needs: []need{{key: "group", shared: true, have: 1, allocations: map[int]int64{1: 1}}}},
		{key: "regular", name: "Regular", needs: []need{{key: "fixed", have: 0}}},
	}
	after := []goal{
		{key: "shared", name: "Shared", needs: []need{{key: "group", shared: true, have: 1, allocations: map[int]int64{0: 1}}}},
		{key: "regular", name: "Regular", needs: []need{{key: "fixed", have: 1, allocations: map[int]int64{1: 1}}}},
	}
	got := changesForecast(before, after, 0)
	if got.Category != "Reallocated" || changeDelta(got, "regular", "Regular") != 1 {
		t.Fatalf("candidate replacement was mislabeled as a simple next category: %+v", got)
	}
}

func TestForecastJSONNamesAndLargeComponentCoverageAreExplicit(t *testing.T) {
	graph := forecastGraph{goals: make([]goal, 7), unknown: make([]bool, 7)}
	component := []int{}
	for i := range graph.goals {
		graph.goals[i] = goal{kind: "quest", future: true}
		graph.unknown[i] = true
		component = append(component, i)
	}
	models, bounded := forecastModels(graph, component)
	if !bounded || len(models) > 20 {
		t.Fatalf("large components must use explicitly bounded priority probes: %d %v", len(models), bounded)
	}
	encoded, err := json.Marshal(Forecast{Status: "estimated", Category: "Regular", PossibleCategories: []string{"Regular", "Shared"}, GoalName: "Goal",
		Changes: []Change{{GoalKey: "quest:key", GoalName: "Goal", Category: "Regular", Delta: 1}}})
	if err != nil || !strings.Contains(string(encoded), `"possibleCategories"`) || !strings.Contains(string(encoded), `"goalKey"`) || strings.Contains(string(encoded), `"GoalKey"`) {
		t.Fatalf("frontend JSON contract changed: %s %v", encoded, err)
	}
}

func containsCategory(categories []string, category string) bool {
	for _, value := range categories {
		if value == category {
			return true
		}
	}
	return false
}

func changeDelta(forecast Forecast, goalKey, category string) int64 {
	for _, change := range forecast.Changes {
		if change.GoalKey == goalKey && change.Category == category {
			return change.Delta
		}
	}
	return 0
}
