package plus

import (
	"reflect"
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

func TestLocalSharedStockFillsMixedRequirementRowsWithoutQualityMetadata(t *testing.T) {
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	fake.snapshot.Items[0].Sources[1].FIR = true
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	var state ItemState
	for i := 1; i <= 8; i++ {
		state = addMarketItemForTest(t, a, trackingItemUID)
		if state.Tracking.AvailableQuantity != int64(i) || state.Tracking.AddAllowed != (i < 8) {
			t.Fatalf("wrong shared stock limit after %d: %+v", i, state.Tracking)
		}
	}
	if fir, required := requirementStock(t, state, "fir"); fir != 5 || required != 5 {
		t.Fatalf("FiR requirement changed: %+v", state.Tracking)
	}
	if regular, required := requirementStock(t, state, "regular"); regular != 3 || required != 3 {
		t.Fatalf("regular requirement changed: %+v", state.Tracking)
	}
	before := state.Tracking
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state = a.GetState()
	if !reflect.DeepEqual(before.Requirements, state.Tracking.Requirements) || state.Tracking.AvailableQuantity != 8 || state.Tracking.AddAllowed {
		t.Fatalf("refresh changed generic allocation: %+v", state.Tracking)
	}
	addMarketItemForTest(t, a, trackingItemUID)
	if len(fake.writes) != 8 {
		t.Fatalf("completed mixed requirements accepted another add: %+v", fake.writes)
	}
	assertNoMarketStockFilesForTest(t, a.dir)
}

func TestLocalAnySourceKindsShareProgressAndStopsAlternativeCollection(t *testing.T) {
	for _, fir := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "fir"}[fir], func(t *testing.T) {
			a, fake := newLocalTestApp(t)
			const second = "591aef7986f774139d495f03"
			setMarketGroupRowsForTest(fake, nil, marketQuestGroupForTest("quest-b", "any-three", 3, fir, trackingBSGID, second))
			fake.groupProgressByCommit = [][]int64{{1}, {2}, {3}}
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			addMarketItemForTest(t, a, trackingItemUID)
			a.state.Item = &market.Item{UID: "second-item", BSGID: second, Name: "Second item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state := addMarketItemForTest(t, a, "second-item")
			want := int64(2)
			if len(fake.writes) != 2 || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != want || state.Tracking.AnyGroups[0].RequiredQuantity != 3 {
				t.Fatalf("Any progress did not share candidate stock: %+v", state.Tracking)
			}
			a.state.Item = &market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "First item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state = a.GetState(); state.Tracking.AnyGroups[0].AvailableQuantity != want || state.Tracking.RequiredQuantity != 2 {
				t.Fatalf("first candidate did not reflect the shared deficit: %+v", state.Tracking)
			}
			for i := want; i < 3; i++ {
				state = addMarketItemForTest(t, a, trackingItemUID)
			}
			if state.Tracking.RequiredQuantity != 2 || state.Tracking.RemainingQuantity != 0 || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 3 || state.Tracking.AnyGroups[0].RequiredQuantity != 3 || state.Tracking.AnyGroups[0].FIR != fir ||
				state.Tracking.AddAllowed {
				t.Fatalf("covered Any lost its full ratio or kept requesting alternatives: %+v", state.Tracking)
			}
			before := state.Tracking.AvailableQuantity
			state = addMarketItemForTest(t, a, trackingItemUID)
			if state.Tracking.AvailableQuantity != before {
				t.Fatal("covered Any accepted another item")
			}
			a.localAt = time.Time{}
			a.state.Item = &market.Item{UID: "second-item", BSGID: second, Name: "Second item"}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state = a.GetState(); state.Tracking.RequiredQuantity != 1 || state.Tracking.RemainingQuantity != 0 || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 3 || state.Tracking.AnyGroups[0].RequiredQuantity != 3 || state.Tracking.AnyGroups[0].FIR != fir ||
				state.Tracking.AddAllowed {
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

func TestLocalMarketReadbackDecreaseReconcilesSharedStock(t *testing.T) {
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	addMarketItemForTest(t, a, trackingItemUID)
	addMarketItemForTest(t, a, trackingItemUID)
	fake.snapshot.Items[0].AvailableQuantity = 1
	fake.snapshot.Revision = "external-decrease"
	setTrackingTestSnapshot(t, a, fake.snapshot)
	state := a.GetState()
	if state.Local.Error != "" || state.Tracking.AvailableQuantity != 1 || !state.Tracking.AddAllowed {
		t.Fatalf("live decrease did not reopen shared deficit: %+v", state)
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
			fake.groupProgressByCommit = [][]int64{{0, 0}, {0, 0}, {0, 0}, {1, 0}, {2, 0}, {3, 0}, {3, 1}, {3, 2}, {3, 3}}
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			var state ItemState
			for i := 0; i < 3; i++ {
				state = addMarketItemForTest(t, a, "augmentin-uid")
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
					state = addMarketItemForTest(t, a, item.uid)
				}
				if index == 0 && (len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 3 || state.Tracking.AnyGroups[0].RequiredQuantity != 6) {
					t.Fatalf("three extras incorrectly covered both Any groups: %+v", state.Tracking)
				}
			}
			if !allAugmentin {
				if len(fake.writes) != 8 || state.Tracking.AddAllowed || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 5 {
					t.Fatalf("IFAK quota did not stop at two while the API still reported five of six: writes=%+v state=%+v", fake.writes, state.Tracking)
				}
				a.state.Item = &market.Item{UID: "augmentin-uid", BSGID: augmentin, Name: augmentin}
				a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
				state = a.GetState()
				if state.Tracking.AvailableQuantity != 3 || state.Tracking.RequiredQuantity != 4 || !state.Tracking.AddAllowed {
					t.Fatalf("the remaining API deficit was not collectable with Augmentin: %+v", state.Tracking)
				}
				state = addMarketItemForTest(t, a, "augmentin-uid")
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
			wantTarget := int64(4)
			if allAugmentin {
				wantTarget = 9
			}
			if state.Tracking.AvailableQuantity != wantTarget || state.Tracking.RequiredQuantity != wantTarget || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddAllowed || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 6 || state.Tracking.AnyGroups[0].RequiredQuantity != 6 {
				t.Fatalf("covered alternatives lost their ratio or changed the fixed requirement: %+v", state.Tracking)
			}
		})
	}
}
