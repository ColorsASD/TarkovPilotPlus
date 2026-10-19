package plus

import (
	"context"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"testing"
	"time"
)

type fakeCharacterProgress struct {
	*fakeTrackingAPI
	quests                   market.QuestSnapshot
	hideout                  market.HideoutSnapshot
	questReads, hideoutReads int
	followTrackedRevision    bool
}

func (f *fakeCharacterProgress) LoadQuests(context.Context, string, string, string) (market.QuestSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.questReads++
	snapshot := f.quests
	if f.followTrackedRevision {
		snapshot.Revision = f.snapshot.Revision
	}
	return snapshot, nil
}
func (f *fakeCharacterProgress) LoadHideout(context.Context, string, string, string) (market.HideoutSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hideoutReads++
	snapshot := f.hideout
	if f.followTrackedRevision {
		snapshot.Revision = f.snapshot.Revision
	}
	return snapshot, nil
}

func newLocalTestApp(t *testing.T) (*ItemWindowApp, *fakeCharacterProgress) {
	a, base := newTrackingTestApp(t)
	base.snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", QuestID: "quest-a", Amount: 1, RemainingRequiredQuantity: 1, FIR: true},
		{Type: "quest", QuestID: "quest-a", Amount: 2, RemainingRequiredQuantity: 2},
		{Type: "quest", QuestID: "quest-b", Amount: 1, RemainingRequiredQuantity: 1, FIR: true},
		{Type: "quest", QuestID: "quest-b", Amount: 3, RemainingRequiredQuantity: 3},
	}
	fake := &fakeCharacterProgress{fakeTrackingAPI: base, followTrackedRevision: true,
		quests: market.QuestSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "revision-1", Quests: []market.Quest{
			{QuestID: "quest-a", Name: "First quest", Status: "available"}, {QuestID: "quest-b", Name: "Second quest", Status: "locked"},
		}},
		hideout: market.HideoutSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "revision-1", Modules: []market.HideoutUpgrade{}},
	}
	a.tracker = fake
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	return a, fake
}
func newMarketQuotaTestApp(t *testing.T, uid, itemID string) (*ItemWindowApp, *fakeCharacterProgress, string) {
	t.Helper()
	a, fake := newLocalTestApp(t)
	a.state.Item = &market.Item{UID: uid, BSGID: itemID, Name: "Market item metadata"}
	fake.snapshot.Items = []market.TrackedItem{{ItemID: itemID, Name: "Market item metadata", RequiredQuantity: 8, RemainingQuantity: 8,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-a", Amount: 3, RemainingRequiredQuantity: 3}, {Type: "quest", QuestID: "quest-b", Amount: 5, RemainingRequiredQuantity: 5}}}}
	storeDir := t.TempDir()
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item); err != nil {
		t.Fatal(err)
	}
	return a, fake, storeDir
}
func (a *ItemWindowApp) canAddLocalForTest() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.canAddTrackedItemLocked()
}
func TestLocalModeMarketItemWritesMarketAndMirrorsStock(t *testing.T) {
	a, fake := newLocalTestApp(t)
	state := addMarketKindForTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Storage != "market" {
		t.Fatalf("Market membership did not route to API: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	if state.Tracking.RequiredQuantity != 7 {
		t.Fatalf("merged view lost confirmed Market stock or requirements: %+v", state)
	}
	fake.quests.Revision, fake.hideout.Revision = fake.snapshot.Revision, fake.snapshot.Revision
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if a.GetState().Tracking.AvailableQuantity != 1 {
		t.Fatal("refresh lost remote increment")
	}
}
func TestLocalCompletionKeepsThreeCompletedAndFourRequired(t *testing.T) {
	a, fake := newLocalTestApp(t)
	initial := a.GetState().Tracking
	if initial.RequiredQuantity != 7 || len(initial.Requirements) != 2 ||
		initial.Requirements[0] != (ItemTrackingRequirement{Kind: "fir", RequiredQuantity: 2}) ||
		initial.Requirements[1] != (ItemTrackingRequirement{Kind: "regular", RequiredQuantity: 5}) {
		t.Fatalf("initial requirement: %+v", a.GetState().Tracking)
	}
	fake.quests.Quests[0].Completed = true
	fake.quests.Quests[0].Status = "completed"
	for i := 0; i < 2; i++ {
		fake.snapshot.Items[0].Sources[i].Completed = true
		fake.snapshot.Items[0].Sources[i].RemainingRequiredQuantity = 0
	}
	fake.snapshot.Items[0].RequiredQuantity = 4
	fake.snapshot.Items[0].SpentQuantity = 3
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := a.GetState()
	if state.Local.Error != "" || state.Tracking.RequiredQuantity != 4 {
		t.Fatalf("completion did not remove exactly one quest: %+v", state)
	}
	if len(state.Tracking.Requirements) != 2 ||
		state.Tracking.Requirements[0] != (ItemTrackingRequirement{Kind: "fir", RequiredQuantity: 1}) ||
		state.Tracking.Requirements[1] != (ItemTrackingRequirement{Kind: "regular", RequiredQuantity: 3}) {
		t.Fatalf("scan panel lost original aggregate FiR/regular requirements: %+v", state.Tracking)
	}
	state.Local.Error = "modified"
	if a.GetState().Local.Error == "modified" {
		t.Fatal("state aliases private local lists")
	}
}
func TestLocalModeHotkeyMarketItemWritesMarket(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.state.AddHotkey, a.state.AddHotkeyReady = "F2", true
	a.publishTrackedAddHotkey()
	target := a.addHotkeyTarget.Load()
	if target == nil {
		t.Fatal("Market shortcut was not available in Local Progress")
	}
	a.addTrackedItem(trackingItemUID, target)
	waitPendingForTest(t, a)
	state := a.GetState()
	if len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Storage != "market" {
		t.Fatalf("Regular shortcut did not route to Market: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
}
