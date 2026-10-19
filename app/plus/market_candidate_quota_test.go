package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const quotaOtherItem = "ymxc-drink"

// A Market stock es csoportprogressz kulon, explicit fixture-adat.
// A masik jelolt fix igenye nem foglalja le meg egyszer az API Any aranyat.
func newMarketCandidateQuotaTestApp(t *testing.T, ownStock, otherStock int64) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newTrackingTestApp(t)
	a.state.Item.Name = "Water bottle"
	fake.snapshot.Items = []market.TrackedItem{
		{ItemID: trackingBSGID, Name: "Water bottle", RequiredQuantity: 2, AvailableQuantity: ownStock, RemainingQuantity: max(0, 2-ownStock),
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "water-fixed", Amount: 2, RemainingRequiredQuantity: 2}}},
		{ItemID: quotaOtherItem, Name: "YMXC", RequiredQuantity: 1, AvailableQuantity: otherStock, RemainingQuantity: max(0, 1-otherStock),
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "ymxc-fixed", Amount: 1, RemainingRequiredQuantity: 1}}},
	}
	fake.snapshot.Groups = []market.TrackedGroup{{QuestID: "operation-aquarius", QuestName: "Operation Aquarius", RequirementID: "drinks", Name: "Drinks",
		Amount: 3, RequiredQuantity: 3, AvailableQuantity: min(3, ownStock+otherStock), RemainingQuantity: max(0, 3-ownStock-otherStock),
		ItemIDs: []string{trackingBSGID, quotaOtherItem}}}
	setTrackingTestSnapshot(t, a, fake.snapshot)
	return a, fake
}

func assertMarketCandidateQuota(t *testing.T, state ItemState, stock, target, apiAny int64, allowed bool) {
	t.Helper()
	v := state.Tracking
	if v == nil || v.AvailableQuantity != stock || v.RequiredQuantity != target || v.RemainingQuantity != max(0, target-stock) || v.AddAllowed != allowed {
		t.Fatalf("candidate stock must stay %d/%d with Add=%v: %+v", stock, target, allowed, v)
	}
	if len(v.AnyGroups) != 1 || v.AnyGroups[0].AvailableQuantity != apiAny || v.AnyGroups[0].RequiredQuantity != 3 {
		t.Fatalf("candidate target changed the explicit API Any %d/3: %+v", apiAny, v)
	}
	if available, required := requirementStock(t, state, "regular"); available != min(v.MarketAvailableQuantity, 2) || required != 2 {
		t.Fatalf("fixed two-water requirement changed: %+v", v)
	}
}

func TestMarketCandidateQuotaRepeatedAddsKeepFourUntilFull(t *testing.T) {
	a, fake := newMarketCandidateQuotaTestApp(t, 0, 1)
	fake.groupProgressByCommit = [][]int64{{2}, {3}, {3}, {3}}
	assertMarketCandidateQuota(t, a.GetState(), 0, 4, 1, true)
	for i := int64(1); i <= 4; i++ {
		state := addMarketItemForTest(t, a, trackingItemUID)
		assertMarketCandidateQuota(t, state, i, 4, min(3, i+1), i < 4)
		a.localAt = time.Time{}
		a.refreshTracking()
		assertMarketCandidateQuota(t, a.GetState(), i, 4, min(3, i+1), i < 4)
	}
	state := addMarketItemForTest(t, a, trackingItemUID)
	assertMarketCandidateQuota(t, state, 4, 4, 3, false)
	if len(fake.writes) != 4 || fake.snapshot.Items[1].AvailableQuantity != 1 {
		t.Fatalf("four water adds altered YMXC stock or accepted a fifth: writes=%+v items=%+v", fake.writes, fake.snapshot.Items)
	}
	for i, write := range fake.writes {
		if write.Item != trackingBSGID || write.Quantity != int64(i+1) {
			t.Fatalf("candidate add skipped or changed its target: %+v", fake.writes)
		}
	}
	assertNoMarketStockFilesForTest(t, a.dir)
}

