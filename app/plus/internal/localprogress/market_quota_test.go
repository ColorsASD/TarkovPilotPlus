package localprogress

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func aquariusQuotaInputs() (market.TrackedSnapshot, market.QuestSnapshot, market.HideoutSnapshot) {
	tracked, quests, hideout := inputs()
	group := marketGroup("aquarius", "water", 3, false, "water", "ymxc")
	group.AvailableQuantity, group.RemainingQuantity = 1, 2
	tracked.Groups = []market.TrackedGroup{group}
	tracked.Items = []market.TrackedItem{
		{ItemID: "water", RequiredQuantity: 2, RemainingQuantity: 2, Sources: []market.TrackedSource{source("thrifty", 2, false)}},
		{ItemID: "ymxc", RequiredQuantity: 1, AvailableQuantity: 1, Sources: []market.TrackedSource{source("collector", 1, false)}},
	}
	return tracked, quests, hideout
}

func TestReportedMarketQuotaKeepsWaterTargetStableThroughConfirmedAdds(t *testing.T) {
	for water := int64(0); water <= 4; water++ {
		tracked, quests, hideout := aquariusQuotaInputs()
		tracked.Items[0].AvailableQuantity = water
		tracked.Groups[0].AvailableQuantity = min(3, water+1)
		tracked.Groups[0].RemainingQuantity = max(0, 3-tracked.Groups[0].AvailableQuantity)
		result := mergeAndAllocate(t, tracked, quests, hideout)
		item := requireItem(t, result, "water")
		if !item.CollectionCalculated || item.CollectionRequiredQuantity != 4 || item.CollectionRemainingQuantity != 4-water || item.AvailableQuantity != water || requireItem(t, result, "ymxc").AvailableQuantity != 1 {
			t.Fatalf("own water stock shrank or inflated its target at %d: %+v", water, item)
		}
		assertAddAllowed(t, result, "water", water < 4)
		if water == 0 && requireItem(t, result, "ymxc").CollectionRequiredQuantity != 4 {
			t.Fatal("YMXC own stock was subtracted from its target a second time")
		}
	}
}

func TestReportedMarketQuotaKeepsOwnPendingSeparateFromAPICounter(t *testing.T) {
	tracked, quests, hideout := aquariusQuotaInputs()
	snapshot, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	for pending := int64(0); pending <= 4; pending++ {
		candidate := Clone(snapshot)
		candidate.Items[0].AvailableQuantity = pending
		result := allocateForTest(t, candidate)
		item := requireItem(t, result, "water")
		if item.CollectionRequiredQuantity != 4 || item.CollectionRemainingQuantity != 4-pending || item.MarketAvailableQuantity != 0 {
			t.Fatalf("pending water changed its own target or confirmed baseline: %+v", item)
		}
		group := requireAllocationSource(t, item, "quest:aquarius:water")
		if group.MarketGroupAvailableQuantity != 1 {
			t.Fatal("pending stock invented an API group acknowledgment")
		}
		assertAddAllowed(t, result, "water", pending < 4)
	}
}

func TestReportedMarketQuotaCountsOtherPendingButDoesNotReserveItsFixedGoal(t *testing.T) {
	tracked, quests, hideout := aquariusQuotaInputs()
	snapshot, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Items[1].AvailableQuantity++
	for water := int64(0); water <= 3; water++ {
		candidate := Clone(snapshot)
		candidate.Items[0].AvailableQuantity = water
		result := allocateForTest(t, candidate)
		item := requireItem(t, result, "water")
		if item.CollectionRequiredQuantity != 3 || item.CollectionRemainingQuantity != 3-water {
			t.Fatalf("other candidate pending did not reduce the collection limit exactly once: %+v", item)
		}
		assertAddAllowed(t, result, "water", water < 3)
	}
}

func TestReportedMarketQuotaDoesNotUseOtherStockTwiceAcrossAnyGroups(t *testing.T) {
	tracked, quests, hideout := inputs()
	for _, quest := range []string{"first", "second"} {
		group := marketGroup(quest, "any", 2, false, "a", "b")
		group.AvailableQuantity, group.RemainingQuantity = 1, 1
		tracked.Groups = append(tracked.Groups, group)
	}
	tracked.Items = []market.TrackedItem{{ItemID: "a", Sources: []market.TrackedSource{}}, {ItemID: "b", AvailableQuantity: 1, Sources: []market.TrackedSource{}}}
	snapshot, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	for own := int64(0); own <= 3; own++ {
		candidate := Clone(snapshot)
		candidate.Items[0].AvailableQuantity = own
		before := Clone(candidate)
		result := allocateForTest(t, candidate)
		if target := requireItem(t, result, "a").CollectionRequiredQuantity; target != 3 {
			t.Fatalf("one other item covered two independent Any pieces: target=%d", target)
		}
		assertAddAllowed(t, result, "a", own < 3)
		if !reflect.DeepEqual(candidate, before) {
			t.Fatal("quota calculation changed the caller's stock or requirements")
		}
	}
}

