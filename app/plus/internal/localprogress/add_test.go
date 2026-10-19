package localprogress

import (
	"errors"
	"reflect"
	"testing"
)

func assertAddOptions(t *testing.T, snapshot Snapshot, id string, wantRegular, wantFIR bool) {
	t.Helper()
	before := Clone(snapshot)
	regular, fir, err := AddOptions(snapshot, id)
	if err != nil || regular != wantRegular || fir != wantFIR {
		t.Fatalf("add options: regular=%v fir=%v err=%v, want %v/%v", regular, fir, err, wantRegular, wantFIR)
	}
	if !reflect.DeepEqual(before, snapshot) {
		t.Fatal("add eligibility changed source requirements or stock")
	}
}

func TestAddOptionsFixedQualityAndCompatibleUnknownStock(t *testing.T) {
	for _, origin := range []string{OriginMarket} {
		for _, fir := range []bool{false, true} {
			fixed := allocationSource("", 0, 1, fir, origin)
			item := allocationItem("a", 0, fixed)
			assertAddOptions(t, allocationSnapshot(item), "a", !fir, true)
			item.AvailableQuantity, item.MarketAvailableQuantity = 1, 1
			assertAddOptions(t, allocationSnapshot(item), "a", false, false)
			item.MarketRegularQuantity = 1
			assertAddOptions(t, allocationSnapshot(item), "a", false, fir)
			item.MarketRegularQuantity, item.MarketFIRQuantity = 0, 1
			assertAddOptions(t, allocationSnapshot(item), "a", false, false)
		}
	}
}

func TestAddOptionsAnyQualityUsesOtherCandidatesAndSharedGoals(t *testing.T) {
	for _, fir := range []bool{false, true} {
		any := allocationSource("any", 2, 2, fir, OriginMarket)
		a, b := allocationItem("a", 0, any), allocationItem("b", 1, any)
		b.MarketFIRQuantity = 1
		assertAddOptions(t, allocationSnapshot(a, b), "a", !fir, true)
		b.AvailableQuantity, b.MarketAvailableQuantity, b.MarketFIRQuantity = 2, 2, 2
		assertAddOptions(t, allocationSnapshot(a, b), "a", false, false)
	}
	first := allocationSource("one", 2, 1, false, OriginMarket)
	second := allocationSource("two", 2, 1, false, OriginMarket)
	a, b, c := allocationItem("a", 1, first, second), allocationItem("b", 0, first), allocationItem("c", 0, second)
	assertAddOptions(t, allocationSnapshot(a, b, c), "b", true, true)
	b.AvailableQuantity, b.MarketAvailableQuantity = 1, 1
	assertAddOptions(t, allocationSnapshot(a, b, c), "c", false, false)
}

func TestAddOptionsPartialMarketGroupAcceptsOnlyOneMoreCandidate(t *testing.T) {
	for _, fir := range []bool{false, true} {
		for _, addedID := range []string{"salewa", "cms", "ifak"} {
			t.Run(addedID+map[bool]string{false: "/regular", true: "/fir"}[fir], func(t *testing.T) {
				tracked, quests, hideout := partialMarketGroupInputs(fir)
				snapshot := mergeAndAllocate(t, tracked, quests, hideout)
				for _, candidate := range snapshot.Items {
					assertAddOptions(t, snapshot, candidate.ItemID, !fir, true)
				}
				for i := range snapshot.Items {
					item := &snapshot.Items[i]
					if item.ItemID != addedID {
						continue
					}
					item.AvailableQuantity++
					item.MarketAvailableQuantity++
					if fir {
						item.MarketFIRQuantity++
					} else {
						item.MarketRegularQuantity++
					}
				}
				for _, candidate := range snapshot.Items {
					assertAddOptions(t, snapshot, candidate.ItemID, false, false)
				}
			})
		}
	}
}

func TestAddOptionsRegularCanFreeFIRForAnAnyGoal(t *testing.T) {
	fixed := allocationSource("", 0, 1, false, OriginMarket)
	any := allocationSource("any-fir", 2, 1, true, OriginMarket)
	a, b := allocationItem("a", 1, fixed, any), allocationItem("b", 0, any)
	a.MarketFIRQuantity = 1
	assertAddOptions(t, allocationSnapshot(a, b), "a", true, true)
	assertAddOptions(t, allocationSnapshot(a, b), "b", false, true)
	a.AvailableQuantity, a.MarketAvailableQuantity, a.MarketRegularQuantity = 2, 2, 1
	assertAddOptions(t, allocationSnapshot(a, b), "a", false, false)
	assertAddOptions(t, allocationSnapshot(a, b), "b", false, false)
}

func TestAddOptionsMarketQualityBreakdownRespectsTypedCap(t *testing.T) {
	fir := allocationSource("", 0, 2, true, OriginMarket)
	regular := allocationSource("", 0, 1, false, OriginMarket)
	a := allocationItem("a", 2, fir, regular)
	a.MarketRegularQuantity, a.MarketFIRQuantity = 1, 1
	assertAddOptions(t, allocationSnapshot(a), "a", false, true)
	a.MarketAvailableQuantity, a.MarketFIRQuantity, a.AvailableQuantity = 3, 2, 3
	assertAddOptions(t, allocationSnapshot(a), "a", false, false)
}

func TestAddOptionsRejectInvalidOrAmbiguousTargetAndQuantityLimit(t *testing.T) {
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
		if _, _, err := AddOptions(tc.snapshot, tc.id); !errors.Is(err, tc.want) {
			t.Fatalf("invalid target accepted: %v, want %v", err, tc.want)
		}
	}
	a.AvailableQuantity, a.MarketAvailableQuantity = MaxQuantity, MaxQuantity
	assertAddOptions(t, allocationSnapshot(a), "a", false, false)
}
