package localprogress

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

func inputs() (market.TrackedSnapshot, market.QuestSnapshot, market.HideoutSnapshot) {
	return market.TrackedSnapshot{CharUID: "char-1", GameMode: "PVP", Revision: "rev-1", Items: []market.TrackedItem{}},
		market.QuestSnapshot{CharUID: "char-1", GameMode: "PVP", Revision: "rev-1", Quests: []market.Quest{}},
		market.HideoutSnapshot{CharUID: "char-1", GameMode: "PVP", Revision: "rev-1", Modules: []market.HideoutUpgrade{}}
}

func source(quest string, count int64, fir bool) market.TrackedSource {
	return market.TrackedSource{Type: "quest", QuestID: quest, Amount: count, RemainingRequiredQuantity: count, FIR: fir}
}

func requireItem(t *testing.T, snapshot Snapshot, id string) Item {
	t.Helper()
	for _, item := range snapshot.Items {
		if item.ItemID == id {
			return item
		}
	}
	t.Fatalf("missing item %q in %#v", id, snapshot)
	return Item{}
}

func sourceCounts(item Item) (fir, regular, completed int64) {
	for _, source := range item.Sources {
		if source.Canceled {
			continue
		}
		if source.Completed {
			completed += source.Amount
			continue
		}
		if source.Optional {
			continue
		}
		if source.FIR {
			fir += source.RemainingRequiredQuantity
		} else {
			regular += source.RemainingRequiredQuantity
		}
	}
	return
}

func TestQuestCompletionKeepsSevenSalewasSplitAndThreeCompleted(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "q1"}, {QuestID: "q2"}}
	tracked.Items = []market.TrackedItem{
		{ItemID: "salewa", RequiredQuantity: 7, RemainingQuantity: 7, Sources: []market.TrackedSource{source("q1", 1, true), source("q1", 2, false), source("q2", 1, true), source("q2", 3, false)}},
		{ItemID: "bolt", RequiredQuantity: 2, RemainingQuantity: 2, Sources: []market.TrackedSource{{Type: "hideout", UpgradeKey: "workbench-2", Amount: 2, RemainingRequiredQuantity: 2}}},
	}
	hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "workbench-2", ModuleID: "workbench", Level: 2}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "q1", Objectives: []tarkovdev.Objective{{ID: "objective-1", Type: "giveItem", ItemIDs: []string{"salewa"}, Count: 99, FoundInRaid: true}}}}}
	before, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, before, "salewa")
	fir, regular, completed := sourceCounts(item)
	if item.RequiredQuantity != 7 || fir != 2 || regular != 5 || completed != 0 || len(item.Sources) != 4 {
		t.Fatalf("Market quantities were changed/doubled: %+v", item)
	}
	quests.Quests[0].Completed = true
	after, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	item = requireItem(t, after, "salewa")
	fir, regular, completed = sourceCounts(item)
	if item.RequiredQuantity != 4 || item.RemainingQuantity != 4 || fir != 1 || regular != 3 || completed != 3 {
		t.Fatalf("wrong completion arithmetic: %+v fir=%d regular=%d completed=%d", item, fir, regular, completed)
	}
	if requireItem(t, after, "bolt").RemainingQuantity != 2 {
		t.Fatal("other hideout requirements were removed")
	}
	if tracked.Items[0].Sources[0].Completed {
		t.Fatal("Merge mutated Market input")
	}
}

