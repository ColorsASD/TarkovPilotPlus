package localprogress

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func confirmedRyeRequirements(t *testing.T, rye, noodles, progress int64) Snapshot {
	t.Helper()
	tracked, quests, hideout := inputs()
	group := marketGroup("easy-money", "food", 5, true, "rye", "noodles")
	group.AvailableQuantity, group.RemainingQuantity = progress, max(0, 5-progress)
	tracked.Groups = []market.TrackedGroup{group}
	tracked.Items = []market.TrackedItem{
		{ItemID: "rye", AvailableQuantity: rye, RequiredQuantity: 4, Sources: []market.TrackedSource{source("bitter-victory", 4, false)}},
		{ItemID: "noodles", AvailableQuantity: noodles},
	}
	snapshot, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func confirmedRequirementByID(snapshot Snapshot, result []RequirementStock, id string) RequirementStock {
	for i, row := range snapshot.Items {
		if row.ItemID == id {
			return result[i]
		}
	}
	return RequirementStock{}
}

func TestConfirmedRequirementsReserveForcedRyeForReportedSharedFIR(t *testing.T) {
	snapshot := allocateForTest(t, confirmedRyeRequirements(t, 1, 1, 2))
	before := Clone(snapshot)
	result := ConfirmedRequirementStock(snapshot)
	if got := confirmedRequirementByID(snapshot, result, "rye"); got != (RequirementStock{Known: true}) {
		t.Fatalf("the Rye already in confirmed Shared FiR was displayed as Regular too: %+v", got)
	}
	if item := requireItem(t, snapshot, "rye"); item.AvailableQuantity != 1 || item.CollectionRequiredQuantity != 8 {
		t.Fatalf("display allocation changed own stock or the independent collection quota: %+v", item)
	}
	if !reflect.DeepEqual(snapshot, before) {
		t.Fatal("confirmed requirement calculation mutated stock, sources, allocation or requirements")
	}
}

func TestConfirmedRequirementsDoNotGuessBetweenFeasibleSharedAllocations(t *testing.T) {
	for _, test := range []struct {
		name                 string
		rye, noodles, shared int64
		want                 RequirementStock
	}{
		{"no-own-stock", 0, 1, 1, RequirementStock{Known: true}},
		{"forced-own", 1, 1, 2, RequirementStock{Known: true}},
		{"ambiguous-own-one", 1, 1, 1, RequirementStock{Known: false}},
		{"ambiguous-own-two", 2, 1, 2, RequirementStock{Known: false}},
		{"forced-remainder", 3, 0, 2, RequirementStock{Regular: 1, Known: true}},
		{"fixed-covered-in-all-solutions", 10, 1, 2, RequirementStock{Regular: 4, Known: true}},
		{"zero-api-progress", 3, 1, 0, RequirementStock{Regular: 3, Known: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := confirmedRyeRequirements(t, test.rye, test.noodles, test.shared)
			if got := confirmedRequirementByID(snapshot, ConfirmedRequirementStock(snapshot), "rye"); got != test.want {
				t.Fatalf("confirmed fixed stock=%+v, want %+v", got, test.want)
			}
		})
	}
}

func TestConfirmedRequirementsIgnorePendingAndRepeatedSources(t *testing.T) {
	snapshot := confirmedRyeRequirements(t, 1, 1, 2)
	snapshot.Items[0].AvailableQuantity = 9
	snapshot.Items[1].AvailableQuantity = 8
	for i := range snapshot.Items {
		snapshot.Items[i].Sources = append(snapshot.Items[i].Sources, snapshot.Items[i].Sources...)
	}
	if got := ConfirmedRequirementStock(snapshot)[0]; got != (RequirementStock{Known: true}) {
		t.Fatalf("pending stock or a repeated group source invented confirmed Regular stock: %+v", got)
	}
	snapshot = confirmedRyeRequirements(t, 10, 1, 2)
	snapshot.Items[0].Sources = append(snapshot.Items[0].Sources, snapshot.Items[0].Sources...)
	if got := ConfirmedRequirementStock(snapshot)[0]; got != (RequirementStock{Regular: 8, Known: true}) {
		t.Fatalf("fixed source totals differed from the requirements shown in the view: %+v", got)
	}
	fixed := source("same-fixed", 4, false)
	fixedOnly := allocationSnapshot(Item{ItemID: "fixed-eight", MarketTracked: true, RequiredQuantity: 8,
		MarketRequiredQuantity: 8, AvailableQuantity: 8, MarketAvailableQuantity: 8,
		Sources: []Source{{TrackedSource: fixed, Origin: OriginMarket}, {TrackedSource: fixed, Origin: OriginMarket}}})
	if got := ConfirmedRequirementStock(fixedOnly)[0]; got != (RequirementStock{Regular: 8, Known: true}) {
		t.Fatalf("two reported fixed requirements could not reach their shared eight-piece total: %+v", got)
	}
}

func TestConfirmedRequirementsReserveOnlyTheMatchingDuplicateVariant(t *testing.T) {
	snapshot := mergedArmorVariantQuota(t)
	// A Scavenger-változat két darabja a közös hatban szerepel. A másik
	// azonos ID-jű, Fishing Gear forrású változat egy darabja Regular marad.
	snapshot.Items[1].AvailableQuantity, snapshot.Items[1].MarketAvailableQuantity = 1, 1
	for reversed := range 2 {
		if reversed == 1 {
			for i, j := 0, len(snapshot.Items)-1; i < j; i, j = i+1, j-1 {
				snapshot.Items[i], snapshot.Items[j] = snapshot.Items[j], snapshot.Items[i]
			}
		}
		before := Clone(snapshot)
		result := ConfirmedRequirementStock(snapshot)
		for i, row := range snapshot.Items {
			if row.Name == "MF-UNTAR body armor (0/50)" && result[i] != (RequirementStock{Regular: 1, Known: true}) {
				t.Fatalf("the other same-ID variant consumed the Fishing Gear stock: %+v", result[i])
			}
		}
		if !reflect.DeepEqual(snapshot, before) {
			t.Fatal("variant requirement display mutated the input")
		}
	}
}

func TestConfirmedRequirementsUseOneCapacityAcrossOverlappingGroups(t *testing.T) {
	tracked, quests, hideout := inputs()
	one := marketGroup("first", "shared", 1, true, "a", "b")
	two := marketGroup("second", "shared", 1, true, "a")
	one.AvailableQuantity, two.AvailableQuantity = 1, 1
	one.RemainingQuantity, two.RemainingQuantity = 0, 0
	tracked.Groups = []market.TrackedGroup{one, two}
	tracked.Items = []market.TrackedItem{
		{ItemID: "a", AvailableQuantity: 1, RequiredQuantity: 1, Sources: []market.TrackedSource{source("a-fixed", 1, false)}},
		{ItemID: "b", AvailableQuantity: 1, RequiredQuantity: 1, Sources: []market.TrackedSource{source("b-fixed", 1, true)}},
	}
	for reverse := range 2 {
		if reverse == 1 {
			tracked.Items[0], tracked.Items[1] = tracked.Items[1], tracked.Items[0]
			tracked.Groups[0], tracked.Groups[1] = tracked.Groups[1], tracked.Groups[0]
		}
		snapshot, err := Merge(tracked, quests, hideout)
		if err != nil {
			t.Fatal(err)
		}
		for i, got := range ConfirmedRequirementStock(snapshot) {
			if got != (RequirementStock{Known: true}) {
				t.Fatalf("overlapping groups reused a stock piece or depended on input order, row %d: %+v", i, got)
			}
		}
	}
}

func TestConfirmedRequirementsKeepIncompleteComponentsUnknownAndIndependentRowsKnown(t *testing.T) {
	snapshot := confirmedRyeRequirements(t, 1, 1, 3)
	snapshot.Items = append(snapshot.Items, Item{ItemID: "independent", MarketAvailableQuantity: 2, AvailableQuantity: 2, RequiredQuantity: 2,
		Sources: []Source{{TrackedSource: source("unrelated", 2, false), Origin: OriginMarket}}})
	zero := marketGroup("zero", "bridge", 1, false, "rye", "independent")
	snapshot.MarketGroups = append(snapshot.MarketGroups, zero)
	for _, index := range []int{0, 2} {
		snapshot.Items[index].Sources = append(snapshot.Items[index].Sources, Source{TrackedSource: market.TrackedSource{Type: "questGroup",
			QuestID: zero.QuestID, RequirementID: zero.RequirementID, Amount: 1}, Origin: OriginMarket})
	}
	got := ConfirmedRequirementStock(snapshot)
	if got[0].Known || got[1].Known {
		t.Fatalf("incomplete group invented a certain row allocation: %+v", got)
	}
	if got[2] != (RequirementStock{Regular: 2, Known: true}) {
		t.Fatalf("zero-progress bridge made an independent stock unknown: %+v", got[2])
	}
}

func TestConfirmedRequirementsIgnoreInactiveGroupsAndSources(t *testing.T) {
	for _, inactive := range []string{"completed", "canceled", "optional"} {
		t.Run(inactive, func(t *testing.T) {
			snapshot := confirmedRyeRequirements(t, 1, 1, 2)
			switch inactive {
			case "completed":
				snapshot.MarketGroups[0].Completed = true
			case "canceled":
				snapshot.MarketGroups[0].Canceled = true
			case "optional":
				snapshot.MarketGroups[0].Optional = true
			}
			if got := ConfirmedRequirementStock(snapshot)[0]; got != (RequirementStock{Regular: 1, Known: true}) {
				t.Fatalf("inactive group consumed confirmed fixed inventory: %+v", got)
			}
			// A lezárt/elhagyott fix forrás sem kaphat megerősített készletet.
			snapshot.Items[0].Sources[0].Canceled = true
			if got := ConfirmedRequirementStock(snapshot)[0]; got != (RequirementStock{Known: true}) {
				t.Fatalf("inactive fixed source received confirmed inventory: %+v", got)
			}
		})
	}
}

func TestConfirmedRequirementsCapAPIGroupDemandAndFixedKinds(t *testing.T) {
	snapshot := confirmedRyeRequirements(t, 3, 0, 5)
	snapshot.MarketGroups[0].RequiredQuantity = 1
	for i := range snapshot.Items {
		for j := range snapshot.Items[i].Sources {
			if snapshot.Items[i].Sources[j].RequirementID == "food" {
				snapshot.Items[i].Sources[j].RemainingRequiredQuantity = 1
			}
		}
	}
	if got := ConfirmedRequirementStock(snapshot)[0]; got != (RequirementStock{Regular: 2, Known: true}) {
		t.Fatalf("API progress above remaining group demand consumed extra stock: %+v", got)
	}
	fir := allocationSource("", 0, 2, true, OriginMarket)
	regular := allocationSource("", 0, 4, false, OriginMarket)
	legacy := allocationSnapshot(allocationItem("plain", 3, fir, regular))
	legacy.Items[0].AvailableQuantity = 20
	if got := ConfirmedRequirementStock(legacy)[0]; got != (RequirementStock{FIR: 2, Regular: 1, Known: true}) {
		t.Fatalf("fixed types reused confirmed stock or counted pending: %+v", got)
	}
}

func TestConfirmedRequirementsRespectLegacyAnyAllocationWithoutUsingPendingAsConfirmed(t *testing.T) {
	any := allocationSource("legacy-any", 2, 2, true, OriginMarket)
	regular := allocationSource("", 0, 2, false, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 3, any, regular), allocationItem("b", 0, any))
	snapshot.Items[0].AvailableQuantity = 4
	snapshot = allocateForTest(t, snapshot)
	snapshot.Items[0].Sources = append(snapshot.Items[0].Sources, snapshot.Items[0].Sources[0])
	if got := ConfirmedRequirementStock(snapshot)[0]; got != (RequirementStock{Regular: 2, Known: true}) {
		t.Fatalf("legacy allocation or its repeated source consumed pending as confirmed fixed stock: %+v", got)
	}
	plain := allocationSnapshot(Item{ItemID: "plain", RequiredQuantity: 4, MarketAvailableQuantity: 2, AvailableQuantity: 7})
	if got := ConfirmedRequirementStock(plain)[0]; got != (RequirementStock{Regular: 2, Known: true}) {
		t.Fatalf("source-free legacy requirement lost confirmed Regular stock: %+v", got)
	}
}

