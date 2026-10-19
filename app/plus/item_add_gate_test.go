package plus

import (
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"testing"
	"time"
)

func newFixedAddGateTestApp(t *testing.T, _ bool, fir bool) (*ItemWindowApp, *fakeCharacterProgress, string) {
	t.Helper()
	a, fake := newLocalTestApp(t)
	fake.snapshot.Items = []market.TrackedItem{{ItemID: trackingBSGID, RequiredQuantity: 1, RemainingQuantity: 1, Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-b", Amount: 1, RemainingRequiredQuantity: 1, FIR: fir}}}}
	fake.snapshot.Groups = nil
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	return a, fake, a.dir
}

func TestAddGateGenericItemFillsEitherRequirementKindAndStops(t *testing.T) {
	for _, fir := range []bool{false, true} {
		a, fake, _ := newFixedAddGateTestApp(t, true, fir)
		if !a.GetState().Tracking.AddAllowed {
			t.Fatal("open requirement blocked generic Add")
		}
		state := addMarketItemForTest(t, a, trackingItemUID)
		if len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddAllowed || state.Tracking.Error != "" {
			t.Fatalf("generic add missed the single source: %+v", state.Tracking)
		}
		wantKind := "regular"
		if fir {
			wantKind = "fir"
		}
		if available, required := requirementStock(t, state, wantKind); available != 1 || required != 1 {
			t.Fatalf("source kind or complete ratio lost: %+v", state.Tracking)
		}
		addMarketItemForTest(t, a, trackingItemUID)
		if len(fake.writes) != 1 {
			t.Fatal("complete source accepted another item")
		}
		assertNoMarketStockFilesForTest(t, a.dir)
	}
}

func TestAddGateGenericItemSharesMixedSourceCapacityOnce(t *testing.T) {
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	fake.snapshot.Items[0].Sources[1].FIR = true
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	for i := 0; i < 8; i++ {
		addMarketItemForTest(t, a, trackingItemUID)
	}
	state := a.GetState()
	if state.Tracking.AvailableQuantity != 8 || state.Tracking.AddAllowed || len(fake.writes) != 8 {
		t.Fatalf("mixed sources changed generic limit: %+v", state.Tracking)
	}
	for _, row := range state.Tracking.Requirements {
		if row.AvailableQuantity != row.RequiredQuantity {
			t.Fatalf("mixed row not complete: %+v", row)
		}
	}
	addMarketItemForTest(t, a, trackingItemUID)
	if len(fake.writes) != 8 {
		t.Fatal("one stock piece was counted twice")
	}
}

func TestAddGateGenericAnyUsesStockFromEveryCandidate(t *testing.T) {
	for _, fir := range []bool{false, true} {
		a, fake := newLocalTestApp(t)
		const other = "another-candidate"
		setMarketGroupRowsForTest(fake, nil, marketQuestGroupForTest("quest-b", "any", 2, fir, trackingBSGID, other))
		setFakeMarketGroupProgress(&fake.snapshot, 1)
		fake.groupProgressByCommit = [][]int64{{2}}
		for i := range fake.snapshot.Items {
			if fake.snapshot.Items[i].ItemID == other {
				fake.snapshot.Items[i].AvailableQuantity = 1
			}
		}
		a.localAt = time.Time{}
		a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
		state := addMarketItemForTest(t, a, trackingItemUID)
		if state.Tracking.AddAllowed || len(fake.writes) != 1 || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 2 {
			t.Fatalf("Any shared stock was duplicated: %+v", state.Tracking)
		}
		addMarketItemForTest(t, a, trackingItemUID)
		if len(fake.writes) != 1 {
			t.Fatal("full Any accepted another generic item")
		}
	}
}

func TestAddGateRechecksFreshMarketStockBeforePATCHAndConflictRetry(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh-read", true: "conflict-retry"}[conflict], func(t *testing.T) {
			a, fake, _ := newFixedAddGateTestApp(t, true, false)
			loads := fake.loads
			if conflict {
				fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
					fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 1, 0
					fake.snapshot.Revision = "external-full"
					return market.ProgressWriteResult{}, market.ErrProgressConflict
				}
			} else {
				fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
					if count >= loads+1 {
						// The final fresh GET supersedes the earlier open UI state.
						fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 1, 0
					}
					return cloneTrackedSnapshot(fake.snapshot), nil
				}
			}
			state := addMarketTrackedItemForTest(a, trackingItemUID)
			wantWrites := 0
			if conflict {
				wantWrites = 1
			}
			if len(fake.writes) != wantWrites || state.Tracking.AvailableQuantity != 1 || state.Tracking.RemainingQuantity != 0 ||
				state.Tracking.AddAllowed || state.Tracking.Adding || state.Tracking.Error != "" {
				t.Fatalf("stale eligibility reached an extra PATCH: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
		})
	}
}