func TestMarketCandidateQuotaPendingRapidAddsReserveExactlyFour(t *testing.T) {
	a, fake := newMarketCandidateQuotaTestApp(t, 0, 1)
	fake.groupProgressByCommit = [][]int64{{2}, {3}, {3}, {3}}
	started, release := blockFirstPendingReadForTest(t, fake)
	state := a.AddTrackedItem(trackingItemUID)
	<-started
	assertMarketCandidateQuota(t, state, 1, 4, 1, true)
	for i := int64(2); i <= 4; i++ {
		state = a.AddTrackedItem(trackingItemUID)
		assertMarketCandidateQuota(t, state, i, 4, 1, i < 4)
		if state.PendingAdds.Count != int(i) {
			t.Fatalf("rapid click %d lost a reservation: %+v", i, state.PendingAdds)
		}
	}
	state = a.AddTrackedItem(trackingItemUID)
	assertMarketCandidateQuota(t, state, 4, 4, 1, false)
	if state.PendingAdds.Count != 4 || len(fake.writes) != 0 {
		t.Fatalf("pending candidate limit sent a write or accepted a fifth: %+v", state)
	}
	release()
	state = waitPendingForTest(t, a)
	assertMarketCandidateQuota(t, state, 4, 4, 3, false)
	if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || len(fake.writes) != 4 || fake.snapshot.Items[1].AvailableQuantity != 1 {
		t.Fatalf("confirmed rapid additions were stopped early or duplicated: state=%+v writes=%+v", state, fake.writes)
	}
}

func TestMarketCandidateQuotaOwnStockDoesNotReduceItsTargetAgain(t *testing.T) {
	for ownStock := int64(0); ownStock <= 4; ownStock++ {
		a, _ := newMarketCandidateQuotaTestApp(t, ownStock, 1)
		assertMarketCandidateQuota(t, a.GetState(), ownStock, 4, min(3, ownStock+1), ownStock < 4)
	}
	for otherStock := int64(0); otherStock <= 3; otherStock++ {
		a, _ := newMarketCandidateQuotaTestApp(t, 0, otherStock)
		assertMarketCandidateQuota(t, a.GetState(), 0, 5-otherStock, min(3, otherStock), true)
	}
}

func TestMarketCandidateQuotaFreshOtherStockStopsObsoleteAdd(t *testing.T) {
	a, fake := newMarketCandidateQuotaTestApp(t, 3, 1)
	assertMarketCandidateQuota(t, a.GetState(), 3, 4, 3, true)
	fake.snapshot.Items[1].AvailableQuantity = 2
	fake.snapshot.Revision = "other-candidate-increased"
	setFakeMarketGroupProgress(&fake.snapshot, 3)
	state := addMarketItemForTest(t, a, trackingItemUID)
	assertMarketCandidateQuota(t, state, 3, 3, 3, false)
	if len(fake.writes) != 0 || state.PendingAdds.Count != 0 {
		t.Fatalf("fresh other-item stock did not stop stale Add: %+v writes=%+v", state, fake.writes)
	}
}

func TestMarketCandidateQuotaOtherPendingStockReducesOnlyOtherCandidateTarget(t *testing.T) {
	a, fake := newMarketCandidateQuotaTestApp(t, 0, 1)
	fake.groupProgressByCommit = [][]int64{{2}, {3}, {3}, {3}}
	started, release := blockFirstPendingReadForTest(t, fake)
	a.mu.Lock()
	water := *a.state.Item
	other := market.Item{UID: "ymxc-uid", BSGID: quotaOtherItem, Name: "YMXC"}
	a.state.Item = &other
	a.state.Tracking = a.pendingTrackedViewLocked(other)
	a.mu.Unlock()
	a.AddTrackedItem(other.UID)
	<-started
	a.mu.Lock()
	a.state.Item = &water
	a.state.Tracking = a.pendingTrackedViewLocked(water)
	a.mu.Unlock()
	assertMarketCandidateQuota(t, a.GetState(), 0, 3, 1, true)
	for i := int64(1); i <= 3; i++ {
		state := a.AddTrackedItem(water.UID)
		assertMarketCandidateQuota(t, state, i, 3, 1, i < 3)
	}
	state := a.AddTrackedItem(water.UID)
	assertMarketCandidateQuota(t, state, 3, 3, 1, false)
	if state.PendingAdds.Count != 4 || len(fake.writes) != 0 {
		t.Fatalf("other pending candidate did not lower the water reservation limit: %+v", state)
	}
	release()
	state = waitPendingForTest(t, a)
	assertMarketCandidateQuota(t, state, 3, 3, 3, false)
	if state.PendingAdds.Count != 0 || len(fake.writes) != 4 || fake.snapshot.Items[1].AvailableQuantity != 2 {
		t.Fatalf("other pending piece was counted again after confirmation: state=%+v writes=%+v", state, fake.writes)
	}
}
