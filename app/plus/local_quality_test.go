package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

func requirementStock(t *testing.T, state ItemState, kind string) (int64, int64) {
	t.Helper()
	for _, row := range state.Tracking.Requirements {
		if row.Kind == kind {
			return row.AvailableQuantity, row.RequiredQuantity
		}
	}
	return 0, 0
}

func TestLocalTypedStockUsesMarketQuotaAndSeparateQualityMetadata(t *testing.T) {
	a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	dev := a.devRequirements.(*fakeRequirements)
	dev.catalog.Tasks[1].Objectives[0].FoundInRaid = true
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	if available, required := requirementStock(t, state, "regular"); available != 2 || required != 3 {
		t.Fatalf("F1 did not show Regular 2/3: %+v", state.Tracking)
	}
	if available, required := requirementStock(t, state, "fir"); available != 0 || required != 5 {
		t.Fatalf("F1 incorrectly covered FiR: %+v", state.Tracking)
	}
	state = addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 3 || fake.writes[2].Quantity != 3 || state.Tracking.AvailableQuantity != 3 {
		t.Fatalf("F2 did not increase the same Market stock: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	if available, required := requirementStock(t, state, "fir"); available != 1 || required != 5 {
		t.Fatalf("F2 did not show FiR 1/5: %+v", state.Tracking)
	}
	for i := 0; i < 5; i++ {
		state = addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	}
	if len(fake.writes) != 3 || state.Tracking.AvailableQuantity != 8 || state.Tracking.RequiredQuantity != 8 || state.Tracking.RemainingQuantity != 0 {
		t.Fatalf("typed local remainder changed Market quota or missed FiR overflow: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	if available, required := requirementStock(t, state, "fir"); available != 5 || required != 5 {
		t.Fatalf("FiR requirement not met exactly: %+v", state.Tracking)
	}
	if available, required := requirementStock(t, state, "regular"); available != 3 || required != 3 {
		t.Fatalf("FiR overflow did not cover the last Regular requirement once: %+v", state.Tracking)
	}
	assertLocalStockFileOnly(t, dir, trackingBSGID, 5)
	saved, err := a.localStore.Load("character-a", "PVP")
	if err != nil || len(saved.Items) != 1 {
		t.Fatalf("typed local metadata could not be loaded: %+v %v", saved, err)
	}
	row := saved.Items[0]
	if row.MarketRegularQuantity != 2 || row.MarketFIRQuantity != 1 || row.LocalFIRQuantity != 5 || row.LocalRegularQuantity != 0 || row.MarketAvailableQuantity != 0 {
		t.Fatalf("Market type annotations doubled or replaced the separate local count: %+v", row)
	}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if state = a.GetState(); state.Tracking.AvailableQuantity != 8 || state.Tracking.RemainingQuantity != 0 {
		t.Fatalf("refresh lost typed stock: %+v", state.Tracking)
	}
}

func TestLocalAnyQualitySharesProgressAndStopsAlternativeCollection(t *testing.T) {
	for _, fir := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "fir"}[fir], func(t *testing.T) {
			a, fake := newDevOnlyTestApp(t)
			const second = "591aef7986f774139d495f03"
			a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest-b", Objectives: []tarkovdev.Objective{
				{ID: "any-three", Type: "giveItem", Count: 3, FoundInRaid: fir, ItemIDs: []string{trackingBSGID, second}},
			}}}, Hideout: []tarkovdev.Station{}}}
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if fir {
				addAndSyncCachedTest(t, a, trackingItemUID, "fir")
			} else {
				addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			}
			a.state.Item = &market.Item{UID: "second-item", BSGID: second, Name: "Second item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state := addAndSyncCachedTest(t, a, "second-item", "fir")
			want := int64(2)
			if len(fake.writes) != 0 || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != want || state.Tracking.AnyGroups[0].RequiredQuantity != 3 {
				t.Fatalf("Any progress did not share typed candidate stock: %+v", state.Tracking)
			}
			a.state.Item = &market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "First item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state = a.GetState(); state.Tracking.AnyGroups[0].AvailableQuantity != want || state.Tracking.RequiredQuantity != 2 {
				t.Fatalf("first candidate did not reflect the shared deficit: %+v", state.Tracking)
			}
			for i := want; i < 3; i++ {
				state = addAndSyncCachedTest(t, a, trackingItemUID, "fir")
			}
			if state.Tracking.RequiredQuantity != 0 || len(state.Tracking.AnyGroups) != 0 || a.canAddLocalForTest() || len(state.Local.QuestItems) != 0 {
				t.Fatalf("covered Any kept requesting alternatives: %+v", state)
			}
			before := state.Tracking.AvailableQuantity
			state = addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			if state.Tracking.AvailableQuantity != before {
				t.Fatal("covered Any accepted another item")
			}
			a.localAt = time.Time{}
			a.state.Item = &market.Item{UID: "second-item", BSGID: second, Name: "Second item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state = a.GetState(); state.Tracking.RequiredQuantity != 0 || len(state.Tracking.AnyGroups) != 0 {
				t.Fatalf("refresh or candidate change reopened a covered Any group: %+v", state.Tracking)
			}
		})
	}
}

