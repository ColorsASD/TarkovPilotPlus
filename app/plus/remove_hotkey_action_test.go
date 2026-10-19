package plus

import "testing"

func newRemoveHotkeyActionTestApp(t *testing.T, noRequirements bool) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newPendingRemoveTestApp(t, 1, noRequirements)
	a.state.AddHotkey, a.state.RemoveHotkey = "F1", "F2"
	a.state.AddHotkeyReady, a.state.RemoveHotkeyReady = true, true
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	a.publishTrackedAddHotkey()
	return a, fake
}

func triggerF2ForTest(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	h := &trackedAddHotkey{trigger: a.requestTrackedRemoveHotkey}
	h.SetKey("F2")
	var keys trackedHotkeyKeys
	if !h.keyEvent(&keys, 0x71, true, false, false) || !h.keyEvent(&keys, 0x71, false, false, false) {
		t.Fatal("F2 did not capture one physical press/release")
	}
}

func TestRemoveHotkeyNativeF2SubtractsOneAndWritesAbsoluteZero(t *testing.T) {
	a, fake := newRemoveHotkeyActionTestApp(t, false)
	if target := a.removeHotkeyTarget.Load(); target == nil || !target.Remove {
		t.Fatal("F2 did not publish a removal target")
	}
	triggerF2ForTest(t, a)
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	state := a.GetState()
	_, writes := trackedHotkeyAPICalls(fake)
	if len(writes) != 1 || writes[0].Quantity != 0 || writes[0].Item != trackingBSGID || state.Tracking.AvailableQuantity != 0 || state.Tracking.RemoveAllowed || state.PendingAdds.Count != 0 {
		t.Fatalf("F2 did not confirm absolute zero: state=%+v writes=%+v", state, writes)
	}
}

func TestRemoveHotkeyEmittedNewIdentityInvalidatesQueuedRemoval(t *testing.T) {
	a, fake := newRemoveHotkeyActionTestApp(t, false)
	triggerF2ForTest(t, a)
	a.mu.Lock()
	a.state.Item.UID = "new-visible-item"
	a.state.Tracking.ItemUID = "new-visible-item"
	a.mu.Unlock()
	a.emit()
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	reads, writes := trackedHotkeyAPICalls(fake)
	if reads != 0 || len(writes) != 0 || a.GetPendingAddCount() != 0 {
		t.Fatalf("old removal followed the emitted new item: reads=%d writes=%+v", reads, writes)
	}
}

func TestRemoveHotkeySharesOccupiedActionQueueWithAdd(t *testing.T) {
	a, fake := newRemoveHotkeyActionTestApp(t, false)
	triggerF2ForTest(t, a)
	a.requestTrackedAddHotkey()
	triggerF2ForTest(t, a)
	if len(a.addHotkeyRequests) != 1 {
		t.Fatal("occupied shared action queue replayed another physical press")
	}
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	_, writes := trackedHotkeyAPICalls(fake)
	if len(writes) != 1 || writes[0].Quantity != 0 {
		t.Fatalf("shared action queue added instead of removing or repeated: %+v", writes)
	}
}

func TestRemoveHotkeyWorksWithoutAnActiveTrackedRequirement(t *testing.T) {
	a, fake := newRemoveHotkeyActionTestApp(t, true)
	a.state.Tracking.Tracked = false
	a.publishTrackedAddHotkey()
	if a.removeHotkeyTarget.Load() == nil || a.addHotkeyTarget.Load() != nil {
		t.Fatal("no-active-requirement stock did not retain only Remove")
	}
	triggerF2ForTest(t, a)
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	_, writes := trackedHotkeyAPICalls(fake)
	if len(writes) != 1 || writes[0].Quantity != 0 || a.GetState().Tracking.AvailableQuantity != 0 {
		t.Fatalf("requirement-free F2 failed: %+v", writes)
	}
}