func TestMissingQuestItemIsAddedOnceAndOtherQuestStillRequiresIt(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "market-q1", QuestIDAliases: []string{"dev-q1"}}, {QuestID: "q2"}}
	tracked.Items = []market.TrackedItem{{ItemID: "salewa", Sources: []market.TrackedSource{source("q2", 4, false)}}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "dev-q1", Objectives: []tarkovdev.Objective{
		{ID: "find", Type: "findItem", ItemIDs: []string{"salewa"}, Count: 3},
		{ID: "give", Type: "giveItem", ItemIDs: []string{"salewa"}, Count: 3, FoundInRaid: true},
		{ID: "optional", Type: "giveItem", ItemIDs: []string{"ledx"}, Count: 5, Optional: true},
	}}}}
	before, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, before, "salewa")
	if len(before.Items) != 1 || len(item.Sources) != 2 || item.RequiredQuantity != 7 || item.Sources[1].QuestID != "market-q1" || item.Sources[1].Origin != OriginDev {
		t.Fatalf("missing relation or alias merge is wrong: %+v", before)
	}
	quests.Quests[0].Completed = true
	after, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if requireItem(t, after, "salewa").RequiredQuantity != 4 {
		t.Fatal("other quest quantity disappeared")
	}
}

func TestDevOnlyFixedRequirementsKeepCompletedCountAcrossQuests(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "quest-a"}, {QuestID: "quest-b"}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{
		{ID: "quest-a", Objectives: []tarkovdev.Objective{{ID: "give-a", Type: "giveItem", ItemIDs: []string{"salewa"}, Count: 3, FoundInRaid: true}}},
		{ID: "quest-b", Objectives: []tarkovdev.Objective{{ID: "give-b", Type: "giveItem", ItemIDs: []string{"salewa"}, Count: 4}}},
	}}
	before, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if item := requireItem(t, before, "salewa"); item.MarketTracked || item.RequiredQuantity != 7 || item.RemainingQuantity != 7 {
		t.Fatalf("dev-only fixed requirements did not total seven: %+v", item)
	}
	quests.Quests[0].Completed = true
	after, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, after, "salewa")
	_, _, completed := sourceCounts(item)
	if item.RequiredQuantity != 4 || item.RemainingQuantity != 4 || completed != 3 || !item.Sources[0].Completed || item.Sources[0].Amount != 3 || item.Sources[1].Completed {
		t.Fatalf("fixed known quantity lost completion or affected other quest: %+v, completed=%d", item, completed)
	}
}

func TestCompletedDevOnlyAlternativesDoNotInventItemCompletions(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "any-three", Completed: true}}
	ids := make([]string, 56)
	for i := range ids {
		ids[i] = fmt.Sprintf("alternative-%d", i)
	}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "any-three", Objectives: []tarkovdev.Objective{{ID: "give-any", Type: "giveItem", ItemIDs: ids, Count: 3}}}}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 56 {
		t.Fatalf("missing alternative candidates: %d", len(merged.Items))
	}
	for _, item := range merged.Items {
		if item.RequiredQuantity != 0 || item.RemainingQuantity != 0 || len(item.Sources) != 1 || !item.Sources[0].Completed || item.Sources[0].Amount != 0 || item.Sources[0].GroupAmount != 3 || item.Sources[0].Alternatives != 56 {
			t.Fatalf("completion invented a quantity for an unidentified alternative: %+v", item)
		}
	}
}

func TestMarketObjectiveOverlapDoesNotTurnFixedDemandIntoAny(t *testing.T) {
	for _, otherType := range []string{"giveItem", "plantItem", "mark", "buildWeapon"} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%v", otherType, reverse), func(t *testing.T) {
				tracked, quests, hideout := inputs()
				quests.Quests = []market.Quest{{QuestID: "quest"}}
				tracked.Items = []market.TrackedItem{{ItemID: "a", RequiredQuantity: 1, RemainingQuantity: 1, Sources: []market.TrackedSource{source("quest", 1, false)}}}
				objectives := []tarkovdev.Objective{
					{ID: "fixed", Type: otherType, ItemIDs: []string{"a"}, Count: 1},
					{ID: "any", Type: "giveItem", ItemIDs: []string{"a", "b"}, Count: 2},
				}
				if reverse {
					objectives[0], objectives[1] = objectives[1], objectives[0]
				}
				merged, err := Merge(tracked, quests, hideout, tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest", Objectives: objectives}}})
				if err != nil {
					t.Fatal(err)
				}
				item := requireItem(t, merged, "a")
				if len(merged.Items) != 1 || len(item.Sources) != 1 || item.RequiredQuantity != 1 || item.Sources[0].GroupID != "" || item.Sources[0].Alternatives != 0 {
					t.Fatalf("ambiguous Market objective was assigned to Any: %+v", merged)
				}
			})
		}
	}
}

