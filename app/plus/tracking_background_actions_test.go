package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

func readyTypedStockHotkeysForTest(a *ItemWindowApp) {
	a.state.AddFIRHotkeyReady, a.state.AddRegularHotkeyReady = true, true
	a.state.RemoveFIRHotkeyReady, a.state.RemoveRegularHotkeyReady = true, true
	a.publishTrackedAddHotkey()
}

func TestBackgroundRefreshKeepsTypedButtonsHotkeysAndRapidReservations(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	readyTypedStockHotkeysForTest(a)
	initial := [4]*trackedAddHotkeyTarget{}
	for i := range initial {
		initial[i] = a.qualityHotkeyTargets[i].Load()
		if initial[i] == nil {
			t.Fatalf("initial action %d is unavailable", i)
		}
	}
	a.localAt = time.Time{}
	started, release := blockFirstPendingReadForTest(t, fake)
	holdTypedPendingWorkerForTest(a)
	done := make(chan struct{})
	go func() { a.refreshTracking(); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("background API refresh did not start")
	}
	state := a.GetState()
	if !state.Tracking.Loading || !state.Local.Loading || !state.Tracking.AddFIRAllowed || !state.Tracking.AddRegularAllowed || !state.Tracking.RemoveFIRAllowed || !state.Tracking.RemoveRegularAllowed {
		t.Fatalf("background Loading disabled the last valid typed actions: local=%+v tracking=%+v", state.Local, state.Tracking)
	}
	for i := range initial {
		if a.qualityHotkeyTargets[i].Load() != initial[i] {
			t.Fatalf("unrelated background refresh invalidated hotkey %d", i)
		}
	}
	const cycles = 10
	for i := 0; i < cycles; i++ {
		if i == 0 {
			a.reservePendingTypedMarketChange(trackingItemUID, initial[0], 1, true)
		} else {
			a.AddFIRTrackedItem(trackingItemUID)
		}
		a.AddRegularTrackedItem(trackingItemUID)
		state = a.GetState()
		if state.Tracking.AvailableQuantity != 4 || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed {
			t.Fatalf("loaded-snapshot reservation exceeded the full requirement: %+v", state.Tracking)
		}
		a.AddRegularTrackedItem(trackingItemUID)
		a.RemoveFIRTrackedItem(trackingItemUID)
		a.RemoveRegularTrackedItem(trackingItemUID)
	}
	if a.GetPendingAddCount() != cycles*4 {
		t.Fatalf("rapid actions during background refresh were lost or overbooked: %+v", a.GetState().PendingAdds)
	}
	release()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("background refresh did not return")
	}
	state = a.GetState()
	if state.PendingAdds.Count != cycles*4 || state.Tracking.AvailableQuantity != 2 || state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 1 || state.Tracking.Loading || state.Local.Loading || state.Local.Error != "" {
		t.Fatalf("fresh snapshot arrival discarded or reapplied pending reservations: pending=%+v local=%+v tracking=%+v", state.PendingAdds, state.Local, state.Tracking)
	}
	startFastTypedPendingWorkerForTest(a)
	state = waitAutomaticPendingDrainForTest(t, a)
	if state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 2 || state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 1 || fake.commits != cycles*4 {
		t.Fatalf("accepted background intents did not all verify exactly once: commits=%d pending=%+v tracking=%+v", fake.commits, state.PendingAdds, state.Tracking)
	}
}

