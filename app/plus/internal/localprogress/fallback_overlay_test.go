package localprogress

import (
	"errors"
	"reflect"
	"testing"
)

func fallbackSnapshotForTest(items ...Item) Snapshot {
	return Snapshot{CharUID: "char-1", GameMode: "PVP", Revision: "local", Items: append([]Item{}, items...)}
}

func fallbackStockItem(id string, regular, fir, unknown int64) Item {
	return Item{ItemID: id, AvailableQuantity: regular + fir + unknown, LocalAvailableQuantity: regular + fir + unknown,
		LocalRegularQuantity: regular, LocalFIRQuantity: fir, Sources: []Source{}}
}

func TestFallbackOverlayKeepsThreeTypedSourcesSeparateAndIsIdempotent(t *testing.T) {
	row := allocationItem("A", 3, allocationSource("", 0, 9, false, OriginMarket))
	row.MarketAvailableQuantity, row.MarketRegularQuantity = 2, 1
	row.LocalAvailableQuantity, row.LocalFIRQuantity = 1, 1
	row.CollectionCalculated, row.CollectionRequiredQuantity = true, 123
	row.Sources[0].AllocationKnown = true
	row.Sources[0].AllocatedQuantity = 9
	snapshot := allocationSnapshot(row)
	own := fallbackStockItem("A", 1, 1, 1)
	own.LocalCollectedQuantity, own.LocalCollectedRegularQuantity, own.LocalCollectedFIRQuantity = 5, 2, 1
	own.LocalUsedQuantity, own.LocalUsedRegularQuantity = 2, 1
	fallback := fallbackSnapshotForTest(own)
	before, beforeFallback := Clone(snapshot), Clone(fallback)
	out, err := OverlayMarketFallback(snapshot, fallback)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, out, "A")
	if item.AvailableQuantity != 6 || item.RemainingQuantity != 3 || item.MarketAvailableQuantity != 2 || item.MarketRegularQuantity != 1 || item.LocalAvailableQuantity != 1 || item.LocalFIRQuantity != 1 ||
		item.MarketFallbackAvailableQuantity != 3 || item.MarketFallbackRegularQuantity != 1 || item.MarketFallbackFIRQuantity != 1 || item.MarketFallbackUsedQuantity != 2 || item.MarketFallbackUsedRegularQuantity != 1 || item.MarketFallbackUsedFIRQuantity != 0 {
		t.Fatalf("three sources were blended or counted twice: %+v", item)
	}
	if item.CollectionCalculated || item.CollectionRequiredQuantity != 0 || item.Sources[0].AllocationKnown || item.Sources[0].AllocatedQuantity != 0 || out.Revision != snapshot.Revision {
		t.Fatalf("overlay retained stale allocation or altered raw revision: %+v", item)
	}
	repeated, err := OverlayMarketFallback(out, fallback)
	if err != nil || !reflect.DeepEqual(out, repeated) || !reflect.DeepEqual(before, snapshot) || !reflect.DeepEqual(beforeFallback, fallback) {
		t.Fatalf("overlay is additive, unstable, or mutates input: %+v %v", repeated, err)
	}
	if _, err := Allocate(out); err != nil {
		t.Fatalf("overlay is not allocator-ready: %v", err)
	}
}