func TestOverlappingDevObjectivesKeepIndependentGroups(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "quest"}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest", Objectives: []tarkovdev.Objective{
		{ID: "one", Type: "giveItem", ItemIDs: []string{"a", "b"}, Count: 1},
		{ID: "two", Type: "giveItem", ItemIDs: []string{"a", "c"}, Count: 1},
	}}}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	a := requireItem(t, merged, "a")
	if len(merged.Items) != 3 || len(a.Sources) != 2 || a.RequiredQuantity != 2 || a.Sources[0].GroupID == a.Sources[1].GroupID || a.Sources[0].Alternatives != 2 || a.Sources[1].Alternatives != 2 {
		t.Fatalf("unambiguous dev objective groups were lost: %+v", merged)
	}
}

func TestNonRequirementObjectivesDoNotBlockMarketAnyMapping(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "quest"}}
	tracked.Items = []market.TrackedItem{{ItemID: "a", RequiredQuantity: 2, RemainingQuantity: 2, Sources: []market.TrackedSource{source("quest", 2, false)}}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest", Objectives: []tarkovdev.Objective{
		{ID: "find", Type: "findItem", ItemIDs: []string{"a", "b"}, Count: 2},
		{ID: "optional", Type: "giveItem", ItemIDs: []string{"a"}, Count: 1, Optional: true},
		{ID: "use-unknown", Type: "useItem", ItemIDs: []string{"a"}, Count: 1},
		{ID: "any", Type: "giveItem", ItemIDs: []string{"a", "b"}, Count: 2},
	}}}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 2 || requireItem(t, merged, "a").Sources[0].GroupID != "quest:quest:any" || requireItem(t, merged, "b").Sources[0].Alternatives != 2 {
		t.Fatalf("find/optional/unclassified use blocked physical Any requirement: %+v", merged)
	}
}

func TestMarkAlternativesDoesNotOverwriteExistingGroup(t *testing.T) {
	q := market.Quest{QuestID: "quest"}
	snapshot := Snapshot{Items: []Item{{ItemID: "a", Sources: []Source{{TrackedSource: source("quest", 2, false), Origin: OriginMarket, GroupID: "first", Alternatives: 2}}}}}
	before := Clone(snapshot)
	base := source("quest", 3, true)
	if markAlternatives(&snapshot, map[string][]int{"a": {0}}, map[string]market.Quest{"quest": q}, "quest", []string{"a", "b"}, "second", map[string]int{"a": 1, "b": 1}, &base) {
		t.Fatal("existing group was reassigned")
	}
	if !reflect.DeepEqual(before, snapshot) || base.Amount != 3 || !base.FIR {
		t.Fatalf("failed mapping mutated snapshot or authority: %+v %+v", snapshot, base)
	}
}

func TestMarketMultipleSourcesForSameCandidateStayUnmapped(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "quest"}}
	tracked.Items = []market.TrackedItem{{ItemID: "a", RequiredQuantity: 4, RemainingQuantity: 4, Sources: []market.TrackedSource{source("quest", 2, false), source("quest", 2, false)}}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest", Objectives: []tarkovdev.Objective{{ID: "any", Type: "giveItem", ItemIDs: []string{"a", "b"}, Count: 2}}}}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	a := requireItem(t, merged, "a")
	if len(merged.Items) != 1 || len(a.Sources) != 2 || a.RequiredQuantity != 4 {
		t.Fatalf("ambiguous Market sources were replaced by a candidate group: %+v", merged)
	}
	for _, source := range a.Sources {
		if source.GroupID != "" || source.Alternatives != 0 {
			t.Fatalf("ambiguous raw source received objective group: %+v", source)
		}
	}
}

