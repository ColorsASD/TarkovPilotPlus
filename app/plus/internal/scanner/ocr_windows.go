//go:build windows

package scanner

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Result contains locally recognised text and the bundled OCR language inventory.
type Result struct {
	Lines         []string   `json:"lines"`
	Groups        [][]string `json:"groups,omitempty"`
	Languages     []string   `json:"languages"`
	Error         string     `json:"error"`
	Active        bool       `json:"active"`
	InspectOpen   bool       `json:"inspectOpen"`
	InspectCount  int        `json:"inspectCount"`
	TitleVariants [][]string `json:"titleVariants,omitempty"`
	Fingerprint   string     `json:"fingerprint"`
}

type ocrSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stop   sync.Once
}

func (s *ocrSession) close() {
	s.stop.Do(func() {
		_ = s.stdin.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
	})
}

// OCR runs the bundled Tesseract engine in an isolated local helper.
// The helper receives its script through stdin and never writes captures to disk.
type OCR struct {
	script  []byte
	scanMu  sync.Mutex
	mu      sync.Mutex
	session *ocrSession
	closed  bool
}

func New(script []byte) *OCR {
	return &OCR{script: append([]byte(nil), script...)}
}

func (o *OCR) sessionForScan(ctx context.Context) (*ocrSession, error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, errors.New("the item scanner has already stopped")
	}
	if o.session != nil {
		session := o.session
		o.mu.Unlock()
		return session, nil
	}
	if len(o.script) == 0 {
		o.mu.Unlock()
		return nil, errors.New("the local item scanner helper is missing")
	}

	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if os.Getenv("SystemRoot") == "" {
		powershell = "powershell.exe"
	}
	// Loading the embedded script from stdin avoids both temporary executable
	// files and command-line size limits. Only the scan protocol follows it.
	bootstrap := "[Console]::InputEncoding = New-Object Text.UTF8Encoding($false); [Console]::OutputEncoding = New-Object Text.UTF8Encoding($false); $source = [Console]::In.ReadLine(); & ([ScriptBlock]::Create([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($source))))"
	cmd := exec.Command(powershell, "-NoProfile", "-STA", "-NonInteractive", "-Command", bootstrap)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), "TARKOVPILOT_OCR_DIR="+runtimeDirectory())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		o.mu.Unlock()
		return nil, fmt.Errorf("the item scanner input cannot be opened: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		o.mu.Unlock()
		return nil, fmt.Errorf("the item scanner output cannot be opened: %w", err)
	}
	// Structured errors travel through stdout. PowerShell diagnostics can contain
	// source excerpts, so they are deliberately kept out of application logs.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		o.mu.Unlock()
		return nil, fmt.Errorf("the local item scanner cannot be started: %w", err)
	}
	session := &ocrSession{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1024*1024)}
	// Publish the process before a potentially blocking pipe write. Close and
	// cancellation must be able to stop a helper that never reads its bootstrap.
	o.session = session
	o.mu.Unlock()
	written := make(chan error, 1)
	go func() {
		_, writeErr := io.WriteString(stdin, base64.StdEncoding.EncodeToString(o.script)+"\n")
		written <- writeErr
	}()
	select {
	case <-ctx.Done():
		o.discardSession(session)
		return nil, ctx.Err()
	case writeErr := <-written:
		if writeErr != nil {
			o.discardSession(session)
			return nil, fmt.Errorf("the local item scanner cannot be loaded: %w", writeErr)
		}
		return session, nil
	}
}

func runtimeDirectory() string {
	if configured := os.Getenv("TARKOVPILOT_OCR_DIR"); configured != "" {
		return configured
	}
	var candidates []string
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "ocr"))
	}
	if working, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(working, "plus", "ocr", "runtime"), filepath.Join(working, "app", "plus", "ocr", "runtime"))
	}
	for _, candidate := range candidates {
		if stat, err := os.Stat(filepath.Join(candidate, "Tesseract.dll")); err == nil && !stat.IsDir() {
			return candidate
		}
	}
	if len(candidates) != 0 {
		return candidates[0]
	}
	return "ocr"
}

func (o *OCR) discardSession(session *ocrSession) {
	o.mu.Lock()
	if o.session == session {
		o.session = nil
	}
	o.mu.Unlock()
	session.close()
}

// Probe checks the visible game for an inspection title using pixels alone.
// It never invokes OCR and returns a fingerprint of the detected title pixels.
func (o *OCR) Probe(ctx context.Context, automatic bool) (Result, error) {
	return o.request(ctx, "probe", "", automatic)
}

// Scan recognises only titles behind the inspection marker and geometry gate.
// Automatic scans require the game to be foreground; manual scans may find a
// visible game behind the control panel. The gate is rechecked for every scan.
func (o *OCR) Scan(ctx context.Context, language string, automatic bool) (Result, error) {
	return o.request(ctx, "scan", language, automatic)
}

func (o *OCR) request(ctx context.Context, operation, language string, automatic bool) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	o.scanMu.Lock()
	defer o.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	session, err := o.sessionForScan(ctx)
	if err != nil {
		return Result{}, err
	}
	request := struct {
		Operation string `json:"operation"`
		Language  string `json:"language"`
		Automatic bool   `json:"automatic"`
	}{operation, language, automatic}
	requestData, err := json.Marshal(request)
	if err != nil {
		return Result{}, err
	}
	written := make(chan error, 1)
	go func() {
		_, writeErr := session.stdin.Write(append(requestData, '\n'))
		written <- writeErr
	}()
	select {
	case <-ctx.Done():
		o.discardSession(session)
		return Result{}, ctx.Err()
	case writeErr := <-written:
		if writeErr != nil {
			o.discardSession(session)
			return Result{}, fmt.Errorf("the item scan request cannot be sent: %w", writeErr)
		}
	}

	type response struct {
		data []byte
		err  error
	}
	received := make(chan response, 1)
	go func() {
		// ReadSlice bounds a malformed response; screenshots never enter this pipe.
		data, readErr := session.stdout.ReadSlice('\n')
		received <- response{data: append([]byte(nil), data...), err: readErr}
	}()
	select {
	case <-ctx.Done():
		o.discardSession(session)
		return Result{}, ctx.Err()
	case reply := <-received:
		if reply.err != nil {
			o.discardSession(session)
			return Result{}, errors.New("the local item scanner stopped or returned an invalid response")
		}
		var result Result
		if err = json.Unmarshal(reply.data, &result); err != nil {
			o.discardSession(session)
			return Result{}, errors.New("the local item scanner returned an invalid response")
		}
		return result, nil
	}
}

// Close interrupts a pending scan and prevents further helper launches.
func (o *OCR) Close() error {
	o.mu.Lock()
	o.closed = true
	session := o.session
	o.session = nil
	o.mu.Unlock()
	if session != nil {
		session.close()
	}
	return nil
}
