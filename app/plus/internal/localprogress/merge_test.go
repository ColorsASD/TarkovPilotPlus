package localprogress

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
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
func marketGroup(quest, requirement string, count int64, fir bool, ids ...string) market.TrackedGroup {
	return market.TrackedGroup{QuestID: quest, QuestName: quest, RequirementID: requirement, Name: "Any accepted item", Amount: count, RequiredQuantity: count, RemainingQuantity: count, FIR: fir, ItemIDs: ids}
}

// https://tarkov-market.com/dev/api: a requiredQuantity a fennmaradó igény;
// a questGroup forrás nem növeli a jelölt tárgy saját requiredQuantity értékét.
func partialMarketGroupInputs(fir bool) (market.TrackedSnapshot, market.QuestSnapshot, market.HideoutSnapshot) {
	tracked, quests, hideout := inputs()
	group := marketGroup("quest-alias", "medical-supplies", 5, fir, "salewa", "cms", "ifak")
	group.RequiredQuantity, group.AvailableQuantity, group.RemainingQuantity = 2, 1, 1
	tracked.Groups = []market.TrackedGroup{group}
	for _, id := range group.ItemIDs {
		tracked.Items = append(tracked.Items, market.TrackedItem{ItemID: id, Sources: []market.TrackedSource{{
			Type: "questGroup", QuestID: group.QuestID, RequirementID: group.RequirementID,
			Amount: group.Amount, FIR: fir,
		}}})
	}
	tracked.Items[0].AvailableQuantity, tracked.Items[0].SpentQuantity = 1, 3
	return tracked, quests, hideout
}

func mergeAndAllocate(t *testing.T, tracked market.TrackedSnapshot, quests market.QuestSnapshot, hideout market.HideoutSnapshot) Snapshot {
	t.Helper()
	merged, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Allocate(merged)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestMarketOnlyDoesNotInventMissingQuestOrHideoutItems(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "quest-with-no-tracked-items"}}
	hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "Workbench_2", Level: 2}}
	result, err := Merge(tracked, quests, hideout)
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("non-Market requirement was invented: %+v %v", result, err)
	}
}
func TestMarketGroupsProjectSharedRequirementAndPreserveRawCounters(t *testing.T) {
	tracked, quests, hideout := inputs()
	tracked.Groups = []market.TrackedGroup{marketGroup("quest", "group", 3, true, "salewa", "cms")}
	raw := market.TrackedSource{Type: "questGroup", QuestID: "quest", RequirementID: "group", Name: "Quest name", GroupName: "Medkits", Amount: 3, FIR: true}
	tracked.Items = []market.TrackedItem{{ItemID: "salewa", AvailableQuantity: 2, SpentQuantity: 7, Sources: []market.TrackedSource{raw}}, {ItemID: "cms", Sources: []market.TrackedSource{raw}}}
	before := tracked.Items[0]
	result := mergeAndAllocate(t, tracked, quests, hideout)
	item := requireItem(t, result, "salewa")
	if item.MarketRequiredQuantity != 0 || item.MarketAvailableQuantity != 2 || item.AvailableQuantity != 2 || item.SpentQuantity != 7 || !reflect.DeepEqual(tracked.Items[0], before) {
		t.Fatalf("raw Market state changed: %+v", item)
	}
	if item.RequiredQuantity != 3 || len(item.Sources) != 1 || item.Sources[0].GroupID != "quest:quest:group" || item.Sources[0].Alternatives != 2 || item.Sources[0].GroupAmount != 3 || item.Sources[0].GroupRemainingQuantity != 1 || item.Sources[0].AllocatedQuantity != 2 || item.Sources[0].Origin != OriginMarket {
		t.Fatalf("Market Any projection lost: %+v", item)
	}
	regular, fir, err := AddOptions(result, "cms")
	if err != nil || regular || !fir {
		t.Fatalf("wrong quality eligibility: %t %t %v", regular, fir, err)
	}
}
func TestMarketPartialGroupProjectsOnlyTheOutstandingSharedRequirement(t *testing.T) {
	tracked, quests, hideout := partialMarketGroupInputs(true)
	result := mergeAndAllocate(t, tracked, quests, hideout)
	var allocated int64
	for i, raw := range tracked.Items {
		item := requireItem(t, result, raw.ItemID)
		if raw.RequiredQuantity != 0 || raw.RemainingQuantity != 0 || len(raw.Sources) != 1 || raw.Sources[0].Type != "questGroup" || raw.Sources[0].RemainingRequiredQuantity != 0 {
			t.Fatalf("Merge changed the raw candidate's independent requirement: %+v", raw)
		}
		if item.RequiredQuantity != 2 || item.MarketRequiredQuantity != 0 || item.AvailableQuantity != raw.AvailableQuantity || item.SpentQuantity != raw.SpentQuantity || len(item.Sources) != 1 {
			t.Fatalf("partial group restored its original amount or changed stock: %+v", item)
		}
		source := item.Sources[0]
		if source.Amount != 5 || source.GroupAmount != 5 || source.RemainingRequiredQuantity != 2 || source.GroupID != "quest:quest-alias:medical-supplies" || source.Alternatives != 3 || !source.AllocationKnown || source.GroupRemainingQuantity != 1 {
			t.Fatalf("candidate %d lost the one shared outstanding requirement: %+v", i, source)
		}
		allocated += source.AllocatedQuantity
	}
	missing, err := collectionDeficit(result)
	if err != nil || allocated != 1 || missing != 1 {
		t.Fatalf("one stock piece or one group deficit was counted per candidate: allocated=%d missing=%d err=%v", allocated, missing, err)
	}
	if tracked.Groups[0].Amount != 5 || tracked.Groups[0].RequiredQuantity != 2 {
		t.Fatal("projection changed the raw Market group")
	}
}

