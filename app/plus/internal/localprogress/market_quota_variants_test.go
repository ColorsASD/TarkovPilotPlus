package localprogress

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func armorVariantQuotaInputs() (market.TrackedSnapshot, market.QuestSnapshot, market.HideoutSnapshot) {
	tracked, quests, hideout := inputs()
	group := marketGroup("scavenger", "body-armor", 15, true, "untar", "6b13", "otv")
	group.QuestName, group.Name = "Scavenger", "Body Armor"
	group.AvailableQuantity, group.RemainingQuantity = 6, 9
	tracked.Groups = []market.TrackedGroup{group}
	tracked.Items = []market.TrackedItem{
		{ItemID: "untar", Name: "MF-UNTAR body armor", AvailableQuantity: 2, Sources: []market.TrackedSource{{
			Type: "questGroup", QuestID: group.QuestID, RequirementID: group.RequirementID,
			Name: group.QuestName, GroupName: group.Name, Amount: group.Amount, FIR: true,
		}}},
		{ItemID: "untar", Name: "MF-UNTAR body armor (0/50)", RequiredQuantity: 1, RemainingQuantity: 1,
			Sources: []market.TrackedSource{source("fishing-gear", 1, false)}},
		{ItemID: "6b13", Name: "6B13 assault armor (EMR)", AvailableQuantity: 3},
		{ItemID: "otv", Name: "Interceptor OTV body armor (UCP)", AvailableQuantity: 1},
	}
	return tracked, quests, hideout
}

func mergedArmorVariantQuota(t *testing.T) Snapshot {
	t.Helper()
	tracked, quests, hideout := armorVariantQuotaInputs()
	snapshot, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestReportedMarketQuotaCountsConfirmedArmorVariantWithoutAllocatingIt(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		for _, sameName := range []bool{false, true} {
			t.Run(testVariantQuotaName(reversed, sameName), func(t *testing.T) {
				tracked, quests, hideout := armorVariantQuotaInputs()
				if sameName {
					tracked.Items[1].Name = tracked.Items[0].Name
				}
				if reversed {
					for i, j := 0, len(tracked.Items)-1; i < j; i, j = i+1, j-1 {
						tracked.Items[i], tracked.Items[j] = tracked.Items[j], tracked.Items[i]
					}
				}
				snapshot, err := Merge(tracked, quests, hideout)
				if err != nil {
					t.Fatal(err)
				}
				before := Clone(snapshot)
				result := allocateForTest(t, snapshot)
				for _, expectation := range []struct {
					id              string
					stock, required int64
				}{{"6b13", 3, 12}, {"otv", 1, 10}} {
					item := requireItem(t, result, expectation.id)
					if !item.CollectionCalculated || item.AvailableQuantity != expectation.stock || item.CollectionRequiredQuantity != expectation.required || item.CollectionRemainingQuantity != expectation.required-expectation.stock {
						t.Fatalf("confirmed alternative variant did not reduce %s target: %+v", expectation.id, item)
					}
					assertAddAllowed(t, result, expectation.id, true)
				}
				if !reflect.DeepEqual(snapshot, before) {
					t.Fatal("quota calculation mutated the input snapshot")
				}
				for i, item := range result.Items {
					original := before.Items[i]
					if item.AvailableQuantity != original.AvailableQuantity || item.MarketAvailableQuantity != original.MarketAvailableQuantity || item.RequiredQuantity != original.RequiredQuantity || item.MarketRequiredQuantity != original.MarketRequiredQuantity || item.SpentQuantity != original.SpentQuantity {
						t.Fatalf("quota calculation changed raw inventory or requirements: before=%+v after=%+v", original, item)
					}
					if item.ItemID == "untar" && !reflect.DeepEqual(item, original) {
						t.Fatalf("ambiguous variant received allocation or a collection target: before=%+v after=%+v", original, item)
					}
				}
				if allowed, err := AddAllowed(result, "untar"); allowed || !errors.Is(err, ErrAmbiguousItem) {
					t.Fatalf("ambiguous variant became writable: allowed=%v err=%v", allowed, err)
				}
				for i := range result.Items {
					if result.Items[i].ItemID == "6b13" {
						result.Items[i].AvailableQuantity = 12
					}
				}
				assertAddAllowed(t, result, "6b13", false)
			})
		}
	}
}