func TestConfirmedSharedUsageBoundsAgreeWithExhaustiveSmallAllocations(t *testing.T) {
	// Két átfedő csoport és három készletsor minden kis egész készletét
	// végigpróbáljuk. A referencia tényleges darabokat rendel a csoportokhoz,
	// így a min/max állítás nem ugyanannak a folyamalgoritmusnak a másolata.
	for firstMask := 1; firstMask < 8; firstMask++ {
		for secondMask := 1; secondMask < 8; secondMask++ {
			groups := make([]confirmedRequirementGroup, 2)
			for i, mask := range []int{firstMask, secondMask} {
				for row := range 3 {
					if mask&(1<<row) != 0 {
						groups[i].rows = append(groups[i].rows, row)
					}
				}
			}
			for first := int64(1); first <= 2; first++ {
				for second := int64(1); second <= 2; second++ {
					groups[0].demand, groups[1].demand = first, second
					for encoded := range 27 {
						stock := []int64{int64(encoded % 3), int64(encoded / 3 % 3), int64(encoded / 9)}
						wantMin, wantMax, wantKnown := exhaustiveConfirmedUsage(groups, stock)
						minimum, minKnown := confirmedSharedUsage(groups, []int{0, 1}, stock, 0, false)
						maximum, maxKnown := confirmedSharedUsage(groups, []int{0, 1}, stock, 0, true)
						if minKnown != wantKnown || maxKnown != wantKnown || wantKnown && (minimum != wantMin || maximum != wantMax) {
							t.Fatalf("wrong own usage interval for masks %d/%d demand %d/%d stock %v: got %d..%d known %v/%v want %d..%d known %v",
								firstMask, secondMask, first, second, stock, minimum, maximum, minKnown, maxKnown, wantMin, wantMax, wantKnown)
						}
					}
				}
			}
		}
	}
}

func exhaustiveConfirmedUsage(groups []confirmedRequirementGroup, stock []int64) (int64, int64, bool) {
	remaining := append([]int64(nil), stock...)
	minimum, maximum := MaxQuantity, int64(-1)
	var visit func(int, int64)
	visit = func(group int, units int64) {
		if group == len(groups) {
			used := stock[0] - remaining[0]
			minimum, maximum = min(minimum, used), max(maximum, used)
			return
		}
		if units == 0 {
			next := int64(0)
			if group+1 < len(groups) {
				next = groups[group+1].demand
			}
			visit(group+1, next)
			return
		}
		for _, row := range groups[group].rows {
			if remaining[row] > 0 {
				remaining[row]--
				visit(group, units-1)
				remaining[row]++
			}
		}
	}
	visit(0, groups[0].demand)
	return minimum, maximum, maximum >= 0
}
