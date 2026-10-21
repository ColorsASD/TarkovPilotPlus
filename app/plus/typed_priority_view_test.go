package plus

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

func typedComTacTrackingViewForTest(t *testing.T) *ItemTrackingState {
	return typedComTacTrackingViewWithQualityForTest(t, 1, 1)
}

func typedComTacTrackingViewWithQualityForTest(t *testing.T, fir, regular int64) *ItemTrackingState {
	t.Helper()
	const itemID = "comtac-ii-headset"
	raw := market.TrackedSnapshot{CharUID: "comtac-character", GameMode: "PVP", Revision: "comtac-revision", Items: []market.TrackedItem{{
		ItemID: itemID, Name: "Peltor ComTac II headset", AvailableQuantity: max(2, fir+regular), RequiredQuantity: 17, RemainingQuantity: max(0, 17-max(2, fir+regular)),
		Sources: []market.TrackedSource{
			{Type: "quest", QuestID: "hot-delivery", Name: "Hot Delivery", Amount: 2, RemainingRequiredQuantity: 2},
			{Type: "questGroup", QuestID: "scavenger", Name: "Scavenger", RequirementID: "headsets", FIR: true},
		},
	}}, Groups: []market.TrackedGroup{{QuestID: "scavenger", QuestName: "Scavenger", RequirementID: "headsets", Name: "Headsets", FIR: true,
		Amount: 15, RequiredQuantity: 15, AvailableQuantity: 1, RemainingQuantity: 14, ItemIDs: []string{itemID}}}}
	snapshot := marketAnyProgressSnapshotForTest(t, raw,
		market.Quest{QuestID: "hot-delivery", Name: "Hot Delivery", Status: "available"},
		market.Quest{QuestID: "scavenger", Name: "Scavenger", Status: "available"})
	snapshot.Items[0].QualityKnown, snapshot.Items[0].FIRQuantity, snapshot.Items[0].RegularQuantity = true, fir, regular
	return localTrackedView(market.Item{UID: "comtac-market-uid", BSGID: itemID, Name: raw.Items[0].Name}, snapshot)
}

func TestTypedUncategorizedComTacCountsOnlyTowardStockUntilLocallyClassified(t *testing.T) {
	view := typedComTacTrackingViewWithQualityForTest(t, 0, 0)
	if view.AvailableQuantity != 2 || view.RequiredQuantity != 17 || view.FIRQuantity != 0 || view.RegularQuantity != 0 || view.UncategorizedQuantity != 2 ||
		len(view.Requirements) != 1 || view.Requirements[0].AvailableQuantity != 0 || view.Requirements[0].RequiredQuantity != 2 ||
		len(view.AnyGroups) != 1 || view.AnyGroups[0].AvailableQuantity != 0 || view.AnyGroups[0].RequiredQuantity != 15 {
		t.Fatalf("two unknown pieces acquired a category or disappeared from total stock: %+v", view)
	}
	if view.NextItem == nil || len(view.NextItem.Targets) != 2 || view.NextItem.Targets[0].GoalName != "Hot Delivery" ||
		view.NextItem.Targets[0].AvailableQuantity != 0 || view.NextItem.Targets[0].RequiredQuantity != 2 ||
		view.NextItem.Targets[1].GoalName != "Scavenger" || view.NextItem.Targets[1].AvailableQuantity != 0 || view.NextItem.Targets[1].RequiredQuantity != 15 {
		t.Fatalf("unknown quality covered a goal: %+v", view.NextItem)
	}
	classified := typedComTacTrackingViewWithQualityForTest(t, 1, 1)
	if classified.AvailableQuantity != 2 || classified.UncategorizedQuantity != 0 || classified.Requirements[0].AvailableQuantity != 1 ||
		classified.AnyGroups[0].AvailableQuantity != 1 || classified.NextItem.Targets[0].AvailableQuantity != 2 || classified.NextItem.Targets[1].AvailableQuantity != 1 {
		t.Fatalf("local classification did not restore both agreed projections without changing API total: %+v", classified)
	}
}

