package localprogress

import (
	"errors"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func allocationSource(group string, alternatives int, quantity int64, fir bool, origin string) Source {
	return Source{TrackedSource: source("quest", quantity, fir), Origin: origin, GroupID: group, Alternatives: alternatives}
}

func allocationItem(id string, stock int64, sources ...Source) Item {
	item := Item{ItemID: id, AvailableQuantity: stock, MarketAvailableQuantity: stock, Sources: append([]Source{}, sources...)}
	for _, source := range sources {
		if active(source) {
			item.RequiredQuantity += source.RemainingRequiredQuantity
		}
		if source.Origin == OriginMarket {
			item.MarketTracked = true
			item.MarketRequiredQuantity += source.RemainingRequiredQuantity
		}
	}
	item.RemainingQuantity = max(0, item.RequiredQuantity-stock)
	return item
}

func allocationSnapshot(items ...Item) Snapshot {
	return Snapshot{CharUID: "char-1", GameMode: "PVP", Revision: "rev-1", Items: append([]Item{}, items...)}
}

func allocateForTest(t *testing.T, snapshot Snapshot) Snapshot {
	t.Helper()
	out, err := Allocate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func requireAllocationSource(t *testing.T, item Item, group string) Source {
	t.Helper()
	for _, source := range item.Sources {
		if source.GroupID == group {
			return source
		}
	}
	t.Fatalf("missing source %q in %+v", group, item)
	return Source{}
}

func TestAllocateAnyTwoUsesOneCommonStockPoolWithoutCompletingQuest(t *testing.T) {
	group := allocationSource("any", 3, 2, false, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 1, group), allocationItem("b", 1, group), allocationItem("c", 0, group))
	snapshot.Items[0].SpentQuantity = 99
	before := Clone(snapshot)
	out := allocateForTest(t, snapshot)
	if !reflect.DeepEqual(snapshot, before) {
		t.Fatal("Allocate mutated the caller's snapshot")
	}
	var assigned int64
	for _, item := range out.Items {
		source := item.Sources[0]
		assigned += source.AllocatedQuantity
		if !item.CollectionCalculated || item.CollectionRequiredQuantity != 0 || item.CollectionRemainingQuantity != 0 || !source.AllocationKnown || source.GroupRemainingQuantity != 0 || source.Completed || source.Amount != 2 || source.RemainingRequiredQuantity != 2 || item.RequiredQuantity != 2 {
			t.Fatalf("covered group changed raw goal or still requested candidates: %+v", item)
		}
		if item.CollectionFIRAvailableQuantity != 0 || item.CollectionRegularAvailableQuantity != 0 {
			t.Fatalf("covered Any stock was reused by visible quality counters: %+v", item)
		}
	}
	if assigned != 2 || out.Items[0].SpentQuantity != 99 || out.Items[0].AvailableQuantity != 1 {
		t.Fatalf("stock was consumed or spent counted as available: %+v", out)
	}
	raw := Clone(out)
	resetAllocation(&raw)
	if !reflect.DeepEqual(raw, before) {
		t.Fatal("allocation changed authoritative raw fields")
	}
}

func TestAllocateAnyThreeProjectsOneMissingOnEveryCandidate(t *testing.T) {
	group := allocationSource("any", 3, 3, false, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 1, group), allocationItem("b", 1, group), allocationItem("c", 0, group))
	out := allocateForTest(t, snapshot)
	for _, id := range []string{"a", "b", "c"} {
		item := requireItem(t, out, id)
		wantAssigned := int64(1)
		if id == "c" {
			wantAssigned = 0
		}
		if item.Sources[0].AllocatedQuantity != wantAssigned || item.Sources[0].GroupRemainingQuantity != 1 || item.CollectionRequiredQuantity != wantAssigned+1 || item.CollectionRemainingQuantity != 1 || item.CollectionRegularAvailableQuantity != wantAssigned {
			t.Fatalf("wrong open-group projection for %s: %+v", id, item)
		}
	}
}