func TestMarketPartialGroupCompletionAndQuestAliasCloseEveryCandidate(t *testing.T) {
	for _, completedBy := range []string{"group", "quest-alias"} {
		t.Run(completedBy, func(t *testing.T) {
			tracked, quests, hideout := partialMarketGroupInputs(true)
			if completedBy == "group" {
				tracked.Groups[0].Completed = true
				tracked.Groups[0].RequiredQuantity, tracked.Groups[0].RemainingQuantity = 0, 0
			} else {
				quests.Quests = []market.Quest{{QuestID: "canonical-quest", QuestIDAliases: []string{"quest-alias"}, Completed: true, Status: "completed"}}
			}
			result := mergeAndAllocate(t, tracked, quests, hideout)
			for _, raw := range tracked.Items {
				item := requireItem(t, result, raw.ItemID)
				if item.RequiredQuantity != 0 || item.RemainingQuantity != 0 || item.AvailableQuantity != raw.AvailableQuantity || item.SpentQuantity != raw.SpentQuantity || len(item.Sources) != 1 {
					t.Fatalf("completion retained a requirement or invented consumption: %+v", item)
				}
				source := item.Sources[0]
				if !source.Completed || source.Amount != 5 || source.GroupAmount != 5 || source.RemainingRequiredQuantity != 0 || source.AllocatedQuantity != 0 {
					t.Fatalf("completion through %s lost or reactivated its source: %+v", completedBy, source)
				}
				assertAddOptions(t, result, item.ItemID, false, false)
			}
		})
	}
}