func TestFallbackOverlayPreservesOwnStockAcrossMarketZeroAndAddsMissingHistory(t *testing.T) {
	row := allocationItem("A", 3, allocationSource("", 0, 4, true, OriginMarket))
	row.MarketAvailableQuantity, row.MarketFIRQuantity = 2, 2
	row.LocalAvailableQuantity, row.LocalRegularQuantity = 1, 1
	fallback := fallbackSnapshotForTest(fallbackStockItem("A", 0, 1, 0), fallbackStockItem("remembered", 1, 0, 0))
	out, err := OverlayMarketFallback(allocationSnapshot(row), fallback)
	if err != nil {
		t.Fatal(err)
	}
	out.Items[0].MarketAvailableQuantity, out.Items[0].MarketFIRQuantity = 0, 0
	reset, err := OverlayMarketFallback(out, fallback)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, reset, "A")
	if item.AvailableQuantity != 2 || item.LocalAvailableQuantity != 1 || item.LocalRegularQuantity != 1 || item.MarketFallbackAvailableQuantity != 1 || item.MarketFallbackFIRQuantity != 1 {
		t.Fatalf("Market reset removed own stock or reused stale combined count: %+v", item)
	}
	history := requireItem(t, reset, "remembered")
	if history.AvailableQuantity != 1 || history.MarketFallbackAvailableQuantity != 1 || history.MarketAvailableQuantity != 0 || history.LocalAvailableQuantity != 0 || history.MarketTracked || len(history.Sources) != 0 {
		t.Fatalf("missing own ID acquired fake Market/dev requirement: %+v", history)
	}
	cleared, err := OverlayMarketFallback(reset, fallbackSnapshotForTest())
	if err != nil || requireItem(t, cleared, "A").AvailableQuantity != 1 || requireItem(t, cleared, "A").MarketFallbackAvailableQuantity != 0 {
		t.Fatalf("fresh empty fallback did not replace old projection: %+v %v", cleared, err)
	}
}

func TestFallbackOverlayNeverDuplicatesCompanionStockAcrossMarketVariants(t *testing.T) {
	full := allocationItem("A", 1)
	full.Name, full.LocalAvailableQuantity, full.MarketAvailableQuantity = "Full", 0, 1
	partial := full
	partial.Name, partial.AvailableQuantity, partial.MarketAvailableQuantity = "Partial", 2, 2
	out, err := OverlayMarketFallback(allocationSnapshot(full, partial), fallbackSnapshotForTest(fallbackStockItem("A", 0, 3, 0)))
	if err != nil || out.Items[0].AvailableQuantity != 1 || out.Items[1].AvailableQuantity != 2 || out.Items[0].MarketFallbackAvailableQuantity != 0 || out.Items[1].MarketFallbackAvailableQuantity != 0 {
		t.Fatalf("ambiguous rows received duplicate fallback: %+v %v", out, err)
	}
	if _, err := OverlayMarketFallback(allocationSnapshot(full), fallbackSnapshotForTest(fallbackStockItem("A", 1, 0, 0), fallbackStockItem("A", 0, 1, 0))); !errors.Is(err, ErrAmbiguousItem) {
		t.Fatalf("duplicate own stock ID accepted: %v", err)
	}
}

func TestFallbackOverlayRefreshAfterMainStoreApplyReplacesStaleProjection(t *testing.T) {
	store := NewStore(t.TempDir())
	main := allocationSnapshot(allocationItem("A", 0, allocationSource("", 0, 1, true, OriginMarket), allocationSource("", 0, 1, true, OriginDev)))
	if _, err := store.Apply(main); err != nil {
		t.Fatal(err)
	}
	stock := fallbackSnapshotForTest(fallbackStockItem("A", 0, 1, 0))
	overlaid, err := OverlayMarketFallback(main, stock)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := store.Apply(overlaid)
	if err != nil || refreshed.Items[0].AvailableQuantity != 0 || refreshed.Items[0].MarketFallbackFIRQuantity != 1 {
		t.Fatalf("fixture did not retain only stale derived metadata: %+v %v", refreshed, err)
	}
	projected, err := OverlayMarketFallback(refreshed, stock)
	if err != nil || projected.Items[0].AvailableQuantity != 1 || projected.Items[0].MarketFallbackFIRQuantity != 1 || projected.Items[0].LocalAvailableQuantity != 0 || projected.Items[0].MarketAvailableQuantity != 0 {
		t.Fatalf("refresh rejected stale overlay or counted it twice: %+v %v", projected, err)
	}
}

