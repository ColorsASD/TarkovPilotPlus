//go:build windows

package plus

import (
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var inspectUser32 = syscall.NewLazyDLL("user32.dll")
var inspectMouseButton = inspectUser32.NewProc("GetAsyncKeyState")
var inspectForegroundWindow = inspectUser32.NewProc("GetForegroundWindow")
var inspectWindowProcess = inspectUser32.NewProc("GetWindowThreadProcessId")

// The reference interaction waits for the UI after a left-button release.
// Coalescing a double click waits for its second release as well.
type inspectClickGate struct {
	pending time.Time
	last    time.Time
}

func (g *inspectClickGate) release(now time.Time) {
	g.pending = now.Add(200 * time.Millisecond)
}

func (g *inspectClickGate) ready(now time.Time) bool {
	if g.pending.IsZero() || now.Before(g.pending) || (!g.last.IsZero() && now.Sub(g.last) < 500*time.Millisecond) {
		return false
	}
	g.pending = time.Time{}
	g.last = now
	return true
}

func foregroundIsTarkov() bool {
	hwnd, _, _ := inspectForegroundWindow.Call()
	if hwnd == 0 {
		return false
	}
	var pid uint32
	inspectWindowProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return false
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	var path [32768]uint16
	length := uint32(len(path))
	if windows.QueryFullProcessImageName(process, 0, &path[0], &length) != nil {
		return false
	}
	return strings.EqualFold(filepath.Base(windows.UTF16ToString(path[:length])), "EscapeFromTarkov.exe")
}

func (a *ItemWindowApp) watchInspectClicks() {
	ticker := time.NewTicker(16 * time.Millisecond)
	defer ticker.Stop()
	requests := make(chan struct{}, 1)
	go a.runAutomaticInspects(requests)
	var gate inspectClickGate
	wasDown := false
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-ticker.C:
			key, _, _ := inspectMouseButton.Call(0x01) // VK_LBUTTON, no keyboard capture.
			down := key&0x8000 != 0
			released := wasDown && !down
			wasDown = down
			st := a.GetState()
			if !st.Enabled || !handoverAutomaticScan(st) || !st.HasAPIKey {
				gate.pending = time.Time{}
				continue
			}
			// Only release events and pending work inspect the foreground process.
			// The timer never takes a screenshot or invokes OCR by itself.
			if released && foregroundIsTarkov() {
				gate.release(now)
			}
			if gate.ready(now) && foregroundIsTarkov() {
				select {
				case requests <- struct{}{}:
				default: // Keep only the newest pending view while a scan is busy.
				}
			}
		}
	}
}

func (a *ItemWindowApp) runAutomaticInspects(requests <-chan struct{}) {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-requests:
			a.scanMu.Lock()
			// Pending clicks refer to the current view, so one fresh probe after
			// the active operation completes handles all coalesced requests.
			select {
			case <-requests:
			default:
			}
			if a.ctx.Err() == nil {
				st := a.GetState()
				if st.Enabled && handoverAutomaticScan(st) && st.HasAPIKey {
					a.scanLocked(true, 0)
				}
			}
			a.scanMu.Unlock()
		}
	}
}