func TestLocalMarketReadbackDecreaseReconcilesQualityBeforeValidation(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	if a.localSnapshot.Items[0].MarketFIRQuantity != 2 {
		t.Fatal("confirmed Market FiR annotations were not established")
	}
	fake.snapshot.Items[0].AvailableQuantity = 1
	fake.snapshot.Revision = "external-decrease"
	a.applyTracking(a.apiKey, a.state.ItemOptions, *a.state.Item, fake.snapshot, time.Now(), "")
	state := a.GetState()
	if state.Local.StockError || state.Local.Error != "" || state.Tracking.AvailableQuantity != 1 || a.localSnapshot.Items[0].MarketFIRQuantity != 0 {
		t.Fatalf("stale quality prevented safe live stock reconciliation: %+v snapshot=%+v", state, a.localSnapshot)
	}
	saved, err := a.localStore.Load("character-a", "PVP")
	if err != nil || len(saved.Items) != 1 || saved.Items[0].MarketFIRQuantity != 0 {
		t.Fatalf("stale Market type survived external decrease: %+v err=%v", saved, err)
	}
}

func TestLocalThreeQuestsReserveFixedAndCoverTwoAnyGroupsOnce(t *testing.T) {
	for _, allAugmentin := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed-medical", true: "all-augmentin"}[allAugmentin], func(t *testing.T) {
			const augmentin = "augmentin-item"
			const salewa = "salewa-item"
			const ifak = "ifak-item"
			a, fake, _ := newSupplementedLocalTestApp(t, "augmentin-uid", augmentin)
			fake.quests.Quests = append(fake.quests.Quests, market.Quest{QuestID: "quest-c", Status: "available"})
			a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{
				{ID: "quest-a", Objectives: []tarkovdev.Objective{{ID: "fixed", Type: "giveItem", Count: 3, ItemIDs: []string{augmentin}}}},
				{ID: "quest-b", Objectives: []tarkovdev.Objective{{ID: "any", Type: "giveItem", Count: 3, ItemIDs: []string{augmentin, salewa, ifak}}}},
				{ID: "quest-c", Objectives: []tarkovdev.Objective{{ID: "any", Type: "giveItem", Count: 3, ItemIDs: []string{augmentin, salewa, ifak}}}},
			}, Hideout: []tarkovdev.Station{}}}
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			var state ItemState
			for i := 0; i < 3; i++ {
				state = addAndSyncCachedTest(t, a, "augmentin-uid", "regular")
			}
			if len(fake.writes) != 3 || len(state.Tracking.AnyGroups) != 1 {
				t.Fatalf("fixed quota or combined Any display lost: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
			for _, group := range state.Tracking.AnyGroups {
				if group.AvailableQuantity != 0 || group.RequiredQuantity != 6 {
					t.Fatalf("the fixed three Augmentin were reused in Any: %+v", state.Tracking)
				}
			}
			items := []struct{ uid, id string }{{"salewa-uid", salewa}, {"ifak-uid", ifak}}
			if allAugmentin {
				items = []struct{ uid, id string }{{"augmentin-uid", augmentin}, {"augmentin-uid", augmentin}}
			}
			for index, item := range items {
				a.state.Item = &market.Item{UID: item.uid, BSGID: item.id, Name: item.id}
				a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
				for i := 0; i < 3; i++ {
					state = addAndSyncCachedTest(t, a, item.uid, "regular")
				}
				if index == 0 && (len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 3 || state.Tracking.AnyGroups[0].RequiredQuantity != 6) {
					t.Fatalf("three extras incorrectly covered both Any groups: %+v", state.Tracking)
				}
			}
			var stock int64
			for _, row := range a.localSnapshot.Items {
				stock += row.AvailableQuantity
			}
			if stock != 9 || len(fake.writes) != 3 || len(state.Tracking.AnyGroups) != 0 {
				t.Fatalf("nine pieces did not cover exactly the three goals: stock=%d writes=%+v state=%+v", stock, fake.writes, state.Tracking)
			}
			a.state.Item = &market.Item{UID: "augmentin-uid", BSGID: augmentin, Name: augmentin}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state = a.GetState()
			if state.Tracking.RequiredQuantity != 3 || state.Tracking.RemainingQuantity != 0 || len(state.Tracking.AnyGroups) != 0 || len(state.Local.QuestItems) != 1 {
				t.Fatalf("covered alternatives hid or enlarged the remaining fixed requirement: %+v", state)
			}
		})
	}
}