func TestCompletedDevHideoutFixedQuantityKeepsCompletedCount(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "quest-b"}}
	hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "Workbench_2", ModuleID: "workbench", Level: 2, Completed: true}}
	dev := tarkovdev.Catalog{
		Tasks:   []tarkovdev.Task{{ID: "quest-b", Objectives: []tarkovdev.Objective{{ID: "give", Type: "giveItem", ItemIDs: []string{"bolt"}, Count: 4}}}},
		Hideout: []tarkovdev.Station{{ID: "workbench", Levels: []tarkovdev.Level{{Level: 2, Requirements: []tarkovdev.ItemRequirement{{ID: "wb-bolt", ItemID: "bolt", Count: 3}}}}}},
	}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, merged, "bolt")
	_, _, completed := sourceCounts(item)
	if item.RequiredQuantity != 4 || completed != 3 || item.Sources[1].Type != "hideout" || !item.Sources[1].Completed || item.Sources[1].Amount != 3 {
		t.Fatalf("completed fixed hideout quantity was lost or consumed quest requirement: %+v", item)
	}
}

func TestFiftyFiveAlternativesShareOneGoalAndCompletionDoesNotInventSpent(t *testing.T) {
	tracked, quests, hideout := inputs()
	ids := make([]string, 56)
	for i := range ids {
		ids[i] = fmt.Sprintf("alternative-%d", i)
	}
	quests.Quests = []market.Quest{{QuestID: "q1"}}
	marketSource := source("q1", 3, true)
	marketSource.RemainingRequiredQuantity = 2
	tracked.Items = []market.TrackedItem{{ItemID: ids[0], Sources: []market.TrackedSource{marketSource}}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "q1", Objectives: []tarkovdev.Objective{{ID: "any3", Type: "giveItem", ItemIDs: ids, Count: 3}}}}}
	before, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 56 {
		t.Fatalf("expected 55 missing alternatives, got %d rows", len(before.Items))
	}
	for _, item := range before.Items {
		if len(item.Sources) != 1 || item.Sources[0].GroupID != "quest:q1:any3" || item.Sources[0].Alternatives != 56 || !item.Sources[0].FIR || item.Sources[0].Amount != 3 || item.Sources[0].GroupAmount != 3 || item.RequiredQuantity != 2 {
			t.Fatalf("independent or changed alternative: %+v", item)
		}
	}
	quests.Quests[0].Completed = true
	after, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	var completed int64
	for _, item := range after.Items {
		if item.RequiredQuantity != 0 || item.RemainingQuantity != 0 || !item.Sources[0].Completed {
			t.Fatalf("completed alternative remained active: %+v", item)
		}
		_, _, count := sourceCounts(item)
		completed += count
	}
	if completed != 3 {
		t.Fatalf("completion was invented for every alternative: total=%d", completed)
	}
}

func TestFailedAndUnmappedTasksAreNotActive(t *testing.T) {
	for _, status := range []string{"failed", "unreachable", "canceled", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			tracked, quests, hideout := inputs()
			quests.Quests = []market.Quest{{QuestID: "q1", Status: status}}
			dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{
				{ID: "q1", Objectives: []tarkovdev.Objective{{ID: "give", Type: "giveItem", ItemIDs: []string{"salewa"}, Count: 3}}},
				{ID: "unknown-q", Objectives: []tarkovdev.Objective{{ID: "unknown-give", Type: "giveItem", ItemIDs: []string{"unknown-item"}, Count: 99}}},
			}}
			merged, err := Merge(tracked, quests, hideout, dev)
			if err != nil {
				t.Fatal(err)
			}
			if len(merged.Items) != 1 || merged.Items[0].RequiredQuantity != 0 || !merged.Items[0].Sources[0].Canceled {
				t.Fatalf("inactive/unmatched quest was activated: %+v", merged)
			}
		})
	}
}

