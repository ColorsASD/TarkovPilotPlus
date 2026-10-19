package plus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type pendingExitRequest struct {
	Prepare bool `json:"prepare"`
	Discard bool `json:"discard"`
}

type pendingExitResponse struct {
	Count    int  `json:"count"`
	Approved bool `json:"approved"`
}

func pendingExitPipeName(pid int) string {
	return fmt.Sprintf(`\\.\pipe\TarkovPilotPlus.PendingExit.%d`, pid)
}

// A névhez nem tartozik fájl. Csak a tényleges szülőfolyamat kérdezheti le
// vagy hagyhatja jóvá a kilépést; a kliens a szerver PID-jét is ellenőrzi.
func servePendingExit(ctx context.Context, parentPID int, count func() int, prepare func(bool) (int, bool, error), abandoned func()) error {
	name, err := windows.UTF16PtrFromString(pendingExitPipeName(os.Getpid()))
	if err != nil {
		return err
	}
	pipe, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_NOWAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 512, 512, 0, nil)
	if err != nil {
		return err
	}
	go func() {
		defer windows.CloseHandle(pipe)
		for ctx.Err() == nil {
			err := windows.ConnectNamedPipe(pipe, nil)
			if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
				if errors.Is(err, windows.ERROR_NO_DATA) {
					_ = windows.DisconnectNamedPipe(pipe)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
				continue
			}
			var clientPID uint32
			if windows.GetNamedPipeClientProcessId(pipe, &clientPID) != nil || int(clientPID) != parentPID {
				_ = windows.DisconnectNamedPipe(pipe)
				continue
			}
			body, err := readPendingExitMessage(ctx, pipe, 300*time.Millisecond)
			var request pendingExitRequest
			if err == nil && json.Unmarshal(body, &request) == nil {
				response := pendingExitResponse{Count: count()}
				if request.Prepare {
					response.Count, response.Approved, err = prepare(request.Discard)
				}
				acknowledged := false
				if err == nil {
					encoded, _ := json.Marshal(response)
					var written uint32
					if windows.WriteFile(pipe, encoded, &written, nil) == nil {
						// A flush korlátlanul várhatna egy megszűnt kliensre.
						// A rövid ACK megtartja a választ az elolvasásig, de a
						// következő kapcsolatot és a leállást nem akaszthatja meg.
						ack, ackErr := readPendingExitMessage(ctx, pipe, 200*time.Millisecond)
						acknowledged = ackErr == nil && string(ack) == "ok"
					}
				}
				if request.Prepare && response.Approved && !acknowledged && abandoned != nil {
					// A főablak hiba esetén nyitva marad. A félbeszakadt
					// kézfogás ezért az új Add elfogadását sem tilthatja végleg.
					abandoned()
				}
			}
			_ = windows.DisconnectNamedPipe(pipe)
		}
	}()
	return nil
}

func readPendingExitMessage(ctx context.Context, pipe windows.Handle, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var buffer [512]byte
	for {
		var read uint32
		err := windows.ReadFile(pipe, buffer[:], &read, nil)
		if err == nil && read > 0 {
			return buffer[:read], nil
		}
		if err != nil && !errors.Is(err, windows.ERROR_NO_DATA) && !errors.Is(err, windows.ERROR_PIPE_LISTENING) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func queryPendingExit(pid int, request pendingExitRequest, timeout time.Duration) (pendingExitResponse, error) {
	var response pendingExitResponse
	name, err := windows.UTF16PtrFromString(pendingExitPipeName(pid))
	if err != nil {
		return response, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var pipe windows.Handle
	for {
		pipe, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return response, err
		}
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer windows.CloseHandle(pipe)
	var serverPID uint32
	if err = windows.GetNamedPipeServerProcessId(pipe, &serverPID); err != nil || int(serverPID) != pid {
		return response, errors.New("Pending save status came from a different process")
	}
	mode := uint32(windows.PIPE_READMODE_MESSAGE | windows.PIPE_NOWAIT)
	if err = windows.SetNamedPipeHandleState(pipe, &mode, nil, nil); err != nil {
		return response, err
	}
	encoded, _ := json.Marshal(request)
	var written uint32
	if err = windows.WriteFile(pipe, encoded, &written, nil); err != nil {
		return response, err
	}
	body, err := readPendingExitMessage(ctx, pipe, timeout)
	if err != nil {
		return response, err
	}
	if json.Unmarshal(body, &response) != nil || response.Count < 0 {
		return response, errors.New("Pending save status is invalid")
	}
	if err = windows.WriteFile(pipe, []byte("ok"), &written, nil); err != nil {
		return response, err
	}
	return response, nil
}

func (c *Companion) runningPIDLocked() int {
	if c.command == nil || c.command.Process == nil {
		return 0
	}
	if c.done != nil {
		select {
		case <-c.done:
			return 0
		default:
		}
	}
	return c.command.Process.Pid
}

func (c *Companion) PrepareExit(discard bool) (int, bool, error) {
	c.mu.Lock()
	pid := c.runningPIDLocked()
	c.mu.Unlock()
	if pid == 0 {
		return 0, true, nil
	}
	response, err := queryPendingExit(pid, pendingExitRequest{Prepare: true, Discard: discard}, time.Second)
	if err != nil {
		c.mu.Lock()
		running := c.runningPIDLocked()
		c.mu.Unlock()
		if running == 0 {
			return 0, true, nil
		}
	}
	return response.Count, response.Approved, err
}

// A főablak GetState hívása csak a gyorsítótárat olvassa. A pipe legfeljebb
// fél másodpercenként, külön goroutine-ban frissül; az UI nem vár IPC-re.
func (c *Companion) PendingAddsStatus() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pid := c.runningPIDLocked()
	if pid == 0 {
		c.pendingCount, c.pendingError = 0, ""
		return 0, ""
	}
	if !c.pendingProbeRunning && time.Since(c.pendingProbeAt) >= 500*time.Millisecond {
		c.pendingProbeRunning, c.pendingProbeAt = true, time.Now()
		go func() {
			response, err := queryPendingExit(pid, pendingExitRequest{}, 200*time.Millisecond)
			c.mu.Lock()
			defer c.mu.Unlock()
			c.pendingProbeRunning = false
			if c.runningPIDLocked() != pid {
				return
			}
			if err != nil {
				c.pendingError = "Pending Tarkov Market saves could not be checked."
				return
			}
			c.pendingCount, c.pendingError = response.Count, ""
		}()
	}
	return c.pendingCount, c.pendingError
}