func TestAllocateReservesMandatoryRequirementBeforeAny(t *testing.T) {
	any := allocationSource("any", 2, 2, false, OriginMarket)
	fixed := allocationSource("", 0, 1, false, OriginMarket)
	out := allocateForTest(t, allocationSnapshot(allocationItem("a", 1, any, fixed), allocationItem("b", 1, any)))
	a, b := requireItem(t, out, "a"), requireItem(t, out, "b")
	if requireAllocationSource(t, a, "any").AllocatedQuantity != 0 || requireAllocationSource(t, b, "any").AllocatedQuantity != 1 || requireAllocationSource(t, a, "any").GroupRemainingQuantity != 1 || a.CollectionRequiredQuantity != 2 || a.CollectionRemainingQuantity != 1 || a.CollectionRegularAvailableQuantity != 1 {
		t.Fatalf("mandatory stock was reused by Any: %+v", out)
	}
	if a.Sources[1].AllocationKnown || a.Sources[1].RemainingRequiredQuantity != 1 {
		t.Fatal("fixed source became an Any projection")
	}
}

func TestAllocateOverlappingGroupsReroutesEarlierAssignment(t *testing.T) {
	one := allocationSource("g1", 2, 1, false, OriginMarket)
	two := allocationSource("g2", 2, 1, false, OriginMarket)
	out := allocateForTest(t, allocationSnapshot(allocationItem("a", 1, one, two), allocationItem("b", 1, one), allocationItem("c", 0, two)))
	a, b := requireItem(t, out, "a"), requireItem(t, out, "b")
	if requireAllocationSource(t, a, "g1").AllocatedQuantity != 0 || requireAllocationSource(t, a, "g2").AllocatedQuantity != 1 || requireAllocationSource(t, b, "g1").AllocatedQuantity != 1 {
		t.Fatalf("greedy assignment was not rerouted: %+v", out)
	}
	for _, item := range out.Items {
		if item.CollectionRequiredQuantity != 0 || item.CollectionRemainingQuantity != 0 {
			t.Fatalf("two pieces did not cover two distinct goals: %+v", item)
		}
	}
}

func TestAllocatePrioritizesFIRAnyBeforeCompatibleRegularAny(t *testing.T) {
	regular := allocationSource("a-regular", 2, 3, false, OriginMarket)
	fir := allocationSource("z-fir", 2, 3, true, OriginMarket)
	fixed := allocationSource("", 0, 3, false, OriginMarket)
	augmentin := allocationItem("augmentin", 1, fixed, regular, fir)

	salewa := allocationItem("salewa", 2, regular, fir)

	out := allocateForTest(t, allocationSnapshot(augmentin, salewa))
	for _, item := range out.Items {
		if requireAllocationSource(t, item, "a-regular").GroupRemainingQuantity != 3 || requireAllocationSource(t, item, "z-fir").GroupRemainingQuantity != 1 {
			t.Fatalf("known FiR stock was spent on the more flexible Any group: %+v", out)
		}
	}
	if requireAllocationSource(t, requireItem(t, out, "augmentin"), "a-regular").AllocatedQuantity != 0 || requireAllocationSource(t, requireItem(t, out, "salewa"), "z-fir").AllocatedQuantity != 2 {
		t.Fatalf("fixed Regular or FiR Any stock was duplicated: %+v", out)
	}
	// A későbbi sima készlet a sima Anyt fedi; az első FiR kiosztás megmarad.
	augmentin.AvailableQuantity, augmentin.MarketAvailableQuantity = 6, 6
	out = allocateForTest(t, allocationSnapshot(augmentin, salewa))
	if requireAllocationSource(t, requireItem(t, out, "augmentin"), "a-regular").GroupRemainingQuantity != 1 || requireAllocationSource(t, requireItem(t, out, "salewa"), "z-fir").GroupRemainingQuantity != 0 {
		t.Fatalf("Regular additions changed the remaining FiR need: %+v", out)
	}
	// A FiR igény feletti alkalmas készlet a nem-FiR Anyhoz is jó.
	augmentin = allocationItem("augmentin", 0, regular, fir)
	salewa = allocationItem("salewa", 4, regular, fir)

	out = allocateForTest(t, allocationSnapshot(augmentin, salewa))
	row := requireItem(t, out, "salewa")
	if requireAllocationSource(t, row, "z-fir").GroupRemainingQuantity != 0 || requireAllocationSource(t, row, "a-regular").AllocatedQuantity != 1 || requireAllocationSource(t, row, "a-regular").GroupRemainingQuantity != 2 {
		t.Fatalf("FiR overflow did not cover Regular Any exactly once: %+v", out)
	}
}

