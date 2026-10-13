//go:build windows

package plus

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

var signalTestSequence atomic.Int64

func uniqueSignalTestPID() int {
	return os.Getpid() + 1_000_000 + int(signalTestSequence.Add(1))
}

func TestHandoverNamedEventRetainsEarlyRequestAndResetsOnce(t *testing.T) {
	pid := uniqueSignalTestPID()
	owner, err := createHandoverSignal(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(owner)
	request, err := createHandoverSignal(pid)
	if err != nil {
		t.Fatalf("existing named event rejected its valid handle: %v", err)
	}
	if err := windows.SetEvent(request); err != nil {
		t.Fatal(err)
	}
	if err := windows.CloseHandle(request); err != nil {
		t.Fatal(err)
	}
	listener, err := createHandoverSignal(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(listener)
	if result, err := windows.WaitForSingleObject(listener, 0); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("early request vanished before listener startup: result=%d err=%v", result, err)
	}
	if result, err := windows.WaitForSingleObject(listener, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("one click delivered more than once: result=%d err=%v", result, err)
	}
	if err := windows.SetEvent(owner); err != nil {
		t.Fatal(err)
	}
	if result, err := windows.WaitForSingleObject(listener, 0); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("a later click could not reuse the event: result=%d err=%v", result, err)
	}
}

func TestCompanionHandoverRetainsOneOwnerUntilProcessDone(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	companion := &Companion{command: &exec.Cmd{Process: process}, done: done}
	closed := false
	defer func() {
		if !closed {
			close(done)
		}
	}()
	if !companion.OpenHandover() {
		t.Fatal("running companion rejected an explicit open request")
	}
	handoverSignalOwners.Lock()
	first := handoverSignalOwners.byCompanion[companion]
	handoverSignalOwners.Unlock()
	if first == nil {
		t.Fatal("request had no owner while the child was not listening")
	}
	listener, err := createHandoverSignal(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(listener)
	if result, err := windows.WaitForSingleObject(listener, 0); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("companion open was lost before listener startup: result=%d err=%v", result, err)
	}
	if !companion.OpenHandover() {
		t.Fatal("same companion could not request another open")
	}
	handoverSignalOwners.Lock()
	second := handoverSignalOwners.byCompanion[companion]
	handoverSignalOwners.Unlock()
	if second != first {
		t.Fatal("another click created another persistent owner")
	}
	if result, err := windows.WaitForSingleObject(listener, 0); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("second explicit open was lost: result=%d err=%v", result, err)
	}
	close(done)
	closed = true
	deadline := time.Now().Add(time.Second)
	for {
		handoverSignalOwners.Lock()
		retained := handoverSignalOwners.byCompanion[companion]
		handoverSignalOwners.Unlock()
		if retained == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exited companion retained its event handle")
		}
		time.Sleep(time.Millisecond)
	}
	if companion.OpenHandover() {
		t.Fatal("exited companion accepted another request")
	}
}

func TestCompanionHandoverRejectsUnavailableProcess(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	close(closed)
	for _, companion := range []*Companion{nil, {}, {stopping: true},
		{command: &exec.Cmd{Process: process}}, {command: &exec.Cmd{Process: process}, done: closed}} {
		if companion.OpenHandover() {
			t.Fatal("unavailable companion received an open request")
		}
	}
	if handle, err := createHandoverSignal(0); handle != 0 || err == nil {
		t.Fatalf("invalid parent PID created an event: handle=%d err=%v", handle, err)
	}
}

func TestHandoverSignalWatcherStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &ItemWindowApp{ctx: ctx}
	pid := uniqueSignalTestPID()
	finished := make(chan struct{})
	go func() {
		app.watchHandoverSignal(pid)
		close(finished)
	}()
	name, err := windows.UTF16PtrFromString(handoverSignalName(pid))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		handle, err := windows.OpenEvent(windows.SYNCHRONIZE, false, name)
		if err == nil {
			_ = windows.CloseHandle(handle)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event watcher did not enter its native wait")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("event watcher ignored cancellation")
	}
	// Kezdeti, még nem inicializált alkalmazás sem indít natív figyelőt.
	(&ItemWindowApp{}).watchHandoverSignal(uniqueSignalTestPID())
}

func TestHandoverSignalWatcherDeliversEarlyOpenWithoutCurrentItem(t *testing.T) {
	app, fake := newDevOnlyTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.ctx = ctx
	app.state.Item, app.state.Tracking = nil, nil
	pid := uniqueSignalTestPID()
	owner, err := createHandoverSignal(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(owner)
	if err := windows.SetEvent(owner); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		app.watchHandoverSignal(pid)
		close(finished)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		state := app.GetState()
		if state.Handover != nil && state.Handover.Open {
			if state.Handover.Error != "" || len(state.Handover.Items) != 0 || state.Item != nil || len(fake.writes) != 0 {
				t.Fatalf("native signal seeded a current item or changed stock: %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("early native request never opened the hand-in state")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("delivered event watcher did not stop with context")
	}
}