func testVariantQuotaName(reversed, sameName bool) string {
	name := "forward"
	if reversed {
		name = "reversed"
	}
	if sameName {
		name += "-same-name"
	}
	return name
}

func TestReportedMarketQuotaVariantNeedsItsOwnExactExplicitGroupMembership(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Snapshot)
	}{
		{"missing-group", func(s *Snapshot) { s.MarketGroups = nil }},
		{"nonmember", func(s *Snapshot) { s.MarketGroups[0].ItemIDs = []string{"6b13", "otv"} }},
		{"group-quest", func(s *Snapshot) { s.MarketGroups[0].QuestID = "different" }},
		{"group-requirement", func(s *Snapshot) { s.MarketGroups[0].RequirementID = "different" }},
		{"group-fir", func(s *Snapshot) { s.MarketGroups[0].FIR = false }},
		{"group-optional", func(s *Snapshot) { s.MarketGroups[0].Optional = true }},
		{"group-completed", func(s *Snapshot) { s.MarketGroups[0].Completed = true }},
		{"group-canceled", func(s *Snapshot) { s.MarketGroups[0].Canceled = true }},
		{"group-amount", func(s *Snapshot) { s.MarketGroups[0].Amount++ }},
		{"group-required", func(s *Snapshot) { s.MarketGroups[0].RequiredQuantity-- }},
		{"group-progress", func(s *Snapshot) { s.MarketGroups[0].AvailableQuantity-- }},
		{"missing-source", func(s *Snapshot) { s.Items[0].Sources = nil }},
		{"source-quest", func(s *Snapshot) { s.Items[0].Sources[0].QuestID = "different" }},
		{"source-requirement", func(s *Snapshot) { s.Items[0].Sources[0].RequirementID = "different" }},
		{"source-fir", func(s *Snapshot) { s.Items[0].Sources[0].FIR = false }},
		{"source-optional", func(s *Snapshot) { s.Items[0].Sources[0].Optional = true }},
		{"source-completed", func(s *Snapshot) { s.Items[0].Sources[0].Completed = true }},
		{"source-canceled", func(s *Snapshot) { s.Items[0].Sources[0].Canceled = true }},
		{"mixed-source-kind", func(s *Snapshot) { s.Items[0].Sources[0].UpgradeKey = "Stash_2" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			snapshot := mergedArmorVariantQuota(t)
			test.change(&snapshot)
			result := allocateForTest(t, snapshot)
			if item := requireItem(t, result, "6b13"); item.CollectionRequiredQuantity != 14 {
				t.Fatalf("unproven variant contribution reduced collection target: %+v", item)
			}
		})
	}
}

func TestReportedMarketQuotaDoesNotUseTheOtherArmorVariantOrPendingVariantStock(t *testing.T) {
	for _, test := range []struct {
		name                 string
		available, confirmed int64
		want                 int64
	}{
		{"confirmed-two", 2, 2, 12},
		{"pending-five", 7, 2, 12},
		{"only-pending", 2, 0, 14},
		{"pending-removal", 1, 2, 13},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := mergedArmorVariantQuota(t)
			snapshot.Items[0].AvailableQuantity, snapshot.Items[0].MarketAvailableQuantity = test.available, test.confirmed
			// A másik azonos ID-jű változat készlete nem Scavenger / FiR forrású.
			snapshot.Items[1].AvailableQuantity, snapshot.Items[1].MarketAvailableQuantity = 7, 7
			result := allocateForTest(t, snapshot)
			if item := requireItem(t, result, "6b13"); item.CollectionRequiredQuantity != test.want {
				t.Fatalf("wrong variant or pending stock reduced collection target: %+v, want %d", item, test.want)
			}
		})
	}
}

