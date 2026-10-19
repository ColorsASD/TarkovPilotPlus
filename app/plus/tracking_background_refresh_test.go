package plus

import (
	"reflect"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestBackgroundTrackingRefreshPreservesLoadedPanelAndDisablesActions(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 2, false)
	a.localAt = time.Time{}
	before := a.GetState().Tracking
	started, release := blockFirstPendingReadForTest(t, fake)
	done := make(chan struct{})
	go func() { a.refreshTracking(); close(done) }()
	select {
	case <-started:
	case <-done:
		t.Fatal("refresh did not request a new snapshot")
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	state := a.GetState()
	if state.Tracking == nil || !state.Tracking.Tracked || !state.Tracking.Loading || state.Tracking.ItemID != trackingBSGID || state.Tracking.AvailableQuantity != 2 || state.Tracking.RequiredQuantity != before.RequiredQuantity || !reflect.DeepEqual(state.Tracking.Requirements, before.Requirements) || state.Local == nil || !state.Local.Loading || state.Tracking.RemoveAllowed {
		t.Fatalf("background refresh erased the loaded panel or retained unsafe actions: tracking=%+v local=%+v", state.Tracking, state.Local)
	}
	a.AddTrackedItem(trackingItemUID)
	a.RemoveTrackedItem(trackingItemUID)
	if a.GetPendingAddCount() != 0 {
		t.Fatal("loading snapshot accepted a stock change")
	}
	release()
	<-done
	state = a.GetState()
	if state.Tracking.Loading || !state.Tracking.Tracked || state.Tracking.AvailableQuantity != 2 || state.Local.Loading || state.Local.Error != "" || !state.Tracking.RemoveAllowed || len(fake.writes) != 0 {
		t.Fatalf("background refresh did not finish atomically: tracking=%+v local=%+v", state.Tracking, state.Local)
	}
}

func TestBackgroundTrackingRefreshFailureKeepsLastKnownPanel(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 2, false)
	a.localAt = time.Time{}
	before := a.GetState().Tracking
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		return market.TrackedSnapshot{}, market.ErrProgressUnavailable
	}
	a.refreshTracking()
	state := a.GetState()
	if state.Tracking == nil || !state.Tracking.Tracked || state.Tracking.Loading || state.Tracking.AvailableQuantity != 2 || state.Tracking.Error == "" || !reflect.DeepEqual(state.Tracking.Requirements, before.Requirements) || state.Local == nil || state.Local.Error == "" || state.Tracking.RemoveAllowed {
		t.Fatalf("failed refresh erased prior values or hid its failure: tracking=%+v local=%+v", state.Tracking, state.Local)
	}
}

func TestBackgroundTrackingRefreshDoesNotReuseDifferentItemPanel(t *testing.T) {
	a, fake := newPendingRemoveTestApp(t, 2, false)
	a.localAt = time.Time{}
	a.state.Item = &market.Item{UID: "new-item-uid", BSGID: "new-item-id", Name: "Different item"}
	started, release := blockFirstPendingReadForTest(t, fake)
	done := make(chan struct{})
	go func() { a.refreshTracking(); close(done) }()
	select {
	case <-started:
	case <-done:
		t.Fatal("refresh did not request the other item snapshot")
	case <-time.After(3 * time.Second):
		t.Fatal("other item refresh did not start")
	}
	state := a.GetState()
	if state.Tracking == nil || !state.Tracking.Loading || state.Tracking.Tracked || state.Tracking.AvailableQuantity != 0 || len(state.Tracking.Requirements) != 0 || state.Tracking.ItemID != "new-item-id" {
		t.Fatalf("another item inherited the old stock panel: %+v", state.Tracking)
	}
	release()
	<-done
}

func TestBackgroundTrackingOptionsKeepOnlySameProfilePanel(t *testing.T) {
	a, _ := newPendingRemoveTestApp(t, 2, false)
	opts := a.state.ItemOptions
	opts.Automatic = !opts.Automatic
	state := a.SetOptions(opts)
	if state.Error != "" || state.Tracking == nil || !state.Tracking.Tracked || !state.Tracking.Loading || state.Tracking.AvailableQuantity != 2 || state.Local == nil || !state.Local.Loading {
		t.Fatalf("scanner-only setting erased same-profile progress: tracking=%+v local=%+v", state.Tracking, state.Local)
	}
	opts = state.ItemOptions
	opts.CharacterUID = "different-character"
	state = a.SetOptions(opts)
	if state.Tracking != nil || len(a.localSnapshot.Items) != 0 || len(a.confirmedProgress.Items) != 0 {
		t.Fatalf("new profile retained the previous profile's inventory: %+v", state.Tracking)
	}
}
