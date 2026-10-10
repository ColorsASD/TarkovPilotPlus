//go:build windows

package scanner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("TARKOVPILOT_OCR_TEST_STALL") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOCRProtocolAndClose(t *testing.T) {
	ocr := New([]byte(`
while ($null -ne ($line = [Console]::In.ReadLine())) {
    $request = $line | ConvertFrom-Json
    $reply = @{lines=@('Árvíztűrő');groups=@(@('Árvíztűrő'),@());languages=@($request.language);error='';active=$request.automatic}
    [Console]::Out.WriteLine(($reply | ConvertTo-Json -Compress))
    [Console]::Out.Flush()
}`))
	t.Cleanup(func() { _ = ocr.Close() })
	for _, automatic := range []bool{true, false} {
		result, err := ocr.Scan(context.Background(), "en", automatic)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Lines) != 1 || result.Lines[0] != "Árvíztűrő" || len(result.Languages) != 1 || result.Languages[0] != "en" || result.Active != automatic {
			t.Fatalf("unexpected protocol result: %+v", result)
		}
		if len(result.Groups) != 2 || len(result.Groups[0]) != 1 || result.Groups[0][0] != "Árvíztűrő" || len(result.Groups[1]) != 0 {
			t.Fatal("title variants or UTF-8 protocol were not preserved")
		}
	}
	if err := ocr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ocr.Scan(context.Background(), "en", true); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("scan after close = %v", err)
	}
}

func TestOCRProbeProtocol(t *testing.T) {
	ocr := New([]byte(`
while ($null -ne ($line = [Console]::In.ReadLine())) {
    $request = $line | ConvertFrom-Json
    $probeError = ''
    if ($request.operation -ne 'probe' -or $request.language -ne '') { $probeError = 'invalid probe request' }
    $reply = @{lines=@();groups=@();languages=@('en','ru');error=$probeError;active=$request.automatic;inspectOpen=$true;fingerprint=('a' * 64)}
    [Console]::Out.WriteLine(($reply | ConvertTo-Json -Compress))
    [Console]::Out.Flush()
}`))
	t.Cleanup(func() { _ = ocr.Close() })
	for _, automatic := range []bool{true, false} {
		result, err := ocr.Probe(context.Background(), automatic)
		if err != nil {
			t.Fatal(err)
		}
		if result.Error != "" || result.Active != automatic || !result.InspectOpen || result.Fingerprint != strings.Repeat("a", 64) || len(result.Lines) != 0 || len(result.Groups) != 0 {
			t.Fatalf("invalid probe response: %+v", result)
		}
	}
}

func TestInspectionDetectorGeometry(t *testing.T) {
	runtime, err := filepath.Abs(filepath.Join("..", "..", "ocr", "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(runtime, "Tesseract.dll")); err != nil {
		t.Skip("build-time OCR runtime is not prepared")
	}
	t.Setenv("TARKOVPILOT_OCR_DIR", runtime)
	helper, err := os.ReadFile(filepath.Join("..", "..", "ocr", "name-scan.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	boundary := strings.Index(string(helper), "while ($null -ne ($requestLine")
	if boundary < 0 {
		t.Fatal("helper definition boundary is missing")
	}
	fixtures, err := os.ReadFile(filepath.Join("..", "..", "ocr", "inspection-tests.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	// Load the real detector and synthetic bitmaps, then serve a test-only
	// protocol. None of the screen capture or foreground APIs are invoked.
	script := string(helper[:boundary]) + string(fixtures) + `
while ($null -ne ([Console]::In.ReadLine())) {
    $failures = @()
    if ($initializationError) { $failures += $initializationError } else { $failures = @(Test-InspectionGeometry) }
    $reply = @{lines=@();groups=@();languages=@('en','ru');error=($failures -join '; ');active=$false;inspectOpen=$false;fingerprint=''}
    [Console]::Out.WriteLine(($reply | ConvertTo-Json -Compress))
    [Console]::Out.Flush()
}
foreach ($engine in $engines.Values) { $engine.Dispose() }
`
	ocr := New([]byte(script))
	t.Cleanup(func() { _ = ocr.Close() })
	result, err := ocr.Probe(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatal(result.Error)
	}
}

func TestOCRStartupCancellation(t *testing.T) {
	// Use the test executable as a helper that never reads its stdin. A large
	// bootstrap then blocks until the cancelled scan terminates that process.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeRoot := t.TempDir()
	fakeShell := filepath.Join(fakeRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if err = os.MkdirAll(filepath.Dir(fakeShell), 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.Create(fakeShell)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy helper: %v, close: %v", copyErr, closeErr)
	}
	t.Setenv("SystemRoot", fakeRoot)
	t.Setenv("TARKOVPILOT_OCR_TEST_STALL", "1")
	ocr := New([]byte(strings.Repeat("# bootstrap\n", 100000)))
	t.Cleanup(func() { _ = ocr.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err = ocr.Scan(ctx, "en", true); err != context.DeadlineExceeded {
		t.Fatalf("cancelled startup = %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("startup cancellation did not stop the blocked pipe write")
	}
}

func TestOCRCancellationStopsHelper(t *testing.T) {
	ocr := New([]byte(`while ($null -ne ($line = [Console]::In.ReadLine())) { Start-Sleep -Seconds 60 }`))
	t.Cleanup(func() { _ = ocr.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := ocr.Scan(ctx, "en", true); err != context.DeadlineExceeded {
		t.Fatalf("cancelled scan = %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("cancellation did not stop the helper promptly")
	}
	ocr.mu.Lock()
	session := ocr.session
	ocr.mu.Unlock()
	if session != nil {
		t.Fatal("cancelled helper remained attached")
	}
}

func TestOCRCloseInterruptsPendingScan(t *testing.T) {
	ocr := New([]byte(`while ($null -ne ($line = [Console]::In.ReadLine())) { Start-Sleep -Seconds 60 }`))
	finished := make(chan error, 1)
	go func() {
		_, err := ocr.Scan(context.Background(), "en", true)
		finished <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		ocr.mu.Lock()
		started := ocr.session != nil
		ocr.mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			_ = ocr.Close()
			t.Fatal("helper did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := ocr.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("interrupted scan unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the helper did not interrupt the scan")
	}
}