func TestPhysicalQuestItemObjectivesIncludePlantMarkersAndWeapon(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "q1"}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "q1", Objectives: []tarkovdev.Objective{
		{ID: "plant", Type: "plantItem", ItemIDs: []string{"wifi-camera"}, Count: 3},
		{ID: "mark", Type: "mark", ItemIDs: []string{"ms2000-marker"}, Count: 4},
		{ID: "gun", Type: "buildWeapon", ItemIDs: []string{"mp133"}, Count: 1},
		{ID: "find-gun", Type: "findItem", ItemIDs: []string{"mp133"}, Count: 1},
		{ID: "use", Type: "useItem", ItemIDs: []string{"water"}, Count: 20},
	}}}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 3 || requireItem(t, merged, "wifi-camera").RequiredQuantity != 3 || requireItem(t, merged, "ms2000-marker").RequiredQuantity != 4 || requireItem(t, merged, "mp133").RequiredQuantity != 1 {
		t.Fatalf("physical requirements were omitted, or use/find events became extra items: %+v", merged)
	}
}

func TestSingleUseVerifiedFlaresAreAlternativeItems(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "flare-quest"}}
	red := []string{"62178c4d4ecf221597654e3d", "62389ba9a63f32501b1b4451"}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "flare-quest", Objectives: []tarkovdev.Objective{{ID: "red-flare", Type: "useItem", ItemIDs: red, Count: 1}}}}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("single-use flare alternatives were omitted: %+v", merged)
	}
	for _, item := range merged.Items {
		if item.RequiredQuantity != 1 || len(item.Sources) != 1 || item.Sources[0].GroupID != "quest:flare-quest:red-flare" || item.Sources[0].Alternatives != 2 {
			t.Fatalf("flare alternatives became independent requirements: %+v", item)
		}
	}
	for _, objective := range []tarkovdev.Objective{
		{ID: "count-two", Type: "useItem", ItemIDs: red, Count: 2},
		{ID: "mixed", Type: "useItem", ItemIDs: []string{red[0], "unknown-item"}, Count: 1},
		{ID: "ordinary-use", Type: "useItem", ItemIDs: []string{"water"}, Count: 1},
	} {
		dev.Tasks[0].Objectives = []tarkovdev.Objective{objective}
		merged, err := Merge(tracked, quests, hideout, dev)
		if err != nil || len(merged.Items) != 0 {
			t.Fatalf("generic or uncertain use event became a purchase quantity: %+v %v", merged, err)
		}
	}
}

func TestHideoutUsesExactLevelAndKeepsQuestRequirement(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "q1"}}
	tracked.Items = []market.TrackedItem{{ItemID: "bolt", Sources: []market.TrackedSource{source("q1", 2, false), {Type: "hideout", UpgradeKey: "Workbench_2", Amount: 4, RemainingRequiredQuantity: 4}}}}
	hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "Workbench_1", ModuleID: "workbench", Level: 1, Prebuilt: true}, {UpgradeKey: "Workbench_2", ModuleID: "workbench", Level: 2}}
	dev := tarkovdev.Catalog{Hideout: []tarkovdev.Station{{ID: "workbench", Name: "Workbench", NormalizedName: "workbench", Levels: []tarkovdev.Level{
		{Level: 1, Requirements: []tarkovdev.ItemRequirement{{ID: "wb1-bolt", ItemID: "bolt", Count: 1}}},
		{Level: 2, Requirements: []tarkovdev.ItemRequirement{{ID: "wb2-bolt", ItemID: "bolt", Count: 100}, {ID: "wb2-screw", ItemID: "screw", Count: 3}}},
		{Level: 3, Requirements: []tarkovdev.ItemRequirement{{ID: "wb3-motor", ItemID: "motor", Count: 99}}},
	}}}}
	before, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 2 || requireItem(t, before, "bolt").RequiredQuantity != 6 || requireItem(t, before, "screw").RequiredQuantity != 3 {
		t.Fatalf("wrong hideout level/primary quantity: %+v", before)
	}
	hideout.Modules[1].Completed = true
	after, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if requireItem(t, after, "bolt").RequiredQuantity != 2 || requireItem(t, after, "screw").RequiredQuantity != 0 {
		t.Fatalf("completion removed quest or kept hideout: %+v", after)
	}
}