func TestFallbackOverlayAddsUsedOnlyHistoryWithoutAvailableStock(t *testing.T) {
	own := fallbackStockItem("historical", 0, 0, 0)
	own.LocalCollectedQuantity, own.LocalCollectedFIRQuantity = 2, 2
	own.LocalUsedQuantity, own.LocalUsedFIRQuantity = 2, 2
	out, err := OverlayMarketFallback(allocationSnapshot(), fallbackSnapshotForTest(own))
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, out, "historical")
	if item.MarketFallbackUsedQuantity != 2 || item.MarketFallbackUsedFIRQuantity != 2 || item.AvailableQuantity != 0 || item.MarketFallbackAvailableQuantity != 0 || item.MarketAvailableQuantity != 0 || item.LocalUsedQuantity != 0 {
		t.Fatalf("used-only fallback history was lost or became another source: %+v", item)
	}
}

func TestFallbackOverlayRejectsMismatchedIdentityAndQuantityWithoutMutation(t *testing.T) {
	snapshot := allocationSnapshot(allocationItem("A", 0))
	for _, fixture := range []struct {
		name string
		edit func(*Snapshot, *Snapshot)
		err  error
	}{
		{"character", func(_ *Snapshot, own *Snapshot) { own.CharUID = "char-2" }, ErrSnapshotMismatch},
		{"mode", func(_ *Snapshot, own *Snapshot) { own.GameMode = "PVE" }, ErrSnapshotMismatch},
		{"negative", func(_ *Snapshot, own *Snapshot) { own.Items[0].LocalRegularQuantity = -1 }, ErrInvalidQuantity},
		{"combined overflow", func(main *Snapshot, _ *Snapshot) {
			main.Items[0].MarketAvailableQuantity, main.Items[0].AvailableQuantity = MaxQuantity, MaxQuantity
		}, ErrInvalidQuantity},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			main, own := Clone(snapshot), fallbackSnapshotForTest(fallbackStockItem("A", 1, 0, 0))
			fixture.edit(&main, &own)
			before, beforeOwn := Clone(main), Clone(own)
			result, err := OverlayMarketFallback(main, own)
			if !errors.Is(err, fixture.err) || result.Items != nil || !reflect.DeepEqual(main, before) || !reflect.DeepEqual(own, beforeOwn) {
				t.Fatalf("invalid overlay was accepted or mutated input: %+v %v", result, err)
			}
		})
	}
}

func TestFallbackTypedStockParticipatesInFixedAndAnyAllocation(t *testing.T) {
	for _, kind := range []string{"regular", "fir", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			fixed := allocationSource("", 0, 1, true, OriginMarket)
			any := allocationSource("group", 2, 1, true, OriginMarket)
			snapshot := allocationSnapshot(allocationItem("A", 0, fixed, any), allocationItem("B", 0, any))
			own := fallbackStockItem("A", 0, 0, 0)
			own.LocalAvailableQuantity, own.AvailableQuantity = 2, 2
			if kind == "regular" {
				own.LocalRegularQuantity = 2
			} else if kind == "fir" {
				own.LocalFIRQuantity = 2
			}
			merged, err := OverlayMarketFallback(snapshot, fallbackSnapshotForTest(own))
			if err != nil {
				t.Fatal(err)
			}
			allocated := allocateForTest(t, merged)
			item := requireItem(t, allocated, "A")
			missing, fixedAvailable := int64(0), int64(1)
			if kind == "regular" {
				missing, fixedAvailable = 2, 0
			}
			if item.CollectionRemainingQuantity != missing || item.CollectionFIRAvailableQuantity != fixedAvailable || requireAllocationSource(t, item, "group").GroupRemainingQuantity != min(int64(1), missing) || item.MarketAvailableQuantity != 0 || item.LocalAvailableQuantity != 0 {
				t.Fatalf("fallback type was misallocated or changed another source: %+v", item)
			}
		})
	}
}

func threePoolItem(dev, fallback, remote StockAmounts) Item {
	item := handoverPoolItem(dev.Regular, dev.FIR, dev.Unknown, remote.Regular, remote.FIR, remote.Unknown)
	item.MarketFallbackAvailableQuantity = fallback.Regular + fallback.FIR + fallback.Unknown
	item.MarketFallbackRegularQuantity, item.MarketFallbackFIRQuantity = fallback.Regular, fallback.FIR
	return item
}

