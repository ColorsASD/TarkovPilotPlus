//go:build windows

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutomaticAwlHeaderUsesRealDetectorAndBundledOCR(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("testdata", "awl-inspect-header.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(fixture); err != nil {
		t.Fatalf("the cropped Awl inspection regression fixture is missing: %v", err)
	}
	t.Setenv("TARKOVPILOT_OCR_AWL_FIXTURE", fixture)
	source := inspectionHelperDefinitions(t)
	before := `[DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();`
	after := `public static IntPtr GetForegroundWindow() { return new IntPtr(1); }`
	if !strings.Contains(source, before) {
		t.Fatal("foreground test seam is missing")
	}
	// Detect and read the real screenshot's cropped header with production
	// preprocessing and the bundled English Tesseract engine. Only foreground
	// identity is simulated: this protocol never captures a screen or queries,
	// activates, or reads a real game window, and it makes no network requests.
	script := strings.Replace(source, before, after, 1) + `
while ($null -ne ($fixtureRequestLine = [Console]::In.ReadLine())) {
    $bitmap = $null
    $reply = @{lines=@();groups=@();titleVariants=@();languages=@('en');error='';active=$true;inspectOpen=$false;inspectCount=0;fingerprint=''}
    try {
        if ($initializationError) { throw $initializationError }
        $request = $fixtureRequestLine | ConvertFrom-Json
        if (-not $request.automatic) { throw 'The Awl regression must exercise an automatic request.' }
        $bitmap = [Drawing.Bitmap]::FromFile($env:TARKOVPILOT_OCR_AWL_FIXTURE)
        $titles = [Drawing.Rectangle[]]@([TarkovPilotCapture.InspectionDetector]::Detect($bitmap))
        $reply.inspectOpen = $titles.Count -gt 0
        $reply.inspectCount = $titles.Count
        if ($titles.Count -gt 0) { $reply.fingerprint = [TarkovPilotCapture.InspectionDetector]::Fingerprint($bitmap, $titles) }
        if ($request.operation -eq 'scan') {
            if ($request.language -ne 'en') { throw 'The Awl regression must use the bundled English model.' }
            $recognized = Read-InspectionVariants (Get-Engine 'en') $bitmap $titles 1.0 ([IntPtr]1) $true
            if (-not $recognized.Completed) { throw 'The simulated foreground inspection was unexpectedly discarded.' }
            $reply.lines = $recognized.Lines
            $reply.groups = $recognized.Groups
            $reply.titleVariants = $recognized.TitleVariants
        } elseif ($request.operation -ne 'probe') {
            throw 'Unknown Awl fixture operation.'
        }
    } catch {
        $reply.error = $_.Exception.Message
    } finally {
        if ($null -ne $bitmap) { $bitmap.Dispose() }
    }
    [Console]::Out.WriteLine(($reply | ConvertTo-Json -Compress -Depth 3))
    [Console]::Out.Flush()
}
foreach ($engine in $engines.Values) { $engine.Dispose() }
`
	ocr := New([]byte(script))
	t.Cleanup(func() { _ = ocr.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	probe, err := ocr.Probe(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Error != "" || !probe.Active || !probe.InspectOpen || probe.InspectCount != 1 || len(probe.Fingerprint) != 64 {
		t.Fatalf("the short Awl title did not pass the automatic pixel-only inspection probe: %+v", probe)
	}
	if len(probe.Lines) != 0 || len(probe.Groups) != 0 || len(probe.TitleVariants) != 0 {
		t.Fatalf("the pixel-only probe unexpectedly returned recognized text: %+v", probe)
	}
	result, err := ocr.Scan(ctx, "en", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || !result.Active || !result.InspectOpen || result.InspectCount != 1 || result.Fingerprint != probe.Fingerprint || len(result.TitleVariants) != 1 || len(result.Groups) != 3 {
		t.Fatalf("the real Awl header did not retain one inspection and its three OCR recipes: %+v", result)
	}
	for _, title := range result.TitleVariants[0] {
		if title == "Awl" {
			return
		}
	}
	t.Fatalf("the bundled English engine did not read Awl from the real header: %+v", result)
}
