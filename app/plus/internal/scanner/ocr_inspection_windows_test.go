//go:build windows

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inspectionHelperDefinitions(t *testing.T) string {
	t.Helper()
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
	return string(helper[:boundary])
}

func TestInspectionVariantResultJSON(t *testing.T) {
	// Exercise the real preprocessing, variant grouping and JSON serialization
	// on an in-memory bitmap. Only the text recognizer is controlled by the test.
	script := inspectionHelperDefinitions(t) + `
$script:replyCalls = 0
$script:replyTitle = ''
$script:replyPage = New-Object PSObject
$script:replyPage | Add-Member -MemberType ScriptMethod -Name GetText -Value { return $script:replyTitle }
$script:replyPage | Add-Member -MemberType ScriptMethod -Name Dispose -Value { }
$fakeEngine = New-Object PSObject
$fakeEngine | Add-Member -MemberType ScriptMethod -Name Process -Value {
    param($pixels, $mode)
    $script:replyTitle = @('Primary title', 'Raw title', 'Adaptive title')[$script:replyCalls % 3]
    $script:replyCalls++
    return $script:replyPage
}
while ($null -ne ([Console]::In.ReadLine())) {
    $bitmap = New-Object Drawing.Bitmap(32, 16, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    try {
        $title = New-Object Drawing.Rectangle(0, 0, 32, 16)
        $recognized = Read-InspectionVariants $fakeEngine $bitmap ([Drawing.Rectangle[]]@($title)) 1.0 ([IntPtr]::Zero) $false
        $reply = @{lines=$recognized.Lines;groups=$recognized.Groups;titleVariants=$recognized.TitleVariants;languages=@('en','ru');error=$initializationError;active=$true;inspectOpen=$true;inspectCount=1;fingerprint='fixture'}
        [Console]::Out.WriteLine(($reply | ConvertTo-Json -Compress -Depth 3))
        [Console]::Out.Flush()
    } finally { $bitmap.Dispose() }
}
`
	ocr := New([]byte(script))
	t.Cleanup(func() { _ = ocr.Close() })
	result, err := ocr.Scan(context.Background(), "en", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.InspectCount != 1 || len(result.TitleVariants) != 1 || len(result.TitleVariants[0]) != 3 || len(result.Groups) != 3 {
		t.Fatalf("inspection variant JSON structure was lost: %+v", result)
	}
	for i, want := range []string{"Primary title", "Raw title", "Adaptive title"} {
		if result.TitleVariants[0][i] != want {
			t.Fatalf("variant %d = %q, want %q", i, result.TitleVariants[0][i], want)
		}
	}
}

func TestInspectionForegroundLossDiscardsPartialOCR(t *testing.T) {
	// Replace the foreground reader only in this child's in-memory test script.
	// No real window, desktop capture or foreground operation is involved.
	source := inspectionHelperDefinitions(t)
	before := `[DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();`
	after := `public static int ForegroundChecks; public static IntPtr GetForegroundWindow() { ForegroundChecks++; return ForegroundChecks <= 2 ? IntPtr.Zero : new IntPtr(1); }`
	if !strings.Contains(source, before) {
		t.Fatal("foreground test seam is missing")
	}
	script := strings.Replace(source, before, after, 1) + `
$script:replyCalls = 0
$script:replyPage = New-Object PSObject
$script:replyPage | Add-Member -MemberType ScriptMethod -Name GetText -Value { return 'Controlled title' }
$script:replyPage | Add-Member -MemberType ScriptMethod -Name Dispose -Value { }
$fakeEngine = New-Object PSObject
$fakeEngine | Add-Member -MemberType ScriptMethod -Name Process -Value {
    param($pixels, $mode)
    $script:replyCalls++
    return $script:replyPage
}
while ($null -ne ([Console]::In.ReadLine())) {
    $bitmap = New-Object Drawing.Bitmap(32, 16, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    try {
        $title = New-Object Drawing.Rectangle(0, 0, 32, 16)
        $recognized = Read-InspectionVariants $fakeEngine $bitmap ([Drawing.Rectangle[]]@($title)) 1.0 ([IntPtr]::Zero) $true
        $fixtureError = $initializationError
        if ($recognized.Completed -or $script:replyCalls -ne 1 -or $recognized.Lines.Count -ne 0 -or $recognized.Groups.Count -ne 0 -or $recognized.TitleVariants.Count -ne 0) { $fixtureError = 'Partial OCR escaped the foreground gate' }
        $reply = @{lines=$recognized.Lines;groups=$recognized.Groups;titleVariants=$recognized.TitleVariants;languages=@('en','ru');error=$fixtureError;active=$false;inspectOpen=$false;inspectCount=0;fingerprint=''}
        [Console]::Out.WriteLine(($reply | ConvertTo-Json -Compress -Depth 3))
        [Console]::Out.Flush()
    } finally { $bitmap.Dispose() }
}
`
	ocr := New([]byte(script))
	t.Cleanup(func() { _ = ocr.Close() })
	result, err := ocr.Scan(context.Background(), "en", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Active || result.InspectOpen || result.InspectCount != 0 || len(result.Lines) != 0 || len(result.Groups) != 0 || len(result.TitleVariants) != 0 {
		t.Fatalf("foreground loss retained a partial inspection result: %+v", result)
	}
}
