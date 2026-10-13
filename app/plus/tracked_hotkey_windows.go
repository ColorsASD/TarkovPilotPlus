//go:build windows

package plus

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var trackedHotkeyUser32 = syscall.NewLazyDLL("user32.dll")
var trackedHotkeySetHook = trackedHotkeyUser32.NewProc("SetWindowsHookExW")
var trackedHotkeyUnhook = trackedHotkeyUser32.NewProc("UnhookWindowsHookEx")
var trackedHotkeyNextHook = trackedHotkeyUser32.NewProc("CallNextHookEx")
var trackedHotkeyPeekMessage = trackedHotkeyUser32.NewProc("PeekMessageW")
var trackedHotkeyDispatchMessage = trackedHotkeyUser32.NewProc("DispatchMessageW")
var trackedHotkeyWaitMessages = trackedHotkeyUser32.NewProc("MsgWaitForMultipleObjects")
var trackedHotkeyAsyncState = trackedHotkeyUser32.NewProc("GetAsyncKeyState")
var trackedHotkeyKernel32 = syscall.NewLazyDLL("kernel32.dll")
var trackedHotkeyModuleHandle = trackedHotkeyKernel32.NewProc("GetModuleHandleW")

type trackedHotkeyMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	X       int32
	Y       int32
	Private uint32
}

type trackedHotkeyKeyboardInput struct {
	Key       uint32
	ScanCode  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type trackedHotkeyStatus struct {
	ready bool
	error string
}

type trackedAddHotkey struct {
	ctx          context.Context
	cancel       context.CancelFunc
	key          atomic.Uint32
	bindings     atomic.Pointer[trackedHotkeyBindings]
	installation atomic.Pointer[trackedHotkeyStatus]
	stopping     atomic.Bool
	trigger      func()
	triggerFIR   func()
	status       func(bool, string)
	statuses     chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
}

// trigger runs on the keyboard hook thread at the first physical key-down. It
// must only capture atomic state and send a nonblocking request; application
// locks, API requests, OCR, logging, and UI calls belong to the action worker.
func newTrackedAddHotkey(ctx context.Context, trigger func(), status func(bool, string), firTrigger ...func()) *trackedAddHotkey {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &trackedAddHotkey{
		ctx: ctx, cancel: cancel, trigger: trigger, status: status,
		statuses: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if len(firTrigger) > 0 {
		h.triggerFIR = firTrigger[0]
	}
	// Keep the reservation disabled until the caller applies its saved option.
	// This avoids briefly capturing a default when the saved setting is disabled
	// or uses a different shortcut.
	go h.runStatuses()
	go h.runHook()
	return h
}

func (h *trackedAddHotkey) SetKey(key string) {
	h.SetKeys(key, "off")
}

func (h *trackedAddHotkey) SetKeys(regular, fir string) {
	if h != nil {
		_, regularChord, _ := parseTrackedShortcut(normalizeTrackedAddHotkey(regular))
		_, firChord, _ := parseTrackedShortcut(normalizeTrackedFIRHotkey(fir))
		binding := &trackedHotkeyBindings{regular: regularChord, fir: firChord}
		if binding.regular != 0 && binding.regular == binding.fir {
			// SetOptions rejects duplicates. Keep native configuration fail-closed
			// if a caller bypasses that validation rather than guessing a quality.
			binding = &trackedHotkeyBindings{}
		}
		h.bindings.Store(binding)
		h.key.Store(binding.regular)
		h.reportCurrent()
	}
}

func (h *trackedAddHotkey) Close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		h.stopping.Store(true)
		h.key.Store(0)
		h.bindings.Store(nil)
		h.cancel()
	})
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-h.done:
	case <-timer.C:
		h.setInstallation(false, "Items Tracker shortcut could not shut down in time.")
	}
}

func (h *trackedAddHotkey) setInstallation(ready bool, message string) {
	h.installation.Store(&trackedHotkeyStatus{ready: ready, error: message})
	h.reportCurrent()
}

func (h *trackedAddHotkey) reportCurrent() {
	select {
	case h.statuses <- struct{}{}:
	default:
	}
}

func (h *trackedAddHotkey) currentStatus() trackedHotkeyStatus {
	binding := h.bindings.Load()
	enabled := binding != nil && (binding.regular != 0 || binding.fir != 0)
	if !enabled && !h.stopping.Load() {
		return trackedHotkeyStatus{}
	}
	if state := h.installation.Load(); state != nil {
		return trackedHotkeyStatus{ready: state.ready && enabled && !h.stopping.Load(), error: state.error}
	}
	return trackedHotkeyStatus{}
}

func (h *trackedAddHotkey) runStatuses() {
	for {
		select {
		case <-h.ctx.Done():
			if h.status != nil {
				value := h.currentStatus()
				h.status(false, value.error)
			}
			return
		case <-h.statuses:
			if h.status != nil {
				value := h.currentStatus()
				h.status(value.ready, value.error)
			}
		}
	}
}

