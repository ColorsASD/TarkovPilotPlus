package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
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
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	for i := range fake.snapshot.Items[0].Sources {
		if fake.snapshot.Items[0].Sources[i].QuestID == "quest-b" {
			fake.snapshot.Items[0].Sources[i].FIR = true
		}
	}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	addMarketKindForTest(t, a, trackingItemUID, "regular")
	state := addMarketKindForTest(t, a, trackingItemUID, "regular")
	if available, required := requirementStock(t, state, "regular"); available != 2 || required != 3 {
		t.Fatalf("Regular Add did not show Regular 2/3: %+v", state.Tracking)
	}
	if available, required := requirementStock(t, state, "fir"); available != 0 || required != 5 {
		t.Fatalf("Regular Add incorrectly covered FiR: %+v", state.Tracking)
	}
	state = addMarketKindForTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 3 || fake.writes[2].Quantity != 3 || state.Tracking.AvailableQuantity != 3 {
		t.Fatalf("FiR Add did not increase the same Market stock: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	if available, required := requirementStock(t, state, "fir"); available != 1 || required != 5 {
		t.Fatalf("FiR Add did not show FiR 1/5: %+v", state.Tracking)
	}
	for i := 0; i < 5; i++ {
		state = addMarketKindForTest(t, a, trackingItemUID, "fir")
	}
	if len(fake.writes) != 8 || state.Tracking.AvailableQuantity != 8 || state.Tracking.RequiredQuantity != 8 || state.Tracking.RemainingQuantity != 0 {
		t.Fatalf("Market additions missed FiR overflow: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	if available, required := requirementStock(t, state, "fir"); available != 5 || required != 5 {
		t.Fatalf("FiR requirement not met exactly: %+v", state.Tracking)
	}
	if available, required := requirementStock(t, state, "regular"); available != 3 || required != 3 {
		t.Fatalf("FiR overflow did not cover the last Regular requirement once: %+v", state.Tracking)
	}
	row := memoryMarketStockForTest(t, a, trackingBSGID)
	if row.MarketRegularQuantity != 2 || row.MarketFIRQuantity != 6 || row.MarketAvailableQuantity != 8 {
		t.Fatalf("confirmed Market types were duplicated or lost in memory: %+v", row)
	}
	assertNoMarketStockFilesForTest(t, a.dir)
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if state = a.GetState(); state.Tracking.AvailableQuantity != 8 || state.Tracking.RemainingQuantity != 0 {
		t.Fatalf("refresh lost typed stock: %+v", state.Tracking)
	}
}

func TestLocalAnyQualitySharesProgressAndStopsAlternativeCollection(t *testing.T) {
	for _, fir := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "fir"}[fir], func(t *testing.T) {
			a, fake := newLocalTestApp(t)
			const second = "591aef7986f774139d495f03"
			setMarketGroupRowsForTest(fake, nil, marketQuestGroupForTest("quest-b", "any-three", 3, fir, trackingBSGID, second))
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if fir {
				addMarketKindForTest(t, a, trackingItemUID, "fir")
			} else {
				addMarketKindForTest(t, a, trackingItemUID, "regular")
			}
			a.state.Item = &market.Item{UID: "second-item", BSGID: second, Name: "Second item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state := addMarketKindForTest(t, a, "second-item", "fir")
			want := int64(2)
			if len(fake.writes) != 2 || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != want || state.Tracking.AnyGroups[0].RequiredQuantity != 3 {
				t.Fatalf("Any progress did not share typed candidate stock: %+v", state.Tracking)
			}
			a.state.Item = &market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "First item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state = a.GetState(); state.Tracking.AnyGroups[0].AvailableQuantity != want || state.Tracking.RequiredQuantity != 2 {
				t.Fatalf("first candidate did not reflect the shared deficit: %+v", state.Tracking)
			}
			for i := want; i < 3; i++ {
				state = addMarketKindForTest(t, a, trackingItemUID, "fir")
			}
			if state.Tracking.RequiredQuantity != 0 || state.Tracking.RemainingQuantity != 0 || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 3 || state.Tracking.AnyGroups[0].RequiredQuantity != 3 || state.Tracking.AnyGroups[0].FIR != fir ||
				a.canAddLocalForTest() {
				t.Fatalf("covered Any lost its full ratio or kept requesting alternatives: %+v", state)
			}
			before := state.Tracking.AvailableQuantity
			state = addMarketKindForTest(t, a, trackingItemUID, "regular")
			if state.Tracking.AvailableQuantity != before {
				t.Fatal("covered Any accepted another item")
			}
			a.localAt = time.Time{}
			a.state.Item = &market.Item{UID: "second-item", BSGID: second, Name: "Second item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state = a.GetState(); state.Tracking.RequiredQuantity != 0 || state.Tracking.RemainingQuantity != 0 || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 3 || state.Tracking.AnyGroups[0].RequiredQuantity != 3 || state.Tracking.AnyGroups[0].FIR != fir ||
				state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed {
				t.Fatalf("refresh or candidate change lost a full Any ratio or reopened collection: %+v", state.Tracking)
			}
			fake.quests.Quests[1].Completed, fake.quests.Quests[1].Status = true, "completed"
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state = a.GetState(); state.Tracking.RequiredQuantity != 0 || len(state.Tracking.AnyGroups) != 0 ||
				len(state.Tracking.CompletedSources) != 1 || state.Tracking.CompletedSources[0].Quantity != 3 {
				t.Fatalf("completed quest kept an active full Any requirement: %+v", state.Tracking)
			}
		})
	}
}