func TestReportedMarketQuotaUsesConfirmedVariantCapacityOnceAcrossGroups(t *testing.T) {
	tracked, quests, hideout := armorVariantQuotaInputs()
	second := tracked.Groups[0]
	second.QuestID = "another-scavenger"
	tracked.Groups = append(tracked.Groups, second)
	raw := tracked.Items[0].Sources[0]
	raw.QuestID = second.QuestID
	tracked.Items[0].Sources = append(tracked.Items[0].Sources, raw)
	result := mergeAndAllocate(t, tracked, quests, hideout)
	// A két 15-ös célból az MF-UNTAR két darabja és az OTV egy darabja
	// összesen három darabot fed le, nem mindkét csoportban hármat.
	if item := requireItem(t, result, "6b13"); item.CollectionRequiredQuantity != 27 || item.CollectionRemainingQuantity != 24 {
		t.Fatalf("the same confirmed variant capacity covered independent groups twice: %+v", item)
	}
}

func TestReportedMarketQuotaCapsConfirmedVariantContributionAtAPIProgress(t *testing.T) {
	for _, test := range []struct {
		progress, want int64
	}{{0, 15}, {1, 14}, {2, 13}, {6, 12}} {
		tracked, quests, hideout := armorVariantQuotaInputs()
		tracked.Groups[0].AvailableQuantity = test.progress
		tracked.Groups[0].RemainingQuantity = 15 - test.progress
		result := mergeAndAllocate(t, tracked, quests, hideout)
		if item := requireItem(t, result, "6b13"); item.CollectionRequiredQuantity != test.want {
			t.Fatalf("confirmed variant capacity bypassed API progress %d: %+v, want %d", test.progress, item, test.want)
		}
	}
}

func TestReportedMarketQuotaCountsVariantWithOnlyOneUnambiguousGroupCandidate(t *testing.T) {
	tracked, quests, hideout := armorVariantQuotaInputs()
	tracked.Groups[0].ItemIDs = []string{"untar", "6b13"}
	tracked.Items = tracked.Items[:3]
	result := mergeAndAllocate(t, tracked, quests, hideout)
	item := requireItem(t, result, "6b13")
	if !item.CollectionCalculated || item.CollectionRequiredQuantity != 13 || item.CollectionRemainingQuantity != 10 {
		t.Fatalf("sole unambiguous candidate ignored confirmed variant stock: %+v", item)
	}
	if len(item.Sources) != 1 || item.Sources[0].AllocationKnown || item.Sources[0].AllocatedQuantity != 0 {
		t.Fatalf("reported quota unexpectedly enabled local allocation: %+v", item.Sources)
	}
	assertAddAllowed(t, result, "6b13", true)
	for i := range result.Items {
		if result.Items[i].ItemID == "6b13" {
			result.Items[i].AvailableQuantity = 13
		}
	}
	assertAddAllowed(t, result, "6b13", false)
	if allowed, err := AddAllowed(result, "untar"); allowed || !errors.Is(err, ErrAmbiguousItem) {
		t.Fatalf("single unambiguous group candidate made variant writable: allowed=%v err=%v", allowed, err)
	}
}

func TestReportedMarketQuotaKeepsSoleMarketCandidateOwnStockInNumerator(t *testing.T) {
	tracked, quests, hideout := inputs()
	group := marketGroup("single", "armor", 15, true, "6b13")
	group.AvailableQuantity, group.RemainingQuantity = 3, 12
	tracked.Groups = []market.TrackedGroup{group}
	tracked.Items = []market.TrackedItem{{ItemID: "6b13", AvailableQuantity: 3}}
	result := mergeAndAllocate(t, tracked, quests, hideout)
	if item := requireItem(t, result, "6b13"); item.CollectionRequiredQuantity != 15 || item.CollectionRemainingQuantity != 12 {
		t.Fatalf("sole candidate stock was subtracted from its own target: %+v", item)
	}
	assertAddAllowed(t, result, "6b13", true)
}