func TestAllocateCoveredStockOffsetsAnotherOpenGroupAndMarketQuota(t *testing.T) {
	one := allocationSource("g1", 2, 1, false, OriginMarket)
	two := allocationSource("g2", 2, 1, false, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 1, one, two), allocationItem("b", 0, one), allocationItem("c", 0, two))
	out := allocateForTest(t, snapshot)
	a := requireItem(t, out, "a")
	if requireAllocationSource(t, a, "g1").GroupRemainingQuantity != 0 || requireAllocationSource(t, a, "g2").GroupRemainingQuantity != 1 || a.CollectionRequiredQuantity != 2 || a.CollectionRemainingQuantity != 1 || a.CollectionMarketRequiredQuantity != 2 || a.MarketRequiredQuantity != 2 || a.CollectionRegularAvailableQuantity != 0 {
		t.Fatalf("covered piece reused or lost from quota offset: %+v", a)
	}
	if requireItem(t, out, "b").CollectionRequiredQuantity != 0 || requireItem(t, out, "c").CollectionRequiredQuantity != 1 {
		t.Fatalf("covered/open candidate display is wrong: %+v", out)
	}
}

func normalizeAllocation(snapshot Snapshot) Snapshot {
	out := Clone(snapshot)
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ItemID < out.Items[j].ItemID })
	for i := range out.Items {
		sort.Slice(out.Items[i].Sources, func(a, b int) bool { return out.Items[i].Sources[a].GroupID < out.Items[i].Sources[b].GroupID })
	}
	return out
}

func TestAllocateIsDeterministicIdempotentAndResetsStaleDerivedFields(t *testing.T) {
	one := allocationSource("g1", 3, 2, false, OriginMarket)
	two := allocationSource("g2", 2, 1, true, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 1, one, two), allocationItem("b", 1, one), allocationItem("c", 0, one, two))
	want := normalizeAllocation(allocateForTest(t, snapshot))
	rng := rand.New(rand.NewSource(34))
	for iteration := 0; iteration < 20; iteration++ {
		permuted := Clone(snapshot)
		rng.Shuffle(len(permuted.Items), func(i, j int) { permuted.Items[i], permuted.Items[j] = permuted.Items[j], permuted.Items[i] })
		for i := range permuted.Items {
			item := &permuted.Items[i]
			item.CollectionCalculated = true
			item.CollectionRequiredQuantity, item.CollectionRemainingQuantity, item.CollectionMarketRequiredQuantity = 123, 456, 789
			item.CollectionFIRAvailableQuantity, item.CollectionRegularAvailableQuantity = 999, 999
			for j := range item.Sources {
				item.Sources[j].AllocationKnown = true
				item.Sources[j].AllocatedQuantity, item.Sources[j].GroupRemainingQuantity = 888, 777
			}
			rng.Shuffle(len(item.Sources), func(a, b int) { item.Sources[a], item.Sources[b] = item.Sources[b], item.Sources[a] })
		}
		before := Clone(permuted)
		out := allocateForTest(t, permuted)
		if !reflect.DeepEqual(before, permuted) || !reflect.DeepEqual(want, normalizeAllocation(out)) || !reflect.DeepEqual(out, allocateForTest(t, out)) {
			t.Fatalf("unstable allocation or input mutation in permutation %d: %+v", iteration, out)
		}
	}
	stale := allocateForTest(t, snapshot)
	for i := range stale.Items {
		for j := range stale.Items[i].Sources {
			stale.Items[i].Sources[j].GroupID, stale.Items[i].Sources[j].Alternatives = "", 0
		}
	}
	for _, item := range allocateForTest(t, stale).Items {
		if item.CollectionCalculated {
			t.Fatal("old Any derived state survived removal of its group")
		}
		for _, source := range item.Sources {
			if source.AllocationKnown || source.AllocatedQuantity != 0 || source.GroupRemainingQuantity != 0 {
				t.Fatal("old source assignment survived removal of its group")
			}
		}
	}
}

