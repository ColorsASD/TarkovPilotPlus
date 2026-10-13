package localprogress

import (
	"errors"
	"testing"
)

func handoverPoolItem(localRegular, localFIR, localUnknown, marketRegular, marketFIR, marketUnknown int64) Item {
	return Item{
		ItemID:                  "handover-item",
		LocalAvailableQuantity:  localRegular + localFIR + localUnknown,
		LocalRegularQuantity:    localRegular,
		LocalFIRQuantity:        localFIR,
		MarketAvailableQuantity: marketRegular + marketFIR + marketUnknown,
		MarketRegularQuantity:   marketRegular,
		MarketFIRQuantity:       marketFIR,
	}
}

func TestHandoverPlanSharesUnknownAcrossKindsAndUsesLocalFirst(t *testing.T) {
	tests := []struct {
		name         string
		item         Item
		regular, fir int64
		want         HandoverPlan
		unavailable  bool
	}{
		{
			name: "local unknown reserved for FiR when Market only knows Regular",
			item: handoverPoolItem(0, 0, 1, 1, 0, 0), regular: 1, fir: 1,
			want: HandoverPlan{Local: HandoverEntry{ItemID: "handover-item", Unknown: 1}, Market: HandoverEntry{ItemID: "handover-item", Regular: 1}, RegularToMarket: 1},
		},
		{
			name: "local unknown reserved for Regular when Market only knows FiR",
			item: handoverPoolItem(0, 0, 1, 0, 1, 0), regular: 1, fir: 1,
			want: HandoverPlan{Local: HandoverEntry{ItemID: "handover-item", Unknown: 1}, Market: HandoverEntry{ItemID: "handover-item", FIR: 1}, FIRToMarket: 1},
		},
		{
			name: "known local kinds used before abundant Market stock",
			item: handoverPoolItem(2, 1, 0, 2, 2, 2), regular: 2, fir: 1,
			want: HandoverPlan{Local: HandoverEntry{ItemID: "handover-item", Regular: 2, FIR: 1}, Market: HandoverEntry{ItemID: "handover-item"}},
		},
		{
			name: "local unknown used before known Market stock",
			item: handoverPoolItem(1, 1, 1, 2, 2, 0), regular: 2, fir: 2,
			want: HandoverPlan{Local: HandoverEntry{ItemID: "handover-item", Regular: 1, FIR: 1, Unknown: 1}, Market: HandoverEntry{ItemID: "handover-item", Regular: 1}, RegularToMarket: 1},
		},
		{
			name: "legacy Market unknown split across both request kinds",
			item: handoverPoolItem(0, 0, 0, 0, 0, 2), regular: 1, fir: 1,
			want: HandoverPlan{Local: HandoverEntry{ItemID: "handover-item"}, Market: HandoverEntry{ItemID: "handover-item", Unknown: 2}, RegularToMarket: 1, FIRToMarket: 1},
		},
		{
			name: "known Regular never supplies FiR",
			item: handoverPoolItem(1, 0, 0, 1, 0, 0), fir: 1, unavailable: true,
		},
		{
			name: "known FiR is not silently consumed for chosen Regular",
			item: handoverPoolItem(0, 1, 0, 0, 1, 0), regular: 1, unavailable: true,
		},
		{
			name: "a single unknown piece cannot cover two selected pieces",
			item: handoverPoolItem(0, 0, 1, 0, 0, 0), regular: 1, fir: 1, unavailable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := tt.item
			got, err := PlanHandoverItem(tt.item, tt.regular, tt.fir)
			if tt.unavailable {
				if !errors.Is(err, ErrInvalidQuantity) {
					t.Fatalf("insufficient exact quality accepted: %+v %v", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("plan = %+v, %v; want %+v", got, err, tt.want)
			}
			if tt.item.ItemID != original.ItemID || tt.item.LocalAvailableQuantity != original.LocalAvailableQuantity || tt.item.MarketAvailableQuantity != original.MarketAvailableQuantity {
				t.Fatal("planning changed collected stock")
			}
			repeated, err := PlanHandoverItem(tt.item, tt.regular, tt.fir)
			if err != nil || repeated != got {
				t.Fatalf("repeated planning changed assignment: %+v %v", repeated, err)
			}
		})
	}
}

// A kis készleteknél minden megengedett helyi típus-kiosztást felsorolunk.
// A Market teljesíthetősége csak a két típushiány és a közös unknown kapacitás
// összehasonlítása; nem a planner unknown-tartományának képletét ismétli meg.
func exhaustiveMaximumLocal(item Item, regular, fir int64) (int64, bool) {
	maximum := int64(-1)
	localUnknown := item.LocalAvailableQuantity - item.LocalRegularQuantity - item.LocalFIRQuantity
	marketUnknown := item.MarketAvailableQuantity - item.MarketRegularQuantity - item.MarketFIRQuantity
	for usedRegular := int64(0); usedRegular <= item.LocalRegularQuantity && usedRegular <= regular; usedRegular++ {
		for usedFIR := int64(0); usedFIR <= item.LocalFIRQuantity && usedFIR <= fir; usedFIR++ {
			for unknownRegular := int64(0); unknownRegular <= localUnknown && usedRegular+unknownRegular <= regular; unknownRegular++ {
				for unknownFIR := int64(0); unknownRegular+unknownFIR <= localUnknown && usedFIR+unknownFIR <= fir; unknownFIR++ {
					remainingRegular := regular - usedRegular - unknownRegular
					remainingFIR := fir - usedFIR - unknownFIR
					missingRegular := max(int64(0), remainingRegular-item.MarketRegularQuantity)
					missingFIR := max(int64(0), remainingFIR-item.MarketFIRQuantity)
					if missingRegular+missingFIR <= marketUnknown {
						maximum = max(maximum, usedRegular+usedFIR+unknownRegular+unknownFIR)
					}
				}
			}
		}
	}
	return maximum, maximum >= 0
}

func TestHandoverPlanExhaustiveSmallPoolsMatchesFeasibilityAndMaximumLocal(t *testing.T) {
	for localRegular := int64(0); localRegular <= 2; localRegular++ {
		for localFIR := int64(0); localFIR <= 2; localFIR++ {
			for localUnknown := int64(0); localUnknown <= 2; localUnknown++ {
				for marketRegular := int64(0); marketRegular <= 2; marketRegular++ {
					for marketFIR := int64(0); marketFIR <= 2; marketFIR++ {
						for marketUnknown := int64(0); marketUnknown <= 2; marketUnknown++ {
							item := handoverPoolItem(localRegular, localFIR, localUnknown, marketRegular, marketFIR, marketUnknown)
							for regular := int64(0); regular <= 3; regular++ {
								for fir := int64(0); fir <= 3; fir++ {
									if regular+fir == 0 {
										continue
									}
									maximum, feasible := exhaustiveMaximumLocal(item, regular, fir)
									got, err := PlanHandoverItem(item, regular, fir)
									if (err == nil) != feasible {
										t.Fatalf("feasibility differs for pools L(%d,%d,%d), M(%d,%d,%d), request (%d,%d): feasible=%v, plan=%+v, error=%v", localRegular, localFIR, localUnknown, marketRegular, marketFIR, marketUnknown, regular, fir, feasible, got, err)
									}
									if !feasible {
										continue
									}
									localTotal := got.Local.Regular + got.Local.FIR + got.Local.Unknown
									marketTotal := got.Market.Regular + got.Market.FIR + got.Market.Unknown
									unknownRegular := regular - got.Local.Regular - got.RegularToMarket
									unknownFIR := fir - got.Local.FIR - got.FIRToMarket
									valid := got.Local.ItemID == item.ItemID && got.Market.ItemID == item.ItemID &&
										got.Local.Regular >= 0 && got.Local.Regular <= localRegular && got.Local.FIR >= 0 && got.Local.FIR <= localFIR && got.Local.Unknown >= 0 && got.Local.Unknown <= localUnknown &&
										got.Market.Regular >= 0 && got.Market.Regular <= marketRegular && got.Market.FIR >= 0 && got.Market.FIR <= marketFIR && got.Market.Unknown >= 0 && got.Market.Unknown <= marketUnknown &&
										unknownRegular >= 0 && unknownFIR >= 0 && unknownRegular+unknownFIR == got.Local.Unknown &&
										got.RegularToMarket >= got.Market.Regular && got.FIRToMarket >= got.Market.FIR &&
										got.RegularToMarket+got.FIRToMarket == marketTotal && localTotal+marketTotal == regular+fir
									if !valid || localTotal != maximum {
										t.Fatalf("invalid/suboptimal plan for pools L(%d,%d,%d), M(%d,%d,%d), request (%d,%d): plan=%+v, maximumLocal=%d, valid=%v", localRegular, localFIR, localUnknown, marketRegular, marketFIR, marketUnknown, regular, fir, got, maximum, valid)
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestHandoverPlanRejectsInvalidIdentityQuantityAndTypedSubsets(t *testing.T) {
	valid := handoverPoolItem(1, 1, 1, 1, 1, 1)
	tests := []struct {
		name         string
		change       func(*Item)
		regular, fir int64
	}{
		{name: "empty selection"},
		{name: "negative Regular", regular: -1},
		{name: "negative FiR", fir: -1},
		{name: "request overflow", regular: MaxQuantity, fir: 1},
		{name: "invalid item", change: func(item *Item) { item.ItemID = "../invalid" }, regular: 1},
		{name: "negative local total", change: func(item *Item) { item.LocalAvailableQuantity = -1 }, regular: 1},
		{name: "negative local Regular", change: func(item *Item) { item.LocalRegularQuantity = -1 }, regular: 1},
		{name: "negative local FiR", change: func(item *Item) { item.LocalFIRQuantity = -1 }, regular: 1},
		{name: "negative Market total", change: func(item *Item) { item.MarketAvailableQuantity = -1 }, regular: 1},
		{name: "negative Market Regular", change: func(item *Item) { item.MarketRegularQuantity = -1 }, regular: 1},
		{name: "negative Market FiR", change: func(item *Item) { item.MarketFIRQuantity = -1 }, regular: 1},
		{name: "local total limit", change: func(item *Item) { item.LocalAvailableQuantity = MaxQuantity + 1 }, regular: 1},
		{name: "Market total limit", change: func(item *Item) { item.MarketAvailableQuantity = MaxQuantity + 1 }, regular: 1},
		{name: "overlapping local subsets", change: func(item *Item) { item.LocalRegularQuantity, item.LocalFIRQuantity = 2, 2 }, regular: 1},
		{name: "overlapping Market subsets", change: func(item *Item) { item.MarketRegularQuantity, item.MarketFIRQuantity = 2, 2 }, regular: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := valid
			if tt.change != nil {
				tt.change(&item)
			}
			if plan, err := PlanHandoverItem(item, tt.regular, tt.fir); !errors.Is(err, ErrInvalidQuantity) {
				t.Fatalf("invalid input accepted: %+v %v", plan, err)
			}
		})
	}
	item := handoverPoolItem(MaxQuantity, 0, 0, 0, 0, 0)
	if plan, err := PlanHandoverItem(item, MaxQuantity, 0); err != nil || plan.Local.Regular != MaxQuantity || plan.Market.Regular != 0 {
		t.Fatalf("valid boundary quantity rejected: %+v %v", plan, err)
	}
}
