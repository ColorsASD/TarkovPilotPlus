//go:build windows

package plus

import (
	"errors"
	"os"
	"strconv"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

type handoverSignalOwner struct {
	handle windows.Handle
	done   <-chan struct{}
}

// A szülő egy nyitott fogantyúval megőrzi a túl korán érkezett kérést is.
// Az event a gyermek indulása előtt sem szűnik meg, és a folyamat végén zárul.
var handoverSignalOwners = struct {
	sync.Mutex
	byCompanion map[*Companion]*handoverSignalOwner
}{byCompanion: make(map[*Companion]*handoverSignalOwner)}

func handoverSignalName(parentPID int) string {
	return `Local\TarkovPilotPlus-HandIn-` + strconv.Itoa(parentPID)
}

func createHandoverSignal(parentPID int) (windows.Handle, error) {
	if parentPID <= 0 {
		return 0, windows.ERROR_INVALID_PARAMETER
	}
	name, err := windows.UTF16PtrFromString(handoverSignalName(parentPID))
	if err != nil {
		return 0, err
	}
	// Az auto-reset event több egymás utáni kattintást egy függő kéréssé vonhat.
	handle, err := windows.CreateEvent(nil, 0, 0, name)
	if handle != 0 && errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return handle, nil
	}
	return handle, err
}

func retainHandoverSignal(c *Companion, done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	handoverSignalOwners.Lock()
	defer handoverSignalOwners.Unlock()
	if owner := handoverSignalOwners.byCompanion[c]; owner != nil {
		return owner.done == done
	}
	handle, err := createHandoverSignal(os.Getpid())
	if err != nil || handle == 0 {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return false
	}
	owner := &handoverSignalOwner{handle: handle, done: done}
	handoverSignalOwners.byCompanion[c] = owner
	go func() {
		<-done
		handoverSignalOwners.Lock()
		if handoverSignalOwners.byCompanion[c] == owner {
			delete(handoverSignalOwners.byCompanion, c)
		}
		_ = windows.CloseHandle(owner.handle)
		handoverSignalOwners.Unlock()
	}()
	return true
}

// OpenHandover a főablak kifejezett kattintását küldi a futó társablaknak.
// A jelzés nem tartalmaz tárgyadatot, API-kulcsot vagy hálózati üzenetet.
func (c *Companion) OpenHandover() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	cmd, done, stopping := c.command, c.done, c.stopping
	c.mu.Unlock()
	if stopping || cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 || done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
	}
	if !retainHandoverSignal(c, done) {
		return false
	}
	name, err := windows.UTF16PtrFromString(handoverSignalName(os.Getpid()))
	if err != nil {
		return false
	}
	handle, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil || handle == 0 {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return false
	}
	defer windows.CloseHandle(handle)
	select {
	case <-done:
		return false
	default:
	}
	if windows.SetEvent(handle) != nil {
		return false
	}
	c.Show()
	return true
}

// A gyermek csak a kézzel kért megnyitáskor jeleníti meg az ablakot.
// A rövid várakozás a context leállítását legfeljebb fél másodperccel késlelteti.
func (a *ItemWindowApp) watchHandoverSignal(parentPID int) {
	if a == nil || a.ctx == nil || parentPID <= 0 {
		return
	}
	ctx := a.ctx
	handle, err := createHandoverSignal(parentPID)
	if err != nil || handle == 0 {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return
	}
	defer windows.CloseHandle(handle)
	for ctx.Err() == nil {
		result, waitErr := windows.WaitForSingleObject(handle, 500)
		if waitErr != nil {
			return
		}
		if result == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		if result != windows.WAIT_OBJECT_0 || ctx.Err() != nil {
			return
		}
		a.OpenHandover()
		if ctx.Err() == nil && a.wailsCtx != nil {
			wailsRuntime.WindowShow(a.wailsCtx)
		}
	}
}
