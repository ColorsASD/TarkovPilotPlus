package localprogress

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func unidentifiedQuestGroupInputs() (market.TrackedSnapshot, market.QuestSnapshot, market.HideoutSnapshot) {
	tracked, quests, hideout := inputs()
	tracked.Groups = []market.TrackedGroup{
		marketGroup("", "event-first", 2, true, "salewa", "ifak"),
		marketGroup("", "event-second", 2, true, "salewa", "ifak"),
		marketGroup("known-quest", "event-first", 2, true, "salewa", "ifak"),
	}
	for i := range tracked.Groups {
		tracked.Groups[i].QuestName = "Halloween: Tightening the Quarantine"
	}
	tracked.Groups[0].AvailableQuantity, tracked.Groups[0].RemainingQuantity = 1, 1
	for _, id := range []string{"salewa", "ifak"} {
		item := market.TrackedItem{ItemID: id, Sources: []market.TrackedSource{}}
		for _, group := range tracked.Groups {
			item.Sources = append(item.Sources, market.TrackedSource{
				Type: "questGroup", QuestID: group.QuestID, RequirementID: group.RequirementID,
				Name: group.QuestName, GroupName: group.Name, Amount: group.Amount, FIR: group.FIR,
			})
		}
		tracked.Items = append(tracked.Items, item)
	}
	tracked.Items[1].AvailableQuantity, tracked.Items[1].SpentQuantity = 1, 4
	quests.Quests = []market.Quest{
		{QuestID: "known-quest", Name: tracked.Groups[0].QuestName, Status: "available"},
		{QuestID: "completed-other-quest", Name: tracked.Groups[0].QuestName, Status: "completed", Completed: true},
	}
	return tracked, quests, hideout
}

func TestMarketGroupsWithoutQuestIDKeepIndependentRequirementsAndStock(t *testing.T) {
	tracked, quests, hideout := unidentifiedQuestGroupInputs()
	wantTracked, wantQuests, _ := unidentifiedQuestGroupInputs()
	merged, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged.MarketGroups, tracked.Groups) {
		t.Fatalf("groups were merged, renamed or closed by a matching name: %+v", merged.MarketGroups)
	}
	wantIDs := []string{"quest::event-first", "quest::event-second", "quest:known-quest:event-first"}
	for own := int64(0); own <= 5; own++ {
		candidate := Clone(merged)
		candidate.Items[0].AvailableQuantity = own
		before := Clone(candidate)
		result := allocateForTest(t, candidate)
		var allocated int64
		for index, item := range result.Items {
			if len(item.Sources) != len(wantIDs) || item.RequiredQuantity != 6 || item.MarketRequiredQuantity != 0 ||
				item.AvailableQuantity != candidate.Items[index].AvailableQuantity || item.MarketAvailableQuantity != tracked.Items[index].AvailableQuantity || item.SpentQuantity != tracked.Items[index].SpentQuantity {
				t.Fatalf("group projection changed requirements or stock: %+v", item)
			}
			for groupIndex, id := range wantIDs {
				source := requireAllocationSource(t, item, id)
				group := tracked.Groups[groupIndex]
				if source.QuestID != group.QuestID || source.RequirementID != group.RequirementID || source.Completed || source.Canceled ||
					!source.AllocationKnown || source.Alternatives != 2 || !source.MarketGroupProgressKnown || source.MarketGroupAvailableQuantity != group.AvailableQuantity ||
					!MarketGroupSourceMatches(item, source, group) {
					t.Fatalf("independent group identity, state or reported progress changed: %+v", source)
				}
				allocated += source.AllocatedQuantity
			}
		}
		if allocated != own+1 {
			t.Fatalf("stock was counted more than once: own=%d allocated=%d", own, allocated)
		}
		item := requireItem(t, result, "salewa")
		if !item.CollectionCalculated || item.CollectionRequiredQuantity != 5 || item.CollectionRemainingQuantity != 5-own {
			t.Fatalf("independent event groups lost their combined collection limit: %+v", item)
		}
		assertAddAllowed(t, result, "salewa", own < 5)
		if !reflect.DeepEqual(candidate, before) {
			t.Fatal("allocation changed the caller's snapshot")
		}
	}
	if !reflect.DeepEqual(tracked, wantTracked) || !reflect.DeepEqual(quests, wantQuests) {
		t.Fatal("merge changed raw Market groups, source identities or quest metadata")
	}
}

func TestMarketGroupWithoutQuestIDKeepsItsOwnCompletion(t *testing.T) {
	tracked, quests, hideout := unidentifiedQuestGroupInputs()
	tracked.Groups[0].Completed = true
	tracked.Groups[0].RequiredQuantity, tracked.Groups[0].RemainingQuantity = 0, 0
	result := mergeAndAllocate(t, tracked, quests, hideout)
	for index, item := range result.Items {
		closed := requireAllocationSource(t, item, "quest::event-first")
		open := requireAllocationSource(t, item, "quest::event-second")
		known := requireAllocationSource(t, item, "quest:known-quest:event-first")
		if !closed.Completed || closed.Amount != 2 || closed.RemainingRequiredQuantity != 0 || closed.AllocatedQuantity != 0 ||
			open.Completed || known.Completed || item.RequiredQuantity != 4 || item.AvailableQuantity != tracked.Items[index].AvailableQuantity || item.SpentQuantity != tracked.Items[index].SpentQuantity {
			t.Fatalf("completion affected an independent group or consumed stock: %+v", item)
		}
	}
}

func TestMarketGroupOptionalQuestIDStillRejectsInvalidIdentity(t *testing.T) {
	for _, name := range []string{"invalid-quest", "empty-requirement", "invalid-requirement", "duplicate-group"} {
		t.Run(name, func(t *testing.T) {
			tracked, quests, hideout := unidentifiedQuestGroupInputs()
			switch name {
			case "invalid-quest":
				tracked.Groups[0].QuestID = "invalid/quest"
			case "empty-requirement":
				tracked.Groups[0].RequirementID = ""
			case "invalid-requirement":
				tracked.Groups[0].RequirementID = "invalid/requirement"
			case "duplicate-group":
				tracked.Groups = append(tracked.Groups, tracked.Groups[0])
			}
			if _, err := Merge(tracked, quests, hideout); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("invalid group identity was accepted: %v", err)
			}
		})
	}
}