func trackedHotkeyNativeError(operation string, err error) string {
	if err == nil || err == syscall.Errno(0) {
		return "Items Tracker shortcut is unavailable: " + operation + " failed."
	}
	return fmt.Sprintf("Items Tracker shortcut is unavailable: %s: %v", operation, err)
}

func (h *trackedAddHotkey) runHook() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(h.done)
	if h.ctx.Err() != nil {
		return
	}

	// The cancellation event wakes the dedicated message pump even if no user
	// input arrives. Shutdown does not depend on a successful posted message.
	wake, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		h.setInstallation(false, trackedHotkeyNativeError("CreateEvent", err))
		return
	}
	defer windows.CloseHandle(wake)
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		select {
		case <-h.ctx.Done():
			h.stopping.Store(true)
			_ = windows.SetEvent(wake)
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		<-watchExited
	}()

	var message trackedHotkeyMessage
	// PeekMessage creates the hook thread's Windows message queue.
	trackedHotkeyPeekMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 0)
	var keys trackedHotkeyKeys
	callback := syscall.NewCallback(func(code, kind uintptr, input *trackedHotkeyKeyboardInput) uintptr {
		if int32(code) >= 0 && input != nil {
			down := kind == 0x100 || kind == 0x104 // WM_(SYS)KEYDOWN.
			up := kind == 0x101 || kind == 0x105   // WM_(SYS)KEYUP.
			if down || up {
				event := input
				consume := h.keyEvent(&keys, event.Key, down, event.Flags&0x12 != 0, event.Flags&0x20 != 0)
				if consume {
					return 1
				}
			}
		}
		result, _, _ := trackedHotkeyNextHook.Call(0, code, kind, uintptr(unsafe.Pointer(input)))
		return result
	})
	module, _, err := trackedHotkeyModuleHandle.Call(0)
	if module == 0 {
		h.setInstallation(false, trackedHotkeyNativeError("GetModuleHandle", err))
		return
	}
	hook, _, err := trackedHotkeySetHook.Call(13, callback, module, 0) // WH_KEYBOARD_LL, this desktop.
	if hook == 0 {
		h.setInstallation(false, trackedHotkeyNativeError("SetWindowsHookEx", err))
		return
	}
	defer func() {
		unhooked, _, unhookErr := trackedHotkeyUnhook.Call(hook)
		runtime.KeepAlive(callback)
		if unhooked == 0 {
			h.setInstallation(false, trackedHotkeyNativeError("UnhookWindowsHookEx", unhookErr))
		} else if h.ctx.Err() != nil {
			h.setInstallation(false, "")
		}
	}()
	// Sample only after installation, so a key-down delivered before the hook
	// cannot fall into a sampling/install gap and lose its forwarded key-up.
	// Asynchronous key state is never read from inside the hook callback.
	for _, key := range trackedHotkeyModifierKeys {
		value, _, _ := trackedHotkeyAsyncState.Call(uintptr(key))
		keys.seed(key, value&0x8000 != 0)
	}
	for key := uint32(0); key <= trackedHotkeyKeyMask; key++ {
		if !trackedHotkeySupportedKey(key) {
			continue
		}
		value, _, _ := trackedHotkeyAsyncState.Call(uintptr(key))
		keys.seed(key, value&0x8000 != 0)
	}
	h.setInstallation(true, "")
	for h.ctx.Err() == nil {
		result, _, waitErr := trackedHotkeyWaitMessages.Call(1, uintptr(unsafe.Pointer(&wake)), 0, 0xffffffff, 0x04ff) // INFINITE, QS_ALLINPUT.
		if result == 0 {                                                                                               // Cancellation event.
			return
		}
		if result == 0xffffffff {
			h.setInstallation(false, trackedHotkeyNativeError("MsgWaitForMultipleObjects", waitErr))
			return
		}
		for h.ctx.Err() == nil {
			found, _, _ := trackedHotkeyPeekMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 1) // PM_REMOVE.
			if found == 0 {
				break
			}
			if message.Message == 0x12 { // WM_QUIT.
				h.setInstallation(false, "Items Tracker shortcut stopped.")
				return
			}
			trackedHotkeyDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
		}
	}
}

// A binding is sampled once per event so configuration changes cannot mix the
// Regular and FiR destinations inside one callback. Physical hold state stays
// in the shared hook state, including captured releases after mode changes.
func (h *trackedAddHotkey) keyEvent(keys *trackedHotkeyKeys, key uint32, down, injected, alt bool) bool {
	binding := h.bindings.Load()
	var configured uint32
	if binding != nil {
		configured = binding.configuredKey(key, keys.modifierMask(alt))
	}
	consume, trigger := keys.event(key, down, injected, alt, configured)
	if trigger && !h.stopping.Load() {
		action := h.trigger
		if binding != nil && configured == binding.fir {
			action = h.triggerFIR
		}
		if action != nil {
			action()
		}
	}
	return consume
}