func TestLocalMarketReadbackDecreaseReconcilesQualityBeforeValidation(t *testing.T) {
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	addMarketKindForTest(t, a, trackingItemUID, "fir")
	addMarketKindForTest(t, a, trackingItemUID, "fir")
	if a.localSnapshot.Items[0].MarketFIRQuantity != 2 {
		t.Fatal("confirmed Market FiR annotations were not established")
	}
	fake.snapshot.Items[0].AvailableQuantity = 1
	fake.snapshot.Revision = "external-decrease"
	setTrackingTestSnapshot(t, a, fake.snapshot)
	state := a.GetState()
	if state.Local.Error != "" || state.Tracking.AvailableQuantity != 1 || a.localSnapshot.Items[0].MarketFIRQuantity != 0 {
		t.Fatalf("stale quality prevented safe live stock reconciliation: %+v snapshot=%+v", state, a.localSnapshot)
	}
	assertNoMarketStockFilesForTest(t, a.dir)
}

func TestLocalThreeQuestsReserveFixedAndCoverTwoAnyGroupsOnce(t *testing.T) {
	for _, allAugmentin := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed-medical", true: "all-augmentin"}[allAugmentin], func(t *testing.T) {
			const augmentin = "augmentin-item"
			const salewa = "salewa-item"
			const ifak = "ifak-item"
			a, fake, _ := newMarketQuotaTestApp(t, "augmentin-uid", augmentin)
			fake.quests.Quests = append(fake.quests.Quests, market.Quest{QuestID: "quest-c", Status: "available"})
			setMarketGroupRowsForTest(fake, []market.TrackedItem{{ItemID: augmentin, RequiredQuantity: 3, RemainingQuantity: 3,
				Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-a", Amount: 3, RemainingRequiredQuantity: 3}},
			}}, marketQuestGroupForTest("quest-b", "any", 3, false, augmentin, salewa, ifak),
				marketQuestGroupForTest("quest-c", "any", 3, false, augmentin, salewa, ifak))
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			var state ItemState
			for i := 0; i < 3; i++ {
				state = addMarketKindForTest(t, a, "augmentin-uid", "regular")
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
					state = addMarketKindForTest(t, a, item.uid, "regular")
				}
				if index == 0 && (len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 3 || state.Tracking.AnyGroups[0].RequiredQuantity != 6) {
					t.Fatalf("three extras incorrectly covered both Any groups: %+v", state.Tracking)
				}
			}
			var stock int64
			for _, row := range a.localSnapshot.Items {
				stock += row.AvailableQuantity
			}
			if stock != 9 || len(fake.writes) != 9 || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 6 || state.Tracking.AnyGroups[0].RequiredQuantity != 6 {
				t.Fatalf("nine pieces did not cover exactly the three goals: stock=%d writes=%+v state=%+v", stock, fake.writes, state.Tracking)
			}
			a.state.Item = &market.Item{UID: "augmentin-uid", BSGID: augmentin, Name: augmentin}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state = a.GetState()
			if state.Tracking.RequiredQuantity != 3 || state.Tracking.RemainingQuantity != 0 || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 6 || state.Tracking.AnyGroups[0].RequiredQuantity != 6 {
				t.Fatalf("covered alternatives lost their ratio or changed the fixed requirement: %+v", state)
			}
		})
	}
}
