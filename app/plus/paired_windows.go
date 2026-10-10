package plus

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var pairedUser32 = syscall.NewLazyDLL("user32.dll")
var pairedEnumWindows = pairedUser32.NewProc("EnumWindows")
var pairedWindowPID = pairedUser32.NewProc("GetWindowThreadProcessId")
var pairedPostMessage = pairedUser32.NewProc("PostMessageW")
var pairedShowWindow = pairedUser32.NewProc("ShowWindow")
var pairedWindowText = pairedUser32.NewProc("GetWindowTextW")

type Companion struct {
	mu       sync.Mutex
	command  *exec.Cmd
	done     chan struct{}
	stopping bool
	onExit   func()
}

func NewCompanion(onExit func()) *Companion {
	return &Companion{onExit: onExit}
}

func (c *Companion) Start(hidden bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping {
		return errors.New("The companion window is stopping.")
	}
	if c.command != nil {
		return errors.New("The companion window is already running.")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"--item-window", "--parent-pid", strconv.Itoa(os.Getpid())}
	if hidden {
		args = append(args, "--hidden")
	}
	cmd := exec.Command(exe, args...)
	// Suppress a console without setting STARTUPINFO's SW_HIDE on the GUI.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return err
	}
	c.command = cmd
	c.done = make(chan struct{})
	done := c.done
	go func() {
		_ = cmd.Wait()
		close(done)
		c.mu.Lock()
		stopping := c.stopping
		c.mu.Unlock()
		if !stopping {
			// A normal close and an unexpected child exit both close the main
			// window. Closing the main window shuts the child down below.
			if c.onExit != nil {
				c.onExit()
			}
		}
	}()
	return nil
}

func forItemWindows(pid int, action func(uintptr)) int {
	count := 0
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var windowPID uint32
		pairedWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
		var title [128]uint16
		pairedWindowText.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
		if windowPID == uint32(pid) && syscall.UTF16ToString(title[:]) == WindowTitle {
			action(hwnd)
			count++
		}
		return 1
	})
	pairedEnumWindows.Call(callback, 0)
	return count
}

func (c *Companion) Show() {
	c.mu.Lock()
	cmd, done := c.command, c.done
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		go func() {
			// Autostart may be shown before WebView2 has created the child HWND.
			// Keep this request briefly rather than leaving the companion hidden.
			for attempt := 0; attempt < 50; attempt++ {
				if forItemWindows(cmd.Process.Pid, func(hwnd uintptr) { pairedShowWindow.Call(hwnd, 1) }) > 0 {
					return
				}
				select {
				case <-done:
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
		}()
	}
}

func (c *Companion) Stop() {
	c.mu.Lock()
	c.stopping = true
	cmd, done := c.command, c.done
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	forItemWindows(cmd.Process.Pid, func(hwnd uintptr) { pairedPostMessage.Call(hwnd, 0x0010, 0, 0) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
}
