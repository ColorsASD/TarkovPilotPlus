//go:build windows

package plus

import (
	"context"
	"testing"
	"time"
)

func TestTrackedHotkeyStatusTracksConfigurationWithoutInstallingHook(t *testing.T) {
	// Exercise configuration/status state only. This never installs a hook or
	// captures physical input on the machine running the test suite.
	h := &trackedAddHotkey{statuses: make(chan struct{}, 1)}
	if h.key.Load() != 0 {
		t.Fatal("engine did not begin with its reservation disabled")
	}
	h.SetKey("F1")
	if h.currentStatus().ready {
		t.Fatal("shortcut reported ready before installation")
	}
	h.setInstallation(true, "")
	if state := h.currentStatus(); !state.ready || state.error != "" {
		t.Fatalf("active installed shortcut has status %+v", state)
	}
	h.SetKey("off")
	if state := h.currentStatus(); state.ready || state.error != "" {
		t.Fatalf("disabled shortcut has status %+v", state)
	}
	h.SetKey("F12")
	if !h.currentStatus().ready || h.key.Load() != 0x7b {
		t.Fatal("reconfigured shortcut did not become ready")
	}
	h.setInstallation(false, "controlled installation failure")
	h.SetKey("F2")
	if state := h.currentStatus(); state.ready || state.error != "controlled installation failure" {
		t.Fatalf("reconfiguration hid the installation failure: %+v", state)
	}
	h.SetKey("off")
	if state := h.currentStatus(); state.ready || state.error != "" {
		t.Fatalf("disabled shortcut kept an irrelevant installation failure: %+v", state)
	}
	if len(h.statuses) != 1 {
		t.Fatal("rapid configuration updates accumulated queued status messages")
	}
}

func TestTrackedHotkeyCancelledStartupDoesNotInstallOrReserveKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The already-cancelled context exits before any native hook is installed.
	h := newTrackedAddHotkey(ctx, func() {
		t.Error("cancelled shortcut invoked its action")
	}, nil)
	if h.key.Load() != 0 {
		t.Fatal("cancelled startup briefly reserved the default key")
	}
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled shortcut did not finish its thread")
	}
	h.Close()
	h.Close()
	if h.currentStatus().ready {
		t.Fatal("closed shortcut reported ready")
	}
}

func TestTrackedHotkeyStatusContinuesAfterInstallationFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications := make(chan trackedHotkeyStatus, 8)
	h := &trackedAddHotkey{
		ctx: ctx, statuses: make(chan struct{}, 1), done: make(chan struct{}),
		status: func(ready bool, message string) {
			notifications <- trackedHotkeyStatus{ready: ready, error: message}
		},
	}
	h.SetKey("F1")
	h.setInstallation(false, "controlled installation failure")
	// A failed native installer has already finished; configuration changes
	// still need to clear/reveal its failure without creating another hook.
	close(h.done)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		h.runStatuses()
	}()
	await := func(expected string) {
		t.Helper()
		select {
		case value := <-notifications:
			if value.ready || value.error != expected {
				t.Fatalf("unexpected status %+v, want error %q", value, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("status worker stopped after an installation failure")
		}
	}
	await("controlled installation failure")
	h.SetKey("off")
	await("")
	h.SetKey("F2")
	await("controlled installation failure")
	cancel()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled status worker did not stop")
	}
}

func TestTrackedHotkeyDualBindingsRouteIndependentPhysicalPresses(t *testing.T) {
	regular, fir := 0, 0
	h := &trackedAddHotkey{statuses: make(chan struct{}, 1),
		trigger: func() { regular++ }, triggerFIR: func() { fir++ }}
	h.SetKeys("F1", "F2")
	h.setInstallation(true, "")
	if !h.currentStatus().ready {
		t.Fatal("the installed dual shortcut was not ready")
	}
	var keys trackedHotkeyKeys
	if !h.keyEvent(&keys, 0x70, true, false, false) || !h.keyEvent(&keys, 0x71, true, false, false) {
		t.Fatal("the two initial physical presses were not captured")
	}
	for range 50 {
		h.keyEvent(&keys, 0x70, true, false, false)
		h.keyEvent(&keys, 0x71, true, false, false)
	}
	// Synthetic releases do not release either physical hold.
	h.keyEvent(&keys, 0x71, false, true, false)
	h.keyEvent(&keys, 0x71, true, false, false)
	if regular != 1 || fir != 1 {
		t.Fatalf("holding the keys repeated or changed kind: regular=%d fir=%d", regular, fir)
	}
	if !h.keyEvent(&keys, 0x70, false, false, false) || !h.keyEvent(&keys, 0x71, false, false, false) {
		t.Fatal("the captured releases were not consumed")
	}
	h.keyEvent(&keys, 0x71, true, false, false)
	if regular != 1 || fir != 2 {
		t.Fatal("a new F2 press was not routed to FiR")
	}
}