func TestAllocateConservativelySkipsDuplicateAndMismatchedGroups(t *testing.T) {
	for _, mismatch := range []string{"duplicate-id", "remaining", "amount", "group-amount", "fir", "alternatives", "missing-member", "duplicate-source", "inactive", "raw-total"} {
		t.Run(mismatch, func(t *testing.T) {
			any := allocationSource("any", 2, 2, false, OriginMarket)
			unknown := allocationSnapshot(allocationItem("a", 2, any), allocationItem("b", 0, any))
			switch mismatch {
			case "duplicate-id":
				unknown.Items = append(unknown.Items, allocationItem("a", 2, any))
			case "remaining":
				unknown.Items[1].Sources[0].RemainingRequiredQuantity = 1
			case "amount":
				unknown.Items[1].Sources[0].Amount = 3
			case "group-amount":
				unknown.Items[1].Sources[0].GroupAmount = 3
			case "fir":
				unknown.Items[1].Sources[0].FIR = true
			case "alternatives":
				unknown.Items[1].Sources[0].Alternatives = 3
			case "missing-member":
				unknown.Items = unknown.Items[:1]
			case "duplicate-source":
				unknown.Items[0].Sources = append(unknown.Items[0].Sources, any)
			case "inactive":
				unknown.Items[1].Sources[0].Completed = true
			case "raw-total":
				unknown.Items[0].RequiredQuantity = 1
			}
			out := allocateForTest(t, unknown)
			for _, item := range out.Items {
				for _, source := range item.Sources {
					if source.AllocationKnown {
						t.Fatalf("ambiguous group was automatically covered: %+v", out)
					}
				}
			}
		})
	}
}

func TestAllocateUnknownGroupReservesStockBeforeKnownGroup(t *testing.T) {
	unknown := allocationSource("missing", 3, 1, false, OriginMarket)
	known := allocationSource("known", 2, 1, false, OriginMarket)
	out := allocateForTest(t, allocationSnapshot(allocationItem("a", 1, unknown, known), allocationItem("b", 0, known)))
	a := requireItem(t, out, "a")
	if a.Sources[0].AllocationKnown || requireAllocationSource(t, a, "known").AllocatedQuantity != 0 || requireAllocationSource(t, a, "known").GroupRemainingQuantity != 1 || a.CollectionRequiredQuantity != 2 {
		t.Fatalf("uncertain requirement was treated as free stock: %+v", out)
	}
}

func TestAllocateUnknownMarketStockCoversAnyIncludingSeparateInventory(t *testing.T) {
	group := allocationSource("any", 2, 3, true, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 2, group), allocationItem("b", 1, group))
	snapshot.FIRInventorySeparate = true
	out := allocateForTest(t, snapshot)
	if requireAllocationSource(t, requireItem(t, out, "a"), "any").AllocatedQuantity != 2 || requireItem(t, out, "a").CollectionRequiredQuantity != 0 || requireItem(t, out, "b").CollectionRequiredQuantity != 0 {
		t.Fatalf("combined or compatibility FiR stock did not cover shared requirement: %+v", out)
	}
	if out.Items[0].MarketAvailableQuantity != 2 || out.Items[0].AvailableQuantity != 2 || out.Items[0].MarketRequiredQuantity != 3 {
		t.Fatal("raw stock sources or quota changed")
	}
}

