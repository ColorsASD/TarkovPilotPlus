package localprogress

import (
	"errors"
	"reflect"
	"testing"
)

func assertAddAllowed(t *testing.T, snapshot Snapshot, id string, want bool) {
	t.Helper()
	before := Clone(snapshot)
	allowed, err := AddAllowed(snapshot, id)
	if err != nil || allowed != want {
		t.Fatalf("add allowed=%v err=%v, want %v", allowed, err, want)
	}
	if !reflect.DeepEqual(before, snapshot) {
		t.Fatal("add eligibility changed requirements or stock")
	}
}

func TestAddAllowedSharedStockKeepsBothRequirementKinds(t *testing.T) {
	for _, fir := range []bool{false, true} {
		item := allocationItem("a", 0, allocationSource("", 0, 1, fir, OriginMarket))
		assertAddAllowed(t, allocationSnapshot(item), "a", true)
		item.AvailableQuantity, item.MarketAvailableQuantity = 1, 1
		assertAddAllowed(t, allocationSnapshot(item), "a", false)
	}
	fir := allocationSource("", 0, 2, true, OriginMarket)
	regular := allocationSource("", 0, 1, false, OriginMarket)
	item := allocationItem("a", 2, fir, regular)
	assertAddAllowed(t, allocationSnapshot(item), "a", true)
	item.AvailableQuantity, item.MarketAvailableQuantity = 3, 3
	assertAddAllowed(t, allocationSnapshot(item), "a", false)
}

func TestAddAllowedAnyCandidatesShareOneDeficit(t *testing.T) {
	for _, fir := range []bool{false, true} {
		any := allocationSource("any", 2, 2, fir, OriginMarket)
		a, b := allocationItem("a", 0, any), allocationItem("b", 1, any)
		assertAddAllowed(t, allocationSnapshot(a, b), "a", true)
		b.AvailableQuantity, b.MarketAvailableQuantity = 2, 2
		assertAddAllowed(t, allocationSnapshot(a, b), "a", false)
	}
	one := allocationSource("one", 2, 1, false, OriginMarket)
	two := allocationSource("two", 2, 1, true, OriginMarket)
	a, b, c := allocationItem("a", 1, one, two), allocationItem("b", 0, one), allocationItem("c", 0, two)
	assertAddAllowed(t, allocationSnapshot(a, b, c), "b", true)
	b.AvailableQuantity, b.MarketAvailableQuantity = 1, 1
	assertAddAllowed(t, allocationSnapshot(a, b, c), "c", false)
}

func TestAddAllowedPartialMarketGroupAcceptsOnlyOneMoreCandidate(t *testing.T) {
	for _, fir := range []bool{false, true} {
		for _, addedID := range []string{"salewa", "cms", "ifak"} {
			tracked, quests, hideout := partialMarketGroupInputs(fir)
			snapshot := mergeAndAllocate(t, tracked, quests, hideout)
			for _, candidate := range snapshot.Items {
				assertAddAllowed(t, snapshot, candidate.ItemID, true)
			}
			for i := range snapshot.Items {
				if snapshot.Items[i].ItemID == addedID {
					snapshot.Items[i].AvailableQuantity++
				}
			}
			for _, candidate := range snapshot.Items {
				assertAddAllowed(t, snapshot, candidate.ItemID, false)
			}
		}
	}
}

func TestAddAllowedRejectInvalidOrAmbiguousTargetAndQuantityLimit(t *testing.T) {
	fixed := allocationSource("", 0, 1, false, OriginMarket)
	a := allocationItem("a", 0, fixed)
	for _, tc := range []struct {
		snapshot Snapshot
		id       string
		want     error
	}{
		{allocationSnapshot(a, a), "a", ErrAmbiguousItem},
		{allocationSnapshot(a), "missing", ErrItemNotFound},
		{allocationSnapshot(Item{ItemID: "a", RequiredQuantity: 2, Sources: a.Sources}), "a", ErrInvalidSnapshot},
	} {
		if _, err := AddAllowed(tc.snapshot, tc.id); !errors.Is(err, tc.want) {
			t.Fatalf("invalid target accepted: %v, want %v", err, tc.want)
		}
	}
	a.AvailableQuantity, a.MarketAvailableQuantity = MaxQuantity, MaxQuantity
	assertAddAllowed(t, allocationSnapshot(a), "a", false)
}