func TestTypedComTacStockProtectsFIRAndNextItemUsesIndependentGoals(t *testing.T) {
	view := typedComTacTrackingViewForTest(t)
	if view.AvailableQuantity != 2 || view.FIRQuantity != 1 || view.RegularQuantity != 1 || len(view.Requirements) != 1 ||
		view.Requirements[0].Kind != "regular" || view.Requirements[0].AvailableQuantity != 1 || view.Requirements[0].RequiredQuantity != 2 ||
		len(view.AnyGroups) != 1 || !view.AnyGroups[0].FIR || view.AnyGroups[0].AvailableQuantity != 1 || view.AnyGroups[0].RequiredQuantity != 15 {
		t.Fatalf("Stock summary did not preserve the physical Regular and FiR categories: %+v", view)
	}
	if view.NextItem == nil || len(view.NextItem.Targets) != 2 || view.NextItem.Targets[0].GoalName != "Hot Delivery" ||
		view.NextItem.Targets[0].AvailableQuantity != 2 || view.NextItem.Targets[0].RequiredQuantity != 2 ||
		view.NextItem.Targets[1].GoalName != "Scavenger" || view.NextItem.Targets[1].AvailableQuantity != 1 || view.NextItem.Targets[1].RequiredQuantity != 15 {
		t.Fatalf("Next Item did not independently cover both goals from eligible classified stock: %+v", view.NextItem)
	}
}

func TestTypedComTacIndependentGoalBreakdownsPreferRegularAndPreserveStock(t *testing.T) {
	for fir := int64(0); fir <= 3; fir++ {
		for regular := int64(0); regular <= 3; regular++ {
			t.Run(fmt.Sprintf("FiR=%d/Regular=%d", fir, regular), func(t *testing.T) {
				view := typedComTacTrackingViewWithQualityForTest(t, fir, regular)
				if view.AvailableQuantity != max(2, fir+regular) || view.RequiredQuantity != 17 ||
					view.FIRQuantity != fir || view.RegularQuantity != regular || view.UncategorizedQuantity != max(0, 2-fir-regular) ||
					len(view.Requirements) != 1 || view.Requirements[0].AvailableQuantity != min(regular, 2) ||
					len(view.AnyGroups) != 1 || view.AnyGroups[0].AvailableQuantity != fir {
					t.Fatalf("per-goal display changed stock, collection target or the physical quality summary: %+v", view)
				}
				if view.NextItem == nil || len(view.NextItem.Targets) != 2 {
					t.Fatalf("ComTac goals missing: %+v", view.NextItem)
				}
				hotDelivery, scavenger := view.NextItem.Targets[0], view.NextItem.Targets[1]
				wantRegular := min(regular, 2)
				wantFIR := min(fir, 2-wantRegular)
				if !hotDelivery.QualityKnown || hotDelivery.RegularQuantity != wantRegular || hotDelivery.FIRQuantity != wantFIR ||
					hotDelivery.AvailableQuantity != wantRegular+wantFIR || hotDelivery.RequiredQuantity != 2 ||
					!scavenger.QualityKnown || scavenger.RegularQuantity != 0 || scavenger.FIRQuantity != fir || scavenger.AvailableQuantity != fir || scavenger.RequiredQuantity != 15 {
					t.Fatalf("goal quality split reused an ineligible piece or failed to prefer nonFiR: Hot Delivery=%+v Scavenger=%+v", hotDelivery, scavenger)
				}
			})
		}
	}
}

func TestTypedComTacAddingRegularReplacesFIRInGoalBreakdown(t *testing.T) {
	before, after := typedComTacTrackingViewWithQualityForTest(t, 1, 1), typedComTacTrackingViewWithQualityForTest(t, 1, 2)
	if !before.AddRegularAllowed || after.AddRegularAllowed || !after.AddFIRAllowed || before.AvailableQuantity != 2 || after.AvailableQuantity != 3 ||
		before.RequiredQuantity != 17 || after.RequiredQuantity != 17 || after.FIRQuantity != 1 || after.RegularQuantity != 2 {
		t.Fatalf("Regular replacement changed eligibility, converted physical quality or lost the API total: before=%+v after=%+v", before, after)
	}
	b, a := before.NextItem.Targets[0], after.NextItem.Targets[0]
	if b.AvailableQuantity != 2 || b.FIRQuantity != 1 || b.RegularQuantity != 1 ||
		a.AvailableQuantity != 2 || a.FIRQuantity != 0 || a.RegularQuantity != 2 ||
		after.NextItem.Targets[1].AvailableQuantity != 1 || after.NextItem.Targets[1].FIRQuantity != 1 {
		t.Fatalf("new Regular stock did not replace only the FiR contribution to Hot Delivery: before=%+v after=%+v", before.NextItem, after.NextItem)
	}
	encoded, err := json.Marshal(after.NextItem.Targets)
	if err != nil || !strings.Contains(string(encoded), `"qualityKnown":true`) || !strings.Contains(string(encoded), `"regularQuantity":2`) || !strings.Contains(string(encoded), `"firQuantity":1`) {
		t.Fatalf("quality breakdown was lost from the frontend DTO: err=%v targets=%s", err, encoded)
	}
}