func TestTrackedHotkeyDualBindingsConfigurationChangeKeepsReleaseAndModifiers(t *testing.T) {
	regular, fir := 0, 0
	h := &trackedAddHotkey{statuses: make(chan struct{}, 1),
		trigger: func() { regular++ }, triggerFIR: func() { fir++ }}
	var keys trackedHotkeyKeys
	h.SetKeys("F1", "F2")
	h.keyEvent(&keys, 0x71, true, false, false)
	h.SetKey("F8")
	if !h.keyEvent(&keys, 0x71, true, false, false) || !h.keyEvent(&keys, 0x71, false, false, false) {
		t.Fatal("switching mode lost the already captured F2 pair")
	}
	if h.keyEvent(&keys, 0x71, true, false, false) {
		t.Fatal("the disabled second binding stayed reserved")
	}
	h.keyEvent(&keys, 0x71, false, false, false)
	h.SetKeys("F1", "F2")
	h.keyEvent(&keys, 0xa2, true, false, false)
	if h.keyEvent(&keys, 0x70, true, false, false) || h.keyEvent(&keys, 0x71, true, false, false) {
		t.Fatal("dual bindings captured modifier chords")
	}
	h.keyEvent(&keys, 0x70, false, false, false)
	h.keyEvent(&keys, 0x71, false, false, false)
	h.keyEvent(&keys, 0xa2, false, false, false)
	h.SetKey("off")
	if h.keyEvent(&keys, 0x70, true, false, false) || h.keyEvent(&keys, 0x71, true, false, false) || regular != 0 || fir != 1 {
		t.Fatal("disabled dual shortcuts still captured or triggered a key")
	}
}

func TestTrackedHotkeyDefaultBindingsAreF2RegularAndF1FIR(t *testing.T) {
	regular, fir := 0, 0
	h := &trackedAddHotkey{statuses: make(chan struct{}, 1), trigger: func() { regular++ }, triggerFIR: func() { fir++ }}
	h.SetKeys("", "")
	h.setInstallation(true, "")
	if h.key.Load() != 0x71 || !h.currentStatus().ready {
		t.Fatal("empty preferences did not enable the default Regular F2 binding")
	}
	var keys trackedHotkeyKeys
	h.keyEvent(&keys, 0x70, true, false, false)
	h.keyEvent(&keys, 0x71, true, false, false)
	if regular != 1 || fir != 1 {
		t.Fatalf("default kinds were reversed: regular=%d fir=%d", regular, fir)
	}
	h.keyEvent(&keys, 0x70, false, false, false)
	h.keyEvent(&keys, 0x71, false, false, false)
	h.SetKeys("off", "F1")
	if !h.currentStatus().ready || h.keyEvent(&keys, 0x71, true, false, false) || !h.keyEvent(&keys, 0x70, true, false, false) || regular != 1 || fir != 2 {
		t.Fatal("disabling only Regular changed the independent FiR binding")
	}
}

func TestTrackedHotkeySameBaseKeyRoutesExactChordsWithoutHoldRedirection(t *testing.T) {
	regular, fir := 0, 0
	h := &trackedAddHotkey{statuses: make(chan struct{}, 1), trigger: func() { regular++ }, triggerFIR: func() { fir++ }}
	h.SetKeys("ctrl+a", "alt+a")
	h.setInstallation(true, "")
	var keys trackedHotkeyKeys
	if h.keyEvent(&keys, 'A', true, false, false) || h.keyEvent(&keys, 'A', false, false, false) {
		t.Fatal("an unconfigured plain letter was consumed")
	}
	h.keyEvent(&keys, 0xa2, true, false, false)
	if !h.keyEvent(&keys, 'A', true, false, false) || regular != 1 || fir != 0 {
		t.Fatal("Ctrl+A did not route to Regular")
	}
	h.keyEvent(&keys, 0xa2, false, false, false)
	h.keyEvent(&keys, 0xa4, true, false, true)
	for range 50 {
		if !h.keyEvent(&keys, 'A', true, false, true) {
			t.Fatal("a modifier transition lost the captured press")
		}
	}
	if regular != 1 || fir != 0 {
		t.Fatal("changing modifiers during a hold redirected or repeated its action")
	}
	if !h.keyEvent(&keys, 'A', false, false, true) || !h.keyEvent(&keys, 'A', true, false, true) || regular != 1 || fir != 1 {
		t.Fatal("the next physical Alt+A did not route independently to FiR")
	}
	h.SetKeys("off", "off")
	if !h.keyEvent(&keys, 'A', false, false, true) {
		t.Fatal("disabling the chord leaked its captured key-up")
	}
	if h.keyEvent(&keys, 'A', true, false, true) || h.currentStatus().ready {
		t.Fatal("disabled shortcuts still reserved a chord")
	}
}

func TestTrackedHotkeyDuplicateChordsFailClosedAndInjectedModifiersDoNotTrigger(t *testing.T) {
	regular, fir := 0, 0
	h := &trackedAddHotkey{statuses: make(chan struct{}, 1), trigger: func() { regular++ }, triggerFIR: func() { fir++ }}
	h.SetKeys("shift+ctrl+a", "Ctrl+Shift+A")
	h.setInstallation(true, "")
	if h.currentStatus().ready || h.key.Load() != 0 {
		t.Fatal("exact duplicate kinds were guessed instead of disabled")
	}
	h.SetKeys("Ctrl+A", "off")
	var keys trackedHotkeyKeys
	h.keyEvent(&keys, 0xa2, true, true, false)
	h.keyEvent(&keys, 'A', true, false, false)
	if regular != 0 || fir != 0 {
		t.Fatal("an injected modifier generated a physical stock action")
	}
	h.keyEvent(&keys, 'A', false, false, false)
	h.keyEvent(&keys, 0xa2, false, true, false)
	h.keyEvent(&keys, 0xa2, true, false, false)
	h.keyEvent(&keys, 'A', true, false, false)
	if regular != 1 || fir != 0 {
		t.Fatal("a genuine physical Ctrl+A was not routed after injected state cleared")
	}
}
