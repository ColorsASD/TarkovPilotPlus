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