func TestTypedIndependentQualityBreakdownCoversQuestsAndHideoutInEveryGameMode(t *testing.T) {
	for _, mode := range []string{"PVP", "PVE", "SEASON"} {
		t.Run(mode, func(t *testing.T) {
			const itemID = "mixed-quest-hideout-item"
			raw := market.TrackedSnapshot{CharUID: "profile-" + mode, GameMode: mode, Revision: "quality-breakdown", Items: []market.TrackedItem{{
				ItemID: itemID, Name: "Mixed item", AvailableQuantity: 4, RequiredQuantity: 11, RemainingQuantity: 7,
				Sources: []market.TrackedSource{
					{Type: "quest", QuestID: "regular-quest", Amount: 2, RemainingRequiredQuantity: 2},
					{Type: "quest", QuestID: "fir-quest", Amount: 3, RemainingRequiredQuantity: 3, FIR: true},
					{Type: "hideout", UpgradeKey: "Regular_1", Amount: 2, RemainingRequiredQuantity: 2},
					{Type: "hideout", UpgradeKey: "FIR_1", Amount: 4, RemainingRequiredQuantity: 4, FIR: true},
				},
			}}}
			snapshot, err := localprogress.Merge(raw,
				market.QuestSnapshot{CharUID: raw.CharUID, GameMode: mode, Revision: raw.Revision, Quests: []market.Quest{
					{QuestID: "regular-quest", Name: "Regular quest", Status: "available"},
					{QuestID: "fir-quest", Name: "FiR quest", Status: "locked"},
				}},
				market.HideoutSnapshot{CharUID: raw.CharUID, GameMode: mode, Revision: raw.Revision, Modules: []market.HideoutUpgrade{
					{UpgradeKey: "Regular_1", Name: "Regular module", Level: 1, CanComplete: true},
					{UpgradeKey: "FIR_1", Name: "FiR module", Level: 1},
				}})
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Items[0].QualityKnown, snapshot.Items[0].FIRQuantity, snapshot.Items[0].RegularQuantity = true, 1, 1
			view := localTrackedView(market.Item{UID: "mixed-market-uid", BSGID: itemID}, snapshot)
			if view.AvailableQuantity != 4 || view.RequiredQuantity != 11 || view.UncategorizedQuantity != 2 || view.NextItem == nil || len(view.NextItem.Targets) != 4 {
				t.Fatalf("mixed goals lost API stock, unknown pieces or requirements in %s: %+v", mode, view)
			}
			wants := map[string]struct{ fir, regular, required int64 }{
				"quest:regular-quest": {1, 1, 2}, "quest:fir-quest": {1, 0, 3},
				"module:Regular_1": {1, 1, 2}, "module:FIR_1": {1, 0, 4},
			}
			for _, target := range view.NextItem.Targets {
				want, exists := wants[target.GoalKey]
				if !exists || !target.QualityKnown || target.AllocationUnknown || target.FIRQuantity != want.fir || target.RegularQuantity != want.regular ||
					target.AvailableQuantity != want.fir+want.regular || target.RequiredQuantity != want.required {
					t.Fatalf("quest/hideout coverage or quality split wrong in %s: %+v", mode, target)
				}
			}
		})
	}
}