func TestNullHideoutIDRequiresUniqueExactNameOrKey(t *testing.T) {
	tracked, quests, hideout := inputs()
	hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "Workbench_2", Name: "Workbench", Level: 2}}
	station := tarkovdev.Station{ID: "workbench", Name: "Workbench", NormalizedName: "workbench", Levels: []tarkovdev.Level{{Level: 2, Requirements: []tarkovdev.ItemRequirement{{ID: "bolt", ItemID: "bolt", Count: 3}}}}}
	merged, err := Merge(tracked, quests, hideout, tarkovdev.Catalog{Hideout: []tarkovdev.Station{station}})
	if err != nil || len(merged.Items) != 1 {
		t.Fatalf("exact fallback mapping failed: %+v, %v", merged, err)
	}
	merged, err = Merge(tracked, quests, hideout, tarkovdev.Catalog{Hideout: []tarkovdev.Station{station, station}})
	if err != nil || len(merged.Items) != 0 {
		t.Fatalf("ambiguous station was mapped: %+v, %v", merged, err)
	}
}

func TestOpaqueHideoutKeysKeepLevelAndCompletionWithQuestDemand(t *testing.T) {
	for _, name := range []string{"Water Collector", "Hall of Fame", "Rest Space", "Pihenő Szoba"} {
		for _, useModuleID := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/moduleID=%t", name, useModuleID), func(t *testing.T) {
				tracked, quests, hideout := inputs()
				quests.Quests = []market.Quest{{QuestID: "q1"}}
				moduleID := "module-id"
				if !useModuleID {
					moduleID = ""
				}
				key := name + "_2"
				hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: key, ModuleID: moduleID, Name: name, Level: 2}}
				dev := tarkovdev.Catalog{
					Tasks: []tarkovdev.Task{{ID: "q1", Objectives: []tarkovdev.Objective{{ID: "give", Type: "giveItem", ItemIDs: []string{"bolt"}, Count: 4}}}},
					Hideout: []tarkovdev.Station{{ID: "module-id", Name: name, NormalizedName: strings.ReplaceAll(strings.ToLower(name), " ", "-"), Levels: []tarkovdev.Level{
						{Level: 1, Requirements: []tarkovdev.ItemRequirement{{ID: "wrong-level", ItemID: "wrong-item", Count: 99}}},
						{Level: 2, Requirements: []tarkovdev.ItemRequirement{{ID: "level-two", ItemID: "bolt", Count: 3}}},
					}}},
				}
				before, err := Merge(tracked, quests, hideout, dev)
				if err != nil {
					t.Fatal(err)
				}
				item := requireItem(t, before, "bolt")
				if len(before.Items) != 1 || item.RequiredQuantity != 7 || item.Sources[1].UpgradeKey != key {
					t.Fatalf("opaque key/level was lost: %+v", before)
				}
				hideout.Modules[0].Completed = true
				after, err := Merge(tracked, quests, hideout, dev)
				if err != nil {
					t.Fatal(err)
				}
				item = requireItem(t, after, "bolt")
				if item.RequiredQuantity != 4 || !item.Sources[1].Completed || item.Sources[1].RemainingRequiredQuantity != 0 || item.Sources[1].UpgradeKey != key || item.Sources[0].Completed {
					t.Fatalf("completion failed or consumed the quest demand: %+v", item)
				}
			})
		}
	}
}

