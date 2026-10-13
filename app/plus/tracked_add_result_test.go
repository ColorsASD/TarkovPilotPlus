package plus

import (
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestTrackedAddRequiresObservedIncreaseBeforeSuccessOrQualityAnnotation(t *testing.T) {
	for _, tc := range []struct {
		name, revision, status string
		changed, blocked       bool
		observed               int64
	}{
		{name: "no-op unchanged", revision: "revision-1", status: "reported no change", blocked: true},
		{name: "no-op already target", revision: "external-target", observed: 1, status: "reported no change"},
		{name: "changed ack unchanged readback", changed: true, revision: "revision-1", status: "did not confirm the increase", blocked: true},
		{name: "changed ack other quantity", changed: true, revision: "external-other", observed: 2, status: "did not confirm the increase", blocked: true},
		{name: "changed ack outdated target", changed: true, revision: "revision-1", observed: 1, status: "readback was outdated", blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				fake.mu.Lock()
				fake.snapshot.Items[0].AvailableQuantity = tc.observed
				fake.snapshot.Revision = tc.revision
				fake.mu.Unlock()
				return market.ProgressWriteResult{OK: true, Changed: tc.changed, Revision: "ack-revision", NotificationDelivered: true}, nil
			}
			state := addMarketTrackedItemForTest(a, trackingItemUID, "fir")
			if len(fake.writes) != 1 || state.Tracking.AvailableQuantity != tc.observed || state.Tracking.Adding ||
				strings.Contains(state.Tracking.Status, "Added 1 item") || !strings.Contains(state.Tracking.Status, tc.status) ||
				!strings.Contains(state.Tracking.Status, "Requested stock: 1") || (state.Tracking.Error != "") != tc.blocked {
				t.Fatalf("response was mistaken for an observed own add: writes=%+v tracking=%+v", fake.writes, state.Tracking)
			}
			local, err := a.localStore.Load("character-a", "PVP")
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range local.Items {
				if item.MarketRegularQuantity != 0 || item.MarketFIRQuantity != 0 || item.LocalCollectedQuantity != 0 {
					t.Fatalf("unconfirmed/no-op add invented typed stock: %+v", item)
				}
			}
			if tc.blocked {
				addMarketTrackedItemForTest(a, trackingItemUID, "fir")
				if len(fake.writes) != 1 {
					t.Fatal("unverified add allowed another write without an explicit refresh")
				}
			}
		})
	}
}

func TestTrackedAddConfirmedUnavailableReadbackBlocksUntilRefresh(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	beforeReads := fake.loads
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read == beforeReads+2 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	state := addMarketTrackedItemForTest(a, trackingItemUID, "fir")
	if state.Tracking.AvailableQuantity != 1 || state.Tracking.Error == "" || !strings.Contains(state.Tracking.Status, "confirmed stock 1") || strings.Contains(state.Tracking.Status, "Added 1 item") {
		t.Fatalf("failed GET appeared to verify the server-confirmed target: %+v", state.Tracking)
	}
	addMarketTrackedItemForTest(a, trackingItemUID, "fir")
	if len(fake.writes) != 1 {
		t.Fatal("verification-pending response allowed an immediate second write")
	}
	fake.loadHook = nil
	a.refreshTracking()
	if state = a.GetState(); state.Tracking.Error != "" || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("explicit refresh did not reconcile the confirmed target: %+v", state.Tracking)
	}
	addMarketTrackedItemForTest(a, trackingItemUID, "fir")
	if len(fake.writes) != 2 || fake.writes[1].Quantity != 2 {
		t.Fatal("new explicit add after refresh did not use the observed current quantity")
	}
}

func TestHandoverNoOpOrMismatchedReadbackKeepsPreparedAndQuality(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "no-op"
		if changed {
			name = "changed ack but unchanged stock"
		}
		t.Run(name, func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			addAndSyncCachedTest(t, a, trackingItemUID, "fir")
			addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				return market.ProgressWriteResult{OK: true, Changed: changed, Revision: "ack-only-new-revision", NotificationDelivered: true}, nil
			}
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
			state := a.confirmHandoverLegacy()
			requireHandoverPending(t, a, state)
			row := handoverStockForTest(t, a, trackingBSGID)
			if len(fake.writes) != 4 || fake.snapshot.Items[0].AvailableQuantity != 3 || row.MarketRegularQuantity != 2 || row.MarketFIRQuantity != 1 ||
				row.LocalCollectedQuantity != 1 || row.LocalUsedQuantity != 1 || a.handover.Items[0].Stage != "prepared" ||
				!strings.Contains(state.Handover.Error, "Requested stock: 2; current stock: 3") {
				t.Fatalf("unchanged Market stock settled annotation or lost the local receipt: row=%+v handover=%+v writes=%+v", row, state.Handover, fake.writes)
			}
			if !changed && !strings.Contains(state.Handover.Error, "reported no change") {
				t.Fatal("documented no-op was not explained to the user")
			}
			// Csak új, kifejezett Resume kezdeményezhet új kérést. A korábbi
			// local-first nyugta ettől nem fogyaszthat még egy saját darabot.
			fake.writeHook = nil
			requireHandoverComplete(t, a.confirmHandoverLegacy())
			row = handoverStockForTest(t, a, trackingBSGID)
			if len(fake.writes) != 5 || row.LocalUsedQuantity != 1 || row.MarketRegularQuantity != 1 || row.MarketFIRQuantity != 1 {
				t.Fatalf("explicit resume repeated local use or missed verified Market removal: %+v writes=%+v", row, fake.writes)
			}
		})
	}
}

func TestHandoverChangedAcknowledgmentNeedsFreshUnambiguousTargetReadback(t *testing.T) {
	for _, tc := range []string{"unavailable", "old revision", "wrong character", "duplicate row"} {
		t.Run(tc, func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			addAndSyncCachedTest(t, a, trackingItemUID, "fir")
			initialReads := fake.loads
			previousRevision := fake.snapshot.Revision
			fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
				fake.mu.Lock()
				observed := cloneTrackedSnapshot(fake.snapshot)
				fake.mu.Unlock()
				if read != initialReads+3 {
					return observed, nil
				}
				switch tc {
				case "unavailable":
					return market.TrackedSnapshot{}, market.ErrProgressUnavailable
				case "old revision":
					observed.Revision = previousRevision
				case "wrong character":
					observed.CharUID = "another-character"
				case "duplicate row":
					observed.Items = append(observed.Items, observed.Items[0])
				}
				return observed, nil
			}
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"))
			state := a.confirmHandoverLegacy()
			requireHandoverPending(t, a, state)
			if len(fake.writes) != 4 || fake.snapshot.Items[0].AvailableQuantity != 2 || a.handover.Items[0].Stage != "prepared" ||
				!strings.Contains(state.Handover.Error, "Requested stock: 2") {
				t.Fatalf("acknowledgment settled despite an unusable readback: writes=%+v handover=%+v", fake.writes, state.Handover)
			}
			fake.loadHook = nil
			requireHandoverComplete(t, a.confirmHandoverLegacy())
			row := handoverStockForTest(t, a, trackingBSGID)
			if len(fake.writes) != 4 || row.MarketRegularQuantity != 1 || row.MarketFIRQuantity != 1 {
				t.Fatalf("fresh explicit verification resent the applied write or lost confirmed remaining quality: writes=%+v row=%+v", fake.writes, row)
			}
		})
	}
}