func TestTypedSharedSummaryDetailsKeepSameIDVariantsPhysicalContributions(t *testing.T) {
	for _, sameName := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			for _, cap := range []int64{1, 2, 8} {
				t.Run(fmt.Sprintf("sameName=%v/reverse=%v/cap=%d", sameName, reverse, cap), func(t *testing.T) {
					const itemID = "same-id-headset"
					raw := market.TrackedSnapshot{CharUID: "variant-character", GameMode: "PVP", Revision: "variant-revision", Items: []market.TrackedItem{
						{ItemID: itemID, Name: "Alpha headset", AvailableQuantity: 1, RequiredQuantity: cap,
							Sources: []market.TrackedSource{{Type: "questGroup", QuestID: "scavenger", RequirementID: "headsets", FIR: true}}},
						{ItemID: itemID, Name: "Beta headset", AvailableQuantity: 2, RequiredQuantity: cap,
							Sources: []market.TrackedSource{{Type: "questGroup", QuestID: "scavenger", RequirementID: "headsets", FIR: true}}},
					}, Groups: []market.TrackedGroup{{QuestID: "scavenger", QuestName: "Scavenger", RequirementID: "headsets", FIR: true,
						Amount: cap, RequiredQuantity: cap, ItemIDs: []string{itemID}}}}
					if sameName {
						raw.Items[1].Name = raw.Items[0].Name
					}
					// Azonos névnél is megkülönböztethető, stabil forrásazonosságú
					// változatok; az elvégzett extra cél nem fogyaszt készletet.
					raw.Items[0].Sources = append(raw.Items[0].Sources, market.TrackedSource{Type: "quest", QuestID: "alpha-role", Amount: 1, Completed: true})
					raw.Items[1].Sources = append(raw.Items[1].Sources, market.TrackedSource{Type: "quest", QuestID: "beta-role", Amount: 1, Completed: true})
					if reverse {
						raw.Items[0], raw.Items[1] = raw.Items[1], raw.Items[0]
					}
					snapshot := marketAnyProgressSnapshotForTest(t, raw,
						market.Quest{QuestID: "scavenger", Name: "Scavenger", Status: "available"},
						market.Quest{QuestID: "alpha-role", Status: "completed", Completed: true},
						market.Quest{QuestID: "beta-role", Status: "completed", Completed: true})
					for i := range snapshot.Items {
						snapshot.Items[i].QualityKnown, snapshot.Items[i].FIRQuantity = true, snapshot.Items[i].AvailableQuantity
					}
					view := localTrackedView(market.Item{UID: "headset-market-uid", BSGID: itemID}, snapshot)
					if len(view.Variants) != 2 {
						t.Fatalf("physical variant views were lost: %+v", view)
					}
					for _, variant := range view.Variants {
						if len(variant.AnyGroups) != 1 || variant.AnyGroups[0].AvailableQuantity != min(3, cap) || len(variant.AnyGroups[0].Sources) != 1 {
							t.Fatalf("same-ID Shared total changed: %+v", variant)
						}
						items := variant.AnyGroups[0].Sources[0].Items
						if len(items) != 2 || items[0].AvailableQuantity+items[1].AvailableQuantity != min(3, cap) {
							t.Fatalf("same-ID variants duplicated their shared aggregate instead of physical contributions: %+v", items)
						}
						for _, item := range items {
							if item.Name == "Alpha headset" && !sameName && item.AvailableQuantity > 1 || item.Name == "Beta headset" && item.AvailableQuantity > 2 {
								t.Fatalf("variant detail exceeded its physical stock: %+v", items)
							}
						}
					}
					body, err := json.Marshal(view)
					if err != nil || strings.Contains(string(body), "RowContributions") || strings.Contains(string(body), "rowIndex") {
						t.Fatalf("internal physical-row bookkeeping leaked into the tracker DTO: err=%v body=%s", err, body)
					}
				})
			}
		}
	}
}

func TestTypedSharedContributionLookupSkipsHiddenZeroMarketRowsAndKeepsVisibleZeroAllocations(t *testing.T) {
	snapshot := localprogress.Snapshot{Items: []localprogress.Item{
		{ItemID: "same-id", Name: "Same name", MarketTracked: true, MarketAvailableQuantity: 0},
		{ItemID: "same-id", Name: "Same name", MarketTracked: true, MarketAvailableQuantity: 2},
		{ItemID: "same-id", Name: "Same name", MarketTracked: true, MarketAvailableQuantity: 1},
	}}
	items := []ItemTrackingAnyItem{
		{ItemID: "same-id", Name: "Same name", AvailableQuantity: 99},
		{ItemID: "same-id", Name: "Same name", AvailableQuantity: 99},
	}
	applyTypedGroupContributions(items, nextitem.TypedGroupStock{RowContributions: map[int]int64{0: 0, 1: 0, 2: 1}}, snapshot)
	if items[0].AvailableQuantity != 0 || items[1].AvailableQuantity != 1 {
		t.Fatalf("a hidden Market row shifted a visible variant's typed contribution: %+v", items)
	}
	applyTypedGroupContributions(items[:1], nextitem.TypedGroupStock{RowContributions: map[int]int64{0: 0, 1: 2}}, snapshot)
	if items[0].AvailableQuantity != 2 {
		t.Fatalf("the first positive Market row inherited the omitted zero-stock variant's allocation: %+v", items)
	}
}