func TestMarketGroupsShareStockWithoutCountingTheSameFiveTwice(t *testing.T) {
	tracked, quests, hideout := inputs()
	tracked.Groups = []market.TrackedGroup{marketGroup("first-in-line", "any3", 3, true, "salewa", "cms"), marketGroup("pets", "any5", 5, true, "salewa", "cms")}
	tracked.Items = []market.TrackedItem{{ItemID: "salewa", AvailableQuantity: 5, Sources: []market.TrackedSource{}}, {ItemID: "cms", Sources: []market.TrackedSource{}}}
	result := mergeAndAllocate(t, tracked, quests, hideout)
	item := requireItem(t, result, "salewa")
	var allocated, missing int64
	for _, s := range item.Sources {
		allocated += s.AllocatedQuantity
		missing += s.GroupRemainingQuantity
	}
	if allocated != 5 || missing != 3 {
		t.Fatalf("five items covered more than five requirements: allocated=%d missing=%d", allocated, missing)
	}
	_, fir, err := AddOptions(result, "cms")
	if err != nil || !fir {
		t.Fatalf("open Any deficit was not collectable: %t %v", fir, err)
	}
	tracked.Items[0].AvailableQuantity = 8
	result = mergeAndAllocate(t, tracked, quests, hideout)
	regular, fir, err := AddOptions(result, "salewa")
	if err != nil || regular || fir {
		t.Fatalf("covered Market groups allowed overcollection: %t %t %v", regular, fir, err)
	}
}
func TestMarketFixedRequirementReservesStockBeforeItsAnyGroup(t *testing.T) {
	tracked, quests, hideout := inputs()
	tracked.Groups = []market.TrackedGroup{marketGroup("any", "medkits", 3, false, "salewa", "cms")}
	tracked.Items = []market.TrackedItem{{ItemID: "salewa", RequiredQuantity: 2, AvailableQuantity: 2, Sources: []market.TrackedSource{source("fixed", 2, false)}}, {ItemID: "cms", Sources: []market.TrackedSource{}}}
	result := mergeAndAllocate(t, tracked, quests, hideout)
	item := requireItem(t, result, "salewa")
	if item.RequiredQuantity != 5 || item.MarketRequiredQuantity != 2 || item.Sources[1].GroupRemainingQuantity != 3 || item.Sources[1].AllocatedQuantity != 0 {
		t.Fatalf("fixed stock was duplicated by Any: %+v", item)
	}
}
func TestMarketCompletedGroupsAndSourcesDoNotConsumeStock(t *testing.T) {
	tracked, quests, hideout := inputs()
	tracked.Groups = []market.TrackedGroup{marketGroup("quest", "any", 3, true, "salewa", "cms")}
	tracked.Items = []market.TrackedItem{{ItemID: "salewa", AvailableQuantity: 5, SpentQuantity: 2, Sources: []market.TrackedSource{source("fixed", 1, false)}}}
	quests.Quests = []market.Quest{{QuestID: "quest", Completed: true}, {QuestID: "fixed", Completed: true}}
	result := mergeAndAllocate(t, tracked, quests, hideout)
	item := requireItem(t, result, "salewa")
	if item.RequiredQuantity != 0 || item.AvailableQuantity != 5 || item.SpentQuantity != 2 || len(item.Sources) != 2 || !item.Sources[0].Completed || !item.Sources[1].Completed || item.Sources[1].GroupAmount != 3 {
		t.Fatalf("completion invented consumption or lost source: %+v", item)
	}
	for _, s := range item.Sources {
		if s.RemainingRequiredQuantity != 0 {
			t.Fatalf("completed source stayed active: %+v", s)
		}
	}
	regular, fir, err := AddOptions(result, "cms")
	if err != nil || regular || fir {
		t.Fatalf("completed group allowed Add: %t %t %v", regular, fir, err)
	}
}
func TestMarketGroupCanSupplyMissingCandidateRowsAndRequirementLinks(t *testing.T) {
	tracked, quests, hideout := inputs()
	tracked.Groups = []market.TrackedGroup{marketGroup("quest", "group", 2, false, "a", "b")}
	tracked.Items = []market.TrackedItem{{ItemID: "a", AvailableQuantity: 1, Sources: []market.TrackedSource{}}}
	result := mergeAndAllocate(t, tracked, quests, hideout)
	if len(result.Items) != 2 || requireItem(t, result, "b").Sources[0].Origin != OriginMarket || requireItem(t, result, "a").Sources[0].GroupRemainingQuantity != 1 {
		t.Fatalf("explicit Market group members were lost: %+v", result)
	}
}
func TestAmbiguousMarketVariantsAreNotAssignedToAnAnyGroup(t *testing.T) {
	tracked, quests, hideout := inputs()
	tracked.Groups = []market.TrackedGroup{marketGroup("quest", "group", 2, false, "a", "b")}
	tracked.Items = []market.TrackedItem{{ItemID: "a", AvailableQuantity: 1, Sources: []market.TrackedSource{source("quest", 2, false)}}, {ItemID: "a", Sources: []market.TrackedSource{source("quest", 2, false)}}}
	result, err := Merge(tracked, quests, hideout)
	if err != nil || len(result.Items) != 3 {
		t.Fatalf("variant rows changed: %+v %v", result, err)
	}
	for _, item := range result.Items {
		if item.ItemID == "a" && item.Sources[0].GroupID != "" {
			t.Fatalf("ambiguous variant was assigned: %+v", item)
		}
	}
	if requireItem(t, result, "b").Sources[0].GroupID != "quest:quest:group" {
		t.Fatal("ambiguous candidate discarded the usable Market group")
	}
}
func TestMarketOnlyMergeRejectsMismatchedAndInvalidSnapshots(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Revision = "other"
	if _, err := Merge(tracked, quests, hideout); !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("mismatched revision accepted: %v", err)
	}
	quests.Revision = tracked.Revision
	for _, group := range []market.TrackedGroup{marketGroup("quest", "dup", 1, false, "a", "a"), marketGroup("quest", "invalid", 2, false, ""), marketGroup("quest", "negative", -1, false, "a", "b")} {
		tracked.Groups = []market.TrackedGroup{group}
		if _, err := Merge(tracked, quests, hideout); err == nil {
			t.Fatalf("invalid group accepted: %+v", group)
		}
	}
}
