package plus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestPendingExitDefaultsToKeepingPendingSaves(t *testing.T) {
	var calls []bool
	guard := PendingExitGuard{dialog: func(_ context.Context, options wailsRuntime.MessageDialogOptions) (string, error) {
		if options.DefaultButton != "No" || options.CancelButton != "No" || !strings.Contains(options.Message, "3 items") || !strings.Contains(options.Message, "discard") {
			t.Fatalf("unsafe quit prompt: %+v", options)
		}
		return "No", nil
	}}
	prevent := guard.BeforeClose(context.Background(), func(discard bool) (int, bool, error) { calls = append(calls, discard); return 3, false, nil })
	if !prevent || guard.Approved() || len(calls) != 1 || calls[0] {
		t.Fatalf("cancelled quit approved an exit: prevent=%t calls=%v", prevent, calls)
	}
}

func TestPendingExitExplicitConfirmationAuthorizesDiscardOnce(t *testing.T) {
	var calls []bool
	guard := PendingExitGuard{dialog: func(context.Context, wailsRuntime.MessageDialogOptions) (string, error) { return "Yes", nil }}
	prepare := func(discard bool) (int, bool, error) { calls = append(calls, discard); return 2, discard, nil }
	if guard.BeforeClose(context.Background(), prepare) || !guard.Approved() || len(calls) != 2 || calls[0] || !calls[1] {
		t.Fatalf("explicit exit was not authorized: %v", calls)
	}
	if guard.BeforeClose(context.Background(), func(bool) (int, bool, error) { t.Fatal("approved close was checked twice"); return 0, false, nil }) {
		t.Fatal("approved close was blocked")
	}
}

func TestPendingExitWithNoPendingDoesNotOpenDialog(t *testing.T) {
	guard := PendingExitGuard{dialog: func(context.Context, wailsRuntime.MessageDialogOptions) (string, error) {
		t.Fatal("empty queue prompted")
		return "", nil
	}}
	if guard.BeforeClose(context.Background(), func(discard bool) (int, bool, error) {
		if discard {
			t.Fatal("empty queue requested discard")
		}
		return 0, true, nil
	}) || !guard.Approved() {
		t.Fatal("empty queue could not exit")
	}
}

func TestPendingExitStatusFailureAndDialogFailureKeepAppOpen(t *testing.T) {
	for _, statusFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "dialog-failure", true: "status-failure"}[statusFailure], func(t *testing.T) {
			guard := PendingExitGuard{dialog: func(_ context.Context, options wailsRuntime.MessageDialogOptions) (string, error) {
				if statusFailure && options.Type != wailsRuntime.WarningDialog {
					t.Fatal("unknown pending state was offered discard")
				}
				return "", errors.New("controlled failure")
			}}
			if !guard.BeforeClose(context.Background(), func(bool) (int, bool, error) {
				if statusFailure {
					return 0, false, errors.New("pipe unavailable")
				}
				return 1, false, nil
			}) || guard.Approved() {
				t.Fatal("failed status or dialog allowed exit")
			}
		})
	}
}

func TestPendingExitPreparationBlocksNewAddsAndAbandonedPreparationReleasesThem(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	started, unblock := blockFirstPendingReadForTest(t, fake)
	count, approved, err := a.preparePendingQuit(false)
	if err != nil || count != 0 || !approved {
		t.Fatalf("empty queue could not prepare exit: %d %t %v", count, approved, err)
	}
	a.AddTrackedItem(trackingItemUID)
	loads, writes := trackedHotkeyAPICalls(fake)
	if a.GetPendingAddCount() != 0 || loads != 0 || len(writes) != 0 {
		t.Fatal("Add arrived between the empty-queue check and process shutdown")
	}
	a.cancelPendingQuitPreparation()
	a.AddTrackedItem(trackingItemUID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("abandoned exit preparation left Add disabled")
	}
	count, approved, err = a.preparePendingQuit(false)
	if err != nil || count != 1 || approved {
		t.Fatalf("pending queue exited without confirmation: %d %t %v", count, approved, err)
	}
	count, approved, err = a.preparePendingQuit(true)
	if err != nil || count != 1 || !approved {
		t.Fatalf("explicit discard could not prepare exit: %d %t %v", count, approved, err)
	}
	a.AddTrackedItem(trackingItemUID)
	if a.GetPendingAddCount() != 1 {
		t.Fatal("Add entered after pending changes were approved for discard")
	}
	unblock()
	waitPendingForTest(t, a)
}