func TestHandoverFallbackUsesDevThenFallbackThenRemoteWithSharedUnknown(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		item       Item
		regular    int64
		fir        int64
		wantLocal  HandoverEntry
		wantOwn    HandoverEntry
		wantRemote HandoverEntry
	}{
		{"three sources", threePoolItem(StockAmounts{Regular: 1}, StockAmounts{Regular: 1}, StockAmounts{Regular: 2}), 3, 0,
			HandoverEntry{ItemID: "handover-item", Regular: 1}, HandoverEntry{ItemID: "handover-item", Regular: 1}, HandoverEntry{ItemID: "handover-item", Regular: 1}},
		{"dev unknown goes to Regular to preserve fallback FiR", threePoolItem(StockAmounts{Unknown: 1}, StockAmounts{FIR: 1}, StockAmounts{Regular: 1}), 1, 1,
			HandoverEntry{ItemID: "handover-item", Unknown: 1}, HandoverEntry{ItemID: "handover-item", FIR: 1}, HandoverEntry{ItemID: "handover-item"}},
		{"dev unknown goes to FiR to preserve fallback Regular", threePoolItem(StockAmounts{Unknown: 1}, StockAmounts{Regular: 1}, StockAmounts{FIR: 1}), 1, 1,
			HandoverEntry{ItemID: "handover-item", Unknown: 1}, HandoverEntry{ItemID: "handover-item", Regular: 1}, HandoverEntry{ItemID: "handover-item"}},
		{"fallback unknown reserved for FiR", threePoolItem(StockAmounts{}, StockAmounts{Unknown: 1}, StockAmounts{Regular: 1}), 1, 1,
			HandoverEntry{ItemID: "handover-item"}, HandoverEntry{ItemID: "handover-item", Unknown: 1}, HandoverEntry{ItemID: "handover-item", Regular: 1}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			before := fixture.item
			plan, err := PlanHandoverItem(fixture.item, fixture.regular, fixture.fir)
			if err != nil || plan.Local != fixture.wantLocal || plan.Fallback != fixture.wantOwn || plan.Market != fixture.wantRemote || !reflect.DeepEqual(fixture.item, before) {
				t.Fatalf("source priority or quality lost: %+v %v", plan, err)
			}
		})
	}
	item := threePoolItem(StockAmounts{}, StockAmounts{Regular: 1}, StockAmounts{})
	if _, err := PlanHandoverItem(item, 0, 1); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("fallback Regular consumed for FiR: %v", err)
	}
	item = threePoolItem(StockAmounts{}, StockAmounts{FIR: 1}, StockAmounts{})
	if _, err := PlanHandoverItem(item, 1, 0); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("fallback known FiR silently consumed for selected Regular: %v", err)
	}
}

func TestHandoverFallbackRejectsInvalidQualityAndQuantityBounds(t *testing.T) {
	valid := threePoolItem(StockAmounts{}, StockAmounts{Regular: 1}, StockAmounts{})
	for _, change := range []func(*Item){
		func(item *Item) { item.MarketFallbackAvailableQuantity = -1 },
		func(item *Item) { item.MarketFallbackRegularQuantity = -1 },
		func(item *Item) { item.MarketFallbackFIRQuantity = 1 },
		func(item *Item) { item.MarketFallbackUsedRegularQuantity = 1 },
		func(item *Item) { item.MarketFallbackUsedQuantity = localprogressQuantityLimitForTest() },
		func(item *Item) { item.MarketAvailableQuantity = -1 },
		func(item *Item) { item.MarketAvailableQuantity = MaxQuantity },
	} {
		item := valid
		change(&item)
		if _, err := PlanHandoverItem(item, 1, 0); !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("invalid fallback bounds accepted: %+v %v", item, err)
		}
	}
	item := threePoolItem(StockAmounts{}, StockAmounts{Regular: MaxQuantity}, StockAmounts{})
	plan, err := PlanHandoverItem(item, MaxQuantity, 0)
	if err != nil || plan.Fallback.Regular != MaxQuantity || handoverQuantityForTest(plan.Local) != 0 || handoverQuantityForTest(plan.Market) != 0 {
		t.Fatalf("valid fallback boundary rejected: %+v %v", plan, err)
	}
}

