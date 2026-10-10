# In-memory geometric fixtures. This file never captures or activates a window.
# The scanner test loads it after the embedded helper's definitions.
function New-InspectionFixture([string]$kind, [double]$scale, [bool]$closeControl, [bool]$leftHandle = $false) {
    $bitmap = New-Object Drawing.Bitmap(800, 450, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    $pen = New-Object Drawing.Pen([Drawing.Color]::FromArgb(225, 225, 225), 1.5)
    $border = New-Object Drawing.Pen([Drawing.Color]::FromArgb(110, 110, 110), 1.0)
    $headerBrush = New-Object Drawing.SolidBrush([Drawing.Color]::FromArgb(24, 24, 24))
    $bodyBrush = New-Object Drawing.SolidBrush([Drawing.Color]::FromArgb(4, 4, 4))
    $redBrush = New-Object Drawing.SolidBrush([Drawing.Color]::FromArgb(70, 15, 15))
    $font = New-Object Drawing.Font('Arial', 11, [Drawing.FontStyle]::Regular, [Drawing.GraphicsUnit]::Pixel)
    try {
        $graphics.Clear([Drawing.Color]::FromArgb(15, 17, 18))
        $graphics.SmoothingMode = [Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $graphics.TextRenderingHint = [Drawing.Text.TextRenderingHint]::AntiAliasGridFit
        if ($kind -in @('inspect', 'header-without-marker', 'marker-without-frame', 'category-captions', 'toolbar-with-panel-edge', 'toolbar-with-borrowed-border', 'inspect-with-toolbar')) {
            $graphics.FillRectangle($bodyBrush, 50, 40, 640, 330)
            $graphics.FillRectangle($headerBrush, 50, 40, 640, 22)
            if ($kind -ne 'marker-without-frame') { $graphics.DrawRectangle($border, 50, 40, 640, 330) }
            if ($kind -eq 'category-captions') {
                # Letters with hollow or diagonal strokes must not act as a marker,
                # even when they sit beside a dark caption and a framed item panel.
                $graphics.DrawString('ARMBAND COVERS ON SLING SHEATH', $font, [Drawing.Brushes]::White, 65, 45)
            } else {
                $graphics.DrawString('Synthetic item inspection title', $font, [Drawing.Brushes]::White, 85, 45)
            }
            if ($closeControl) { $graphics.FillRectangle($redBrush, 666, 42, 20, 18) }
        } elseif ($kind -eq 'search') {
            $graphics.FillRectangle($headerBrush, 50, 40, 180, 22)
            $graphics.DrawRectangle($border, 50, 40, 180, 22)
            $graphics.DrawString('SEARCH', $font, [Drawing.Brushes]::White, 72, 45)
        } else {
            $graphics.DrawString('Inventory currency and unrelated item names', $font, [Drawing.Brushes]::White, 50, 45)
        }
        if ($kind -in @('toolbar-with-panel-edge', 'toolbar-with-borrowed-border', 'inspect-with-toolbar')) {
            # A body action can contain a real magnifier. Its nearby panel edge
            # or unrelated borders below it must not establish another title.
            $toolbarX = 50
            $markerX = 65
            if ($kind -ne 'toolbar-with-panel-edge') {
                $toolbarX = 100
                $markerX = 115
                $graphics.DrawLine($border, $toolbarX, 168, $toolbarX, 235)
            }
            $graphics.FillRectangle($headerBrush, ($toolbarX + 1), 145, (689 - $toolbarX), 22)
            $graphics.DrawString('Synthetic body action', $font, [Drawing.Brushes]::White, ($markerX + 20), 150)
            if ($leftHandle) {
                $graphics.DrawEllipse($pen, ($markerX + 3), 150, 9, 9)
                $graphics.DrawLine($pen, ($markerX + 4), 158, $markerX, 162)
            } else {
                $graphics.DrawEllipse($pen, $markerX, 150, 9, 9)
                $graphics.DrawLine($pen, ($markerX + 8), 158, ($markerX + 12), 162)
            }
            if ($closeControl) { $graphics.FillRectangle($redBrush, 666, 147, 20, 18) }
        }
        if ($kind -in @('inspect', 'search', 'marker-without-frame', 'isolated-marker', 'inspect-with-toolbar')) {
            $x = 65
            if ($kind -eq 'search') { $x = 55 }
            if ($leftHandle) {
                $graphics.DrawEllipse($pen, ($x + 3), 45, 9, 9)
                $graphics.DrawLine($pen, ($x + 4), 53, $x, 57)
            } else {
                $graphics.DrawEllipse($pen, $x, 45, 9, 9)
                $graphics.DrawLine($pen, ($x + 8), 53, ($x + 12), 57)
            }
        }
        $graphics.Dispose()
        $graphics = $null
        return [TarkovPilotCapture.Native]::PrepareText($bitmap, [int]($bitmap.Width * $scale), [int]($bitmap.Height * $scale), $false)
    } finally {
        foreach ($resource in @($graphics, $pen, $border, $headerBrush, $bodyBrush, $redBrush, $font, $bitmap)) {
            if ($null -ne $resource) { $resource.Dispose() }
        }
    }
}

function Test-InspectionGeometry {
    $failures = New-Object 'System.Collections.Generic.List[string]'
    foreach ($scale in @(0.75, 1.0, 1.25, 1.5, 2.0)) {
        foreach ($kind in @('inspect', 'search', 'inventory', 'header-without-marker', 'marker-without-frame', 'isolated-marker', 'category-captions', 'toolbar-with-panel-edge', 'toolbar-with-borrowed-border', 'inspect-with-toolbar')) {
            foreach ($closeControl in @($false, $true)) {
                foreach ($leftHandle in @($false, $true)) {
                    $bitmap = New-InspectionFixture $kind $scale $closeControl $leftHandle
                    try {
                        $found = [TarkovPilotCapture.InspectionDetector]::Detect($bitmap)
                        $expected = 0
                        if ($kind -in @('inspect', 'inspect-with-toolbar')) { $expected = 1 }
                        if ($found.Count -ne $expected) { $failures.Add(('{0}, scale={1}, close={2}, leftHandle={3}, expected={4}, detected={5}' -f $kind, $scale, $closeControl, $leftHandle, $expected, $found.Count)) }
                        if ($found.Count -gt 0) {
                            $first = [TarkovPilotCapture.InspectionDetector]::Fingerprint($bitmap, $found)
                            $second = [TarkovPilotCapture.InspectionDetector]::Fingerprint($bitmap, $found)
                            if ($first.Length -ne 64 -or $first -ne $second) { $failures.Add('Unstable title fingerprint') }
                            # A detected title must retain all of its bright letter
                            # strokes, including the lowest descender and last glyph.
                            $clipped = $false
                            for ($y = [int](44 * $scale); $y -lt [int](62 * $scale); $y++) {
                                for ($x = [int](84 * $scale); $x -lt [int](500 * $scale); $x++) {
                                    $pixel = $bitmap.GetPixel($x, $y)
                                    if ($pixel.R -ge 150 -and $pixel.G -ge 150 -and $pixel.B -ge 150 -and -not $found[0].Contains($x, $y)) { $clipped = $true }
                                }
                            }
                            if ($clipped) { $failures.Add(('Title ink clipped, scale={0}, leftHandle={1}' -f $scale, $leftHandle)) }
                        }
                    } finally { $bitmap.Dispose() }
                }
            }
        }
    }
    foreach ($failure in @(Test-TitleVariantProtocol)) { $failures.Add($failure) }
    return $failures.ToArray()
}

function Test-TitleVariantProtocol {
    $failures = New-Object 'System.Collections.Generic.List[string]'
    $script:fixtureOcrCalls = 0
    $script:fixtureDistinct = $false
    $script:fixtureText = 'L'
    $fakePage = New-Object PSObject
    $fakePage | Add-Member -MemberType ScriptMethod -Name GetText -Value { return $script:fixtureText }
    $fakePage | Add-Member -MemberType ScriptMethod -Name Dispose -Value { }
    $script:fixturePage = $fakePage
    $fakeEngine = New-Object PSObject
    $fakeEngine | Add-Member -MemberType ScriptMethod -Name Process -Value {
        param($pixels, $mode)
        $script:fixtureOcrCalls++
        $script:fixtureText = 'L'
        if ($script:fixtureDistinct) { $script:fixtureText = @('L', 'R', 'A')[($script:fixtureOcrCalls - 1) % 3] }
        return $script:fixturePage
    }
    $bitmap = New-Object Drawing.Bitmap(32, 16, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $title = New-Object Drawing.Rectangle(0, 0, 32, 16)
    try {
        $recognized = Read-InspectionVariants $fakeEngine $bitmap ([Drawing.Rectangle[]]@($title)) 1.0 ([IntPtr]::Zero) $false
        $reply = @{ titleVariants = $recognized.TitleVariants; inspectCount = 1 }
        $serialized = $reply | ConvertTo-Json -Compress -Depth 3
        if ($serialized -notmatch '"titleVariants":\[\["L"\]\]' -or $script:fixtureOcrCalls -ne 3) {
            $failures.Add('Single-character title or single-window variant array was lost')
        }
        $script:fixtureDistinct = $true
        $recognized = Read-InspectionVariants $fakeEngine $bitmap ([Drawing.Rectangle[]]@($title)) 1.0 ([IntPtr]::Zero) $false
        $reply = @{ titleVariants = $recognized.TitleVariants; inspectCount = 1 }
        $serialized = $reply | ConvertTo-Json -Compress -Depth 3
        if ($serialized -notmatch '"titleVariants":\[\["L","R","A"\]\]' -or $recognized.Groups.Count -ne 3) {
            $failures.Add('Single-window OCR variants were not preserved as three complete titles')
        }
        $script:fixtureDistinct = $false
        $recognized = Read-InspectionVariants $fakeEngine $bitmap ([Drawing.Rectangle[]]@($title, $title)) 1.0 ([IntPtr]::Zero) $false
        $reply = @{ titleVariants = $recognized.TitleVariants; inspectCount = 2 }
        $serialized = $reply | ConvertTo-Json -Compress -Depth 3
        if ($serialized -notmatch '"titleVariants":\[\["L"\],\["L"\]\]') { $failures.Add('Separate inspection title arrays were merged') }
        $before = $script:fixtureOcrCalls
        $recognized = Read-InspectionVariants $fakeEngine $bitmap ([Drawing.Rectangle[]]@()) 1.0 ([IntPtr]::Zero) $false
        if ($script:fixtureOcrCalls -ne $before -or $recognized.TitleVariants.Count -ne 0) { $failures.Add('Absent inspection invoked OCR') }
    } finally {
        $bitmap.Dispose()
        Remove-Variable -Name fixturePage, fixtureOcrCalls, fixtureDistinct, fixtureText -Scope Script -ErrorAction SilentlyContinue
    }
    return $failures.ToArray()
}
