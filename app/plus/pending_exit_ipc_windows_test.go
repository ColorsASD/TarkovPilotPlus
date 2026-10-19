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

func TestPendingExitWindowsPipeStatusPrepareAndReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var approved atomic.Bool
	var abandonedPreparations atomic.Int32
	if err := servePendingExit(ctx, os.Getpid(), func() int { return 4 }, func(discard bool) (int, bool, error) {
		if discard {
			approved.Store(true)
		}
		return 4, approved.Load(), nil
	}, func() {
		approved.Store(false)
		abandonedPreparations.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	for i, request := range []pendingExitRequest{{}, {Prepare: true}, {Prepare: true, Discard: true}, {}} {
		response, err := queryPendingExit(os.Getpid(), request, time.Second)
		if err != nil || response.Count != 4 || i == 1 && response.Approved || i == 2 && !response.Approved {
			t.Fatalf("roundtrip %d failed: %+v %v", i, response, err)
		}
	}
	// Egy kliens nyitva hagyott kapcsolata sem várathatja korlátlanul a
	// szervert, ha a válasz után nem küld ACK-ot és nem olvas tovább.
	approved.Store(false)
	name, _ := windows.UTF16PtrFromString(pendingExitPipeName(os.Getpid()))
	var abandoned windows.Handle
	abandonDeadline := time.Now().Add(time.Second)
	for {
		var err error
		abandoned, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			break
		}
		if time.Now().After(abandonDeadline) {
			t.Fatalf("could not create abandoned request: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer func() {
		if abandoned != windows.InvalidHandle {
			_ = windows.CloseHandle(abandoned)
		}
	}()
	var written uint32
	if err := windows.WriteFile(abandoned, []byte(`{"prepare":true,"discard":true}`), &written, nil); err != nil {
		t.Fatal(err)
	}
	response, err := queryPendingExit(os.Getpid(), pendingExitRequest{}, time.Second)
	if err != nil || response.Count != 4 {
		t.Fatalf("abandoned reader blocked a new request: %+v %v", response, err)
	}
	if approved.Load() || abandonedPreparations.Load() != 1 {
		t.Fatal("abandoned preparation permanently blocked new Add actions")
	}
	_ = windows.CloseHandle(abandoned)
	abandoned = windows.InvalidHandle
	// A főablak polling hívása nem várhat a pipe-ra.
	companion := NewCompanion(nil)
	companion.command = &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	start := time.Now()
	companion.PendingAddsStatus()
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("UI state waited for IPC")
	}
	deadline := time.Now().Add(time.Second)
	for {
		count, _ := companion.PendingAddsStatus()
		if count == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background count did not arrive")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	// Leállás után ugyanaz a név újra használható: nincs handle vagy fájl,
	// amely a következő appindítást vagy státuszkapcsolatot blokkolná.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	deadline = time.Now().Add(time.Second)
	for {
		err := servePendingExit(ctx2, os.Getpid(), func() int { return 0 }, func(bool) (int, bool, error) { return 0, true, nil }, nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pipe was not released after cancellation: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	response, err = queryPendingExit(os.Getpid(), pendingExitRequest{Prepare: true}, time.Second)
	if err != nil || response.Count != 0 || !response.Approved {
		t.Fatalf("regenerated server returned stale state: %+v %v", response, err)
	}
}