func TestUpgradeKeysAreOpaqueButBSGIDsRemainStrict(t *testing.T) {
	for _, key := range []string{"Water Collector_1", "Hall of Fame_2", "Rest Space_1", "Pihenő Szoba_1"} {
		if !validUpgradeKey(key) {
			t.Fatalf("valid opaque key rejected: %q", key)
		}
		if validID(key) {
			t.Fatalf("opaque key weakened item/quest ID validation: %q", key)
		}
	}
	for _, key := range []string{"", " ", " key_1", "key_1 ", "bad\nkey", "bad\tkey", "bad\x00key", "bad\u0085key", strings.Repeat("a", 129), string([]byte{0xff})} {
		if validUpgradeKey(key) {
			t.Fatalf("invalid opaque key accepted: %q", key)
		}
	}
	tracked, quests, hideout := inputs()
	tracked.Items = []market.TrackedItem{{ItemID: "valid-item", Sources: []market.TrackedSource{{Type: "hideout", UpgradeKey: "Water Collector_1", Amount: 2, RemainingRequiredQuantity: 2}}}}
	hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "Water Collector_1", Level: 1, Completed: true}}
	merged, err := Merge(tracked, quests, hideout, tarkovdev.Catalog{})
	if err != nil || !merged.Items[0].Sources[0].Completed || merged.Items[0].RequiredQuantity != 0 {
		t.Fatalf("raw Market source did not match completed opaque key: %+v %v", merged, err)
	}
	tracked.Items[0].Sources[0].UpgradeKey = "bad\nkey"
	if _, err := Merge(tracked, quests, hideout, tarkovdev.Catalog{}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("invalid source upgrade key accepted: %v", err)
	}
	tracked.Items[0].Sources = []market.TrackedSource{source("quest with spaces", 1, false)}
	if _, err := Merge(tracked, quests, hideout, tarkovdev.Catalog{}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("quest ID accepted opaque key syntax: %v", err)
	}
}

func TestMergeRejectsMixedCharacterModeRevisionAndQuantityOverflow(t *testing.T) {
	for _, change := range []func(*market.QuestSnapshot){func(q *market.QuestSnapshot) { q.CharUID = "other" }, func(q *market.QuestSnapshot) { q.GameMode = "PVE" }, func(q *market.QuestSnapshot) { q.Revision = "rev-2" }} {
		tracked, quests, hideout := inputs()
		change(&quests)
		if _, err := Merge(tracked, quests, hideout, tarkovdev.Catalog{}); !errors.Is(err, ErrSnapshotMismatch) {
			t.Fatalf("mixed snapshot accepted: %v", err)
		}
	}
	tracked, quests, hideout := inputs()
	tracked.Items = []market.TrackedItem{{ItemID: "bolt", Sources: []market.TrackedSource{source("q1", MaxQuantity, false), source("q2", 1, false)}}}
	if _, err := Merge(tracked, quests, hideout, tarkovdev.Catalog{}); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("overflow accepted: %v", err)
	}
}

func TestVariantsKeepTheirSeparateMarketStockAndSources(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "q1"}, {QuestID: "q2"}}
	tracked.Items = []market.TrackedItem{{ItemID: "canister", Name: "Full", AvailableQuantity: 2, Sources: []market.TrackedSource{source("q1", 4, false)}}, {ItemID: "canister", Name: "Empty", AvailableQuantity: 7, Sources: []market.TrackedSource{source("q2", 9, false)}}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "q1", Objectives: []tarkovdev.Objective{{ID: "give", Type: "giveItem", ItemIDs: []string{"canister"}, Count: 4}}}}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 2 || merged.Items[0].AvailableQuantity != 2 || merged.Items[1].AvailableQuantity != 7 || merged.Items[0].Name != "Full" || merged.Items[1].Name != "Empty" || !reflect.DeepEqual(merged.Items[0].Sources[0].TrackedSource, tracked.Items[0].Sources[0]) {
		t.Fatalf("variant rows were merged: %+v", merged)
	}
}