func localprogressQuantityLimitForTest() int64 { return MaxQuantity }

type fallbackOracleAssignment struct{ regular, fir, total int64 }

func fallbackOracleChoices(pool StockAmounts) []fallbackOracleAssignment {
	choices := []fallbackOracleAssignment{}
	for regular := int64(0); regular <= pool.Regular; regular++ {
		for fir := int64(0); fir <= pool.FIR; fir++ {
			for unknownRegular := int64(0); unknownRegular <= pool.Unknown; unknownRegular++ {
				for unknownFIR := int64(0); unknownRegular+unknownFIR <= pool.Unknown; unknownFIR++ {
					choices = append(choices, fallbackOracleAssignment{regular + unknownRegular, fir + unknownFIR, regular + fir + unknownRegular + unknownFIR})
				}
			}
		}
	}
	return choices
}

// Az oracle minden helyi típus-kiosztást felsorol, és csak a távoli végső
// teljesíthetőséget vizsgálja. Nem ismétli a planner tartomány-/fordulópontképletét.
func fallbackOracleMaximum(dev, fallback, remote StockAmounts, regular, fir int64) (int64, int64, bool) {
	bestDev, bestFallback := int64(-1), int64(-1)
	for _, d := range fallbackOracleChoices(dev) {
		for _, f := range fallbackOracleChoices(fallback) {
			r, q := regular-d.regular-f.regular, fir-d.fir-f.fir
			if r < 0 || q < 0 || max(int64(0), r-remote.Regular)+max(int64(0), q-remote.FIR) > remote.Unknown {
				continue
			}
			if d.total > bestDev || d.total == bestDev && f.total > bestFallback {
				bestDev, bestFallback = d.total, f.total
			}
		}
	}
	return bestDev, bestFallback, bestDev >= 0
}

func TestHandoverFallbackExhaustiveThreePoolsMaximizesDevThenFallback(t *testing.T) {
	for mask := 0; mask < 1<<9; mask++ {
		pools := [3]StockAmounts{}
		for i := range pools {
			pools[i] = StockAmounts{Regular: int64(mask >> (3 * i) & 1), FIR: int64(mask >> (3*i + 1) & 1), Unknown: int64(mask >> (3*i + 2) & 1)}
		}
		item := threePoolItem(pools[0], pools[1], pools[2])
		for regular := int64(0); regular <= 3; regular++ {
			for fir := int64(0); fir <= 3; fir++ {
				if regular+fir == 0 {
					continue
				}
				dev, fallback, feasible := fallbackOracleMaximum(pools[0], pools[1], pools[2], regular, fir)
				plan, err := PlanHandoverItem(item, regular, fir)
				if (err == nil) != feasible {
					t.Fatalf("three-pool feasibility mismatch: pools=%+v request=%d/%d plan=%+v error=%v", pools, regular, fir, plan, err)
				}
				if !feasible {
					continue
				}
				if handoverQuantityForTest(plan.Local) != dev || handoverQuantityForTest(plan.Fallback) != fallback || handoverQuantityForTest(plan.Local)+handoverQuantityForTest(plan.Fallback)+handoverQuantityForTest(plan.Market) != regular+fir || plan.Market.Regular > plan.RegularToMarket || plan.Market.FIR > plan.FIRToMarket {
					t.Fatalf("three-pool priority/count mismatch: pools=%+v request=%d/%d want=%d/%d plan=%+v", pools, regular, fir, dev, fallback, plan)
				}
			}
		}
	}
}

func handoverQuantityForTest(entry HandoverEntry) int64 {
	return entry.Regular + entry.FIR + entry.Unknown
}
