package plus

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

func mixedGoalRequirementSnapshot(t *testing.T, stock int64, reverse bool) (market.Item, localprogress.Snapshot) {
	t.Helper()
	item := market.Item{UID: "mixed-goal-item", BSGID: "mixed-physical-item", Name: "Mixed goal item"}
	tracked := market.TrackedSnapshot{CharUID: "mixed-character", GameMode: "PVP", Revision: "mixed-revision", Items: []market.TrackedItem{{
		ItemID: item.BSGID, Name: item.Name, AvailableQuantity: stock, RequiredQuantity: 14, RemainingQuantity: max(0, 14-stock),
		Sources: []market.TrackedSource{
			{Type: "quest", QuestID: "large-fir", Name: "Large FiR quest", Amount: 10, RemainingRequiredQuantity: 10, FIR: true},
			{Type: "quest", QuestID: "small-regular", Name: "Small Regular quest", Amount: 4, RemainingRequiredQuantity: 4},
		},
	}}}
	quests := market.QuestSnapshot{CharUID: tracked.CharUID, GameMode: tracked.GameMode, Revision: tracked.Revision, Quests: []market.Quest{
		{QuestID: "large-fir", Name: "Large FiR quest", Status: "locked"},
		{QuestID: "small-regular", Name: "Small Regular quest", Status: "locked"},
	}}
	if reverse {
		tracked.Items[0].Sources[0], tracked.Items[0].Sources[1] = tracked.Items[0].Sources[1], tracked.Items[0].Sources[0]
		quests.Quests[0], quests.Quests[1] = quests.Quests[1], quests.Quests[0]
	}
	snapshot, err := localprogress.Merge(tracked, quests, market.HideoutSnapshot{
		CharUID: tracked.CharUID, GameMode: tracked.GameMode, Revision: tracked.Revision, Modules: []market.HideoutUpgrade{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return item, snapshot
}

func TestRequirementRowsUseTheSameCurrentAllocationAsNextItemTargets(t *testing.T) {
	for _, stock := range []int64{0, 1, 3, 4, 5, 14, 17} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("stock-%d-reverse-%t", stock, reverse), func(t *testing.T) {
				item, snapshot := mixedGoalRequirementSnapshot(t, stock, reverse)
				before := localprogress.Clone(snapshot)
				forecast := nextitem.Calculate(snapshot, item.BSGID)
				addAllowed, _ := localprogress.AddAllowed(snapshot, item.BSGID)
				view := localTrackedView(item, snapshot)
				assertConfirmedRequirementRow(t, view, "fir", min(max(0, stock-4), 10), 10)
				assertConfirmedRequirementRow(t, view, "regular", min(stock, 4), 4)
				if view.AvailableQuantity != stock || view.RequiredQuantity != 14 || view.AddAllowed != addAllowed {
					t.Fatalf("a megjelenítés megváltoztatta a készletet, az igényt vagy az Add korlátját: %+v", view)
				}
				wantForecast, _ := json.Marshal(forecast)
				gotForecast, _ := json.Marshal(view.NextItem)
				if string(gotForecast) != string(wantForecast) || !reflect.DeepEqual(snapshot, before) {
					t.Fatal("a kategóriaszámláló módosította a Next Item értékeit vagy a bemenetet")
				}
				for _, target := range view.NextItem.Targets {
					want := min(stock, 4)
					if target.GoalKey == "quest:large-fir" {
						want = min(max(0, stock-4), 10)
					}
					if target.AvailableQuantity != want || target.AllocationUnknown {
						t.Fatalf("az összesítő eltér a küldetéskiosztástól: %+v", target)
					}
				}
			})
		}
	}
}

func TestRequirementRowsKeepTheConfirmedGoalAllocationDuringPendingChanges(t *testing.T) {
	for _, pendingStock := range []int64{0, 2, 5} {
		item, snapshot := mixedGoalRequirementSnapshot(t, 1, false)
		snapshot.Items[0].AvailableQuantity = pendingStock
		before := localprogress.Clone(snapshot)
		view := localTrackedView(item, snapshot)
		assertConfirmedRequirementRow(t, view, "fir", 0, 10)
		assertConfirmedRequirementRow(t, view, "regular", 1, 4)
		if view.AvailableQuantity != pendingStock || view.NextItem == nil || view.NextItem.Status != "unavailable" || len(view.NextItem.Targets) != 0 {
			t.Fatalf("a pending készletet megerősítettként mutatja a nézet: %+v", view)
		}
		if !reflect.DeepEqual(snapshot, before) {
			t.Fatal("a megerősített kiosztás felülírta a pending készletet")
		}
	}
}

func TestRequirementRowsPendingOtherItemDoesNotRestoreFIRFirst(t *testing.T) {
	item, snapshot := mixedGoalRequirementSnapshot(t, 1, false)
	snapshot.Items = append(snapshot.Items, localprogress.Item{ItemID: "other-pending-item", Name: "Other item", MarketTracked: true,
		AvailableQuantity: 1, MarketAvailableQuantity: 0})
	view := localTrackedView(item, snapshot)
	assertConfirmedRequirementRow(t, view, "fir", 0, 10)
	assertConfirmedRequirementRow(t, view, "regular", 1, 4)
	if view.NextItem == nil || view.NextItem.Status != "unavailable" {
		t.Fatalf("a másik tárgy pending állapota mellett megváltozott a Next Item szerződése: %+v", view.NextItem)
	}
}