func TestBackgroundRefreshTypedActionsStillRequireMatchingValidLoadedSnapshot(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ItemWindowApp)
	}{
		{"no loaded revision", func(a *ItemWindowApp) { a.localSnapshot.Revision = "" }},
		{"no loaded row", func(a *ItemWindowApp) { a.localSnapshot.Items = nil }},
		{"different displayed item", func(a *ItemWindowApp) { a.state.Item.UID = "different-item" }},
		{"different displayed item ID", func(a *ItemWindowApp) { a.state.Item.BSGID = "different-item-id" }},
		{"different character", func(a *ItemWindowApp) { a.state.CharacterUID = "different-character" }},
		{"different game mode", func(a *ItemWindowApp) { a.state.Mode = "pve" }},
		{"different options revision", func(a *ItemWindowApp) { a.localRevision-- }},
		{"stale local failure", func(a *ItemWindowApp) { a.state.Local.Error = "previous refresh failed" }},
		{"stale tracking failure", func(a *ItemWindowApp) { a.state.Tracking.Error = "previous tracker failed" }},
		{"invalid stock", func(a *ItemWindowApp) {
			a.localSnapshot.Items[0].AvailableQuantity = -1
			a.state.Tracking.AvailableQuantity = -1
		}},
		{"invalid API stock", func(a *ItemWindowApp) {
			a.localSnapshot.Items[0].MarketAvailableQuantity = localprogress.MaxQuantity + 1
		}},
		{"invalid typed stock", func(a *ItemWindowApp) { a.localSnapshot.Items[0].FIRQuantity = 3 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake := newTypedActionTestApp(t, 1, 1)
			a.state.Tracking.Loading, a.state.Local.Loading = true, true
			test.change(a)
			readyTypedStockHotkeysForTest(a)
			state := a.GetState()
			if state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed || state.Tracking.RemoveFIRAllowed || state.Tracking.RemoveRegularAllowed {
				t.Fatalf("Loading bypassed an invalid loaded-snapshot guard: %+v", state.Tracking)
			}
			for i := range a.qualityHotkeyTargets {
				if a.qualityHotkeyTargets[i].Load() != nil {
					t.Fatalf("invalid cached snapshot published hotkey %d", i)
				}
			}
			a.AddFIRTrackedItem(trackingItemUID)
			a.AddRegularTrackedItem(trackingItemUID)
			a.RemoveFIRTrackedItem(trackingItemUID)
			a.RemoveRegularTrackedItem(trackingItemUID)
			if a.GetPendingAddCount() != 0 {
				t.Fatal("invalid cached snapshot accepted a reservation")
			}
			if reads, writes := trackedHotkeyAPICalls(fake); reads != 0 || len(writes) != 0 {
				t.Fatalf("invalid cached snapshot touched API stock: reads=%d writes=%+v", reads, writes)
			}
		})
	}
}

func TestBackgroundRefreshRetainsPriorFailureUntilFreshSnapshotSucceeds(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	a.state.Local.Error, a.state.Tracking.Error = "prior local failure", ""
	a.localAt = time.Time{}
	started, release := blockFirstPendingReadForTest(t, fake)
	done := make(chan error, 1)
	go func() { done <- a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("failed-state refresh did not start")
	}
	state := a.GetState()
	if !state.Local.Loading || state.Local.Error != "prior local failure" || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed || state.Tracking.RemoveFIRAllowed || state.Tracking.RemoveRegularAllowed {
		t.Fatalf("starting a refresh cleared the failure guard too early: local=%+v tracking=%+v", state.Local, state.Tracking)
	}
	a.AddFIRTrackedItem(trackingItemUID)
	if a.GetPendingAddCount() != 0 {
		t.Fatal("prior refresh failure accepted a stock change before recovery")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state = a.GetState()
	if state.Local.Loading || state.Local.Error != "" || !state.Tracking.AddFIRAllowed || !state.Tracking.RemoveRegularAllowed {
		t.Fatalf("successful fresh snapshot did not release the failure guard: local=%+v tracking=%+v", state.Local, state.Tracking)
	}
}

func TestBackgroundRefreshAcceptedIntentUsesFreshWorkerStockBeforeWriting(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	a.localAt = time.Time{}
	started, release := blockFirstPendingReadForTest(t, fake)
	holdTypedPendingWorkerForTest(a)
	done := make(chan struct{})
	go func() { a.refreshTracking(); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("background refresh did not start")
	}
	a.AddFIRTrackedItem(trackingItemUID)
	if a.GetPendingAddCount() != 1 {
		t.Fatal("background refresh rejected the valid loaded-snapshot intent")
	}
	release()
	<-done
	setPendingACKExternalStock(fake, 3, "external-stock-after-background-refresh")
	startFastTypedPendingWorkerForTest(a)
	state := waitAutomaticPendingDrainForTest(t, a)
	_, writes := trackedHotkeyAPICalls(fake)
	if len(writes) != 1 || writes[0].Quantity != 4 || writes[0].Revision != "external-stock-after-background-refresh" || state.Tracking.AvailableQuantity != 4 || state.Tracking.FIRQuantity != 2 || state.Tracking.RegularQuantity != 1 || state.Tracking.UncategorizedQuantity != 1 {
		t.Fatalf("background reservation overwrote fresh API stock or fabricated its quality: pending=%+v tracking=%+v writes=%+v", state.PendingAdds, state.Tracking, writes)
	}
}