func TestReportedMarketQuotaLimitsConfirmedContributionByRawAPIProgress(t *testing.T) {
	tracked, quests, hideout := aquariusQuotaInputs()
	tracked.Items[1].AvailableQuantity = 3
	tracked.Groups[0].AvailableQuantity = 1
	result := mergeAndAllocate(t, tracked, quests, hideout)
	if item := requireItem(t, result, "water"); item.CollectionRequiredQuantity != 4 {
		t.Fatalf("unacknowledged confirmed candidate stock bypassed the API progress cap: %+v", item)
	}
}

func TestReportedMarketQuotaOtherPendingRemovalRaisesTargetWithoutChangingAPIProgress(t *testing.T) {
	tracked, quests, hideout := aquariusQuotaInputs()
	snapshot, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Items[1].AvailableQuantity = 0 // YMXC: confirmed 1, pending -1.
	result := allocateForTest(t, snapshot)
	water := requireItem(t, result, "water")
	if water.CollectionRequiredQuantity != 5 || water.CollectionRemainingQuantity != 5 || requireItem(t, result, "ymxc").MarketAvailableQuantity != 1 {
		t.Fatalf("pending removal was still counted as another candidate's stock: %+v", water)
	}
	if requireAllocationSource(t, water, "quest:aquarius:water").MarketGroupAvailableQuantity != 1 {
		t.Fatal("pending removal changed the confirmed API progress")
	}
	assertAddAllowed(t, result, "water", true)
}

func TestReportedMarketQuotaOwnPendingRemovalKeepsItsTargetStable(t *testing.T) {
	tracked, quests, hideout := aquariusQuotaInputs()
	tracked.Items[0].AvailableQuantity = 3
	tracked.Groups[0].AvailableQuantity, tracked.Groups[0].RemainingQuantity = 3, 0
	snapshot, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Items[0].AvailableQuantity = 2 // Water: confirmed 3, pending -1.
	result := allocateForTest(t, snapshot)
	water, ymxc := requireItem(t, result, "water"), requireItem(t, result, "ymxc")
	if water.CollectionRequiredQuantity != 4 || water.CollectionRemainingQuantity != 2 || water.MarketAvailableQuantity != 3 || ymxc.CollectionRequiredQuantity != 2 {
		t.Fatalf("pending removal changed its own target or did not reduce the other candidate's contribution: water=%+v ymxc=%+v", water, ymxc)
	}
	assertAddAllowed(t, result, "water", true)
}

func TestReportedMarketQuotaStopsOwnTwoWhenOtherSixCoverFiveOfTwoGroups(t *testing.T) {
	tracked, quests, hideout := inputs()
	for i, quest := range []string{"first", "second"} {
		group := marketGroup(quest, "medical", 3, false, "ifak", "augmentin", "salewa")
		group.AvailableQuantity = int64(3 - i)
		group.RemainingQuantity = 3 - group.AvailableQuantity
		tracked.Groups = append(tracked.Groups, group)
	}
	tracked.Items = []market.TrackedItem{
		{ItemID: "ifak", AvailableQuantity: 2},
		{ItemID: "augmentin", AvailableQuantity: 3, RequiredQuantity: 3, Sources: []market.TrackedSource{source("fixed", 3, false)}},
		{ItemID: "salewa", AvailableQuantity: 3},
	}
	result := mergeAndAllocate(t, tracked, quests, hideout)
	item := requireItem(t, result, "ifak")
	if item.CollectionRequiredQuantity != 1 || item.CollectionRemainingQuantity != 0 || item.AvailableQuantity != 2 {
		t.Fatalf("other candidate API contribution reserved their fixed goals or reused own stock: %+v", item)
	}
	assertAddAllowed(t, result, "ifak", false)
	// Az Augmentin saját fixed3 igénye a saját céljában továbbra is megmarad.
	if requireItem(t, result, "augmentin").CollectionRequiredQuantity != 4 {
		t.Fatal("candidate quota lost the current item's fixed requirement")
	}
	assertAddAllowed(t, result, "augmentin", true)
}
