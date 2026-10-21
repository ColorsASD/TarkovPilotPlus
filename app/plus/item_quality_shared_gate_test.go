package plus

import (
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestTypedSharedCoveredAPICounterDoesNotDisableLocallyMissingQuality(t *testing.T) {
	for _, fir := range []bool{false, true} {
		t.Run(map[bool]string{false: "Regular", true: "FiR"}[fir], func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			a.qualityTracking = true
			a.state.ItemOptions = normalizeItemOptions(a.state.ItemOptions)
			fake.snapshot.Items = []market.TrackedItem{
				{ItemID: trackingBSGID, Name: "Selected candidate"},
				{ItemID: "other-candidate", Name: "Other candidate", AvailableQuantity: 3},
			}
			fake.snapshot.Groups = []market.TrackedGroup{{
				QuestID: "regular-goal", QuestName: "Regular goal", RequirementID: "shared-goal", Name: "Shared items", FIR: fir,
				Amount: 3, RequiredQuantity: 3, AvailableQuantity: 3, RemainingQuantity: 0,
				ItemIDs: []string{trackingBSGID, "other-candidate"},
			}}
			a.tracker = &typedActionTrackingFixture{fake}
			remote := cloneTrackedSnapshot(fake.snapshot)
			merged := marketAnyProgressSnapshotForTest(t, remote, typedActionQuests(remote).Quests...)
			a.mu.Lock()
			a.applyMergedMarketSnapshotLocked(*a.state.Item, remote, merged, "")
			a.mu.Unlock()
			state := a.GetState()
			if state.Local.Error != "" || state.Tracking.RequiredQuantity != 3 || state.Tracking.RemainingQuantity != 3 ||
				state.Tracking.AvailableQuantity != 0 || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 0 ||
				state.Tracking.AnyGroups[0].RequiredQuantity != 3 || state.Tracking.AnyGroups[0].FIR != fir {
				t.Fatalf("covered API counter hid the local typed deficit: local=%+v tracking=%+v", state.Local, state.Tracking)
			}
			if !state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed == fir {
				t.Fatalf("local typed deficit did not enable the useful qualities: %+v", state.Tracking)
			}
			a.state.AddFIRHotkeyReady, a.state.AddRegularHotkeyReady = true, true
			a.publishTrackedAddHotkey()
			actionIndex := qualityActionIndex(false, fir)
			if target := a.qualityHotkeyTargets[actionIndex].Load(); target == nil || !target.Typed || target.FIR != fir {
				t.Fatalf("API-covered Shared deficit did not publish its native Add target: %+v", target)
			}
			if fir {
				a.AddFIRTrackedItem(trackingItemUID)
			} else {
				a.AddRegularTrackedItem(trackingItemUID)
			}
			state = waitPendingForTest(t, a)
			if state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 1 ||
				state.Tracking.MarketAvailableQuantity != 1 || len(state.Tracking.AnyGroups) != 1 ||
				state.Tracking.AnyGroups[0].AvailableQuantity != 1 || state.Tracking.AnyGroups[0].RequiredQuantity != 3 {
				t.Fatalf("typed Shared addition did not use its local quality after saving: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
			}
			if _, writes := trackedHotkeyAPICalls(fake); len(writes) != 1 || writes[0].Quantity != 1 {
				t.Fatalf("typed Shared addition was canceled or replayed: %+v", writes)
			}
			if fake.snapshot.Groups[0].AvailableQuantity != 3 {
				t.Fatal("local quality accounting modified the fixture API counter")
			}
		})
	}
}