func TestAllocateSharedOverlapReroutesStockToFIRGroup(t *testing.T) {
	regular := allocationSource("g1", 2, 1, false, OriginMarket)
	fir := allocationSource("g2", 2, 1, true, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 1, regular, fir), allocationItem("b", 1, regular), allocationItem("c", 0, fir))

	out := allocateForTest(t, snapshot)
	if requireAllocationSource(t, requireItem(t, out, "a"), "g2").AllocatedQuantity != 1 || requireAllocationSource(t, requireItem(t, out, "b"), "g1").AllocatedQuantity != 1 {
		t.Fatalf("qualifying stock was trapped in regular Any group: %+v", out)
	}
	for _, item := range out.Items {
		if item.CollectionRemainingQuantity != 0 {
			t.Fatalf("typed max flow failed to cover both groups: %+v", out)
		}
	}
}

func TestAllocateLegacyCountersAreFilledWithoutDerivedQuantityChange(t *testing.T) {
	fir := allocationSource("", 0, 5, true, OriginMarket)
	regular := allocationSource("", 0, 3, false, OriginMarket)
	out := allocateForTest(t, allocationSnapshot(allocationItem("a", 2, regular, fir)))
	a := out.Items[0]
	if a.CollectionCalculated || a.RequiredQuantity != 8 || a.RemainingQuantity != 6 || a.CollectionFIRAvailableQuantity != 2 || a.CollectionRegularAvailableQuantity != 0 {
		t.Fatalf("legacy manual stock counters or normal target changed: %+v", a)
	}
}

func TestAllocateFixedReservationPreservesSharedCapacityForAny(t *testing.T) {
	regular := allocationSource("", 0, 1, false, OriginMarket)
	firAny := allocationSource("any", 2, 1, true, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", 2, regular, firAny), allocationItem("b", 0, firAny))
	out := allocateForTest(t, snapshot)
	a := requireItem(t, out, "a")
	if a.CollectionRequiredQuantity != 1 || a.CollectionRemainingQuantity != 0 || requireAllocationSource(t, a, "any").AllocatedQuantity != 1 || a.CollectionRegularAvailableQuantity != 1 || a.CollectionFIRAvailableQuantity != 0 {
		t.Fatalf("fixed regular consumed FiR despite available regular or covered counter counted twice: %+v", out)
	}
	fiRfixed := allocationSource("", 0, 1, true, OriginMarket)
	snapshot.Items[0].Sources = append(snapshot.Items[0].Sources, fiRfixed)
	snapshot.Items[0].RequiredQuantity++
	a = requireItem(t, allocateForTest(t, snapshot), "a")
	if a.CollectionRequiredQuantity != 3 || a.CollectionRemainingQuantity != 1 || a.CollectionFIRAvailableQuantity != 1 || a.CollectionRegularAvailableQuantity != 1 || requireAllocationSource(t, a, "any").AllocatedQuantity != 0 {
		t.Fatalf("Any took a piece reserved for mandatory FiR: %+v", a)
	}
}

func TestAllocateQuantityBounds(t *testing.T) {
	group := allocationSource("any", 2, MaxQuantity, true, OriginMarket)
	snapshot := allocationSnapshot(allocationItem("a", MaxQuantity, group), allocationItem("b", 0, group))

	a := allocateForTest(t, snapshot).Items[0]
	if a.CollectionRequiredQuantity != 0 || a.CollectionRemainingQuantity != 0 || a.Sources[0].AllocatedQuantity != MaxQuantity {
		t.Fatalf("valid maximum count was overflowed or incorrectly allocated: %+v", a)
	}
	for _, invalid := range []string{"stock", "source", "group-amount", "source-sum"} {
		t.Run(invalid, func(t *testing.T) {
			bad := Clone(snapshot)
			switch invalid {
			case "stock":
				bad.Items[0].AvailableQuantity = MaxQuantity + 1
			case "source":
				bad.Items[0].Sources[0].Amount = MaxQuantity + 1
			case "group-amount":
				bad.Items[0].Sources[0].GroupAmount = MaxQuantity + 1

			case "source-sum":
				bad.Items[0].Sources = append(bad.Items[0].Sources, allocationSource("", 0, 1, false, OriginMarket))
			}
			if _, err := Allocate(bad); !errors.Is(err, ErrInvalidQuantity) {
				t.Fatalf("invalid quantity accepted: %v", err)
			}
		})
	}
}
