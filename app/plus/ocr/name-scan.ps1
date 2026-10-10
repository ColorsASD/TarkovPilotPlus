# The embedded helper runs in Windows PowerShell 5.1 (.NET Framework).
# It accepts one JSON scan request per stdin line and returns one JSON result.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
# The bootstrap sets input encoding before reading the script. Resetting it here
# would discard a buffered scan request already received on stdin.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)

$initializationError = ''
$recognizerLanguages = @()
$engines = @{}
try {
    Add-Type -AssemblyName System.Drawing
    Add-Type -AssemblyName System.Windows.Forms
    $runtimeDirectory = $env:TARKOVPILOT_OCR_DIR
    if (-not $runtimeDirectory -or -not (Test-Path -LiteralPath (Join-Path $runtimeDirectory 'Tesseract.dll'))) {
        throw 'The bundled Tesseract engine is missing. Copy the ocr folder alongside the application too.'
    }
    [void][System.Reflection.Assembly]::LoadFrom((Join-Path $runtimeDirectory 'Tesseract.dll'))
    [Tesseract.TesseractEnviornment]::CustomSearchPath = $runtimeDirectory
    $modelDirectory = Join-Path $runtimeDirectory 'tessdata'
    # Scope these settings to the helper. No model or item data is downloaded at runtime.
    $env:TESSDATA_PREFIX = $modelDirectory
    $env:OMP_THREAD_LIMIT = '2'
    if (Test-Path -LiteralPath (Join-Path $modelDirectory 'eng.traineddata')) { $recognizerLanguages += 'en' }
    if (Test-Path -LiteralPath (Join-Path $modelDirectory 'rus.traineddata')) { $recognizerLanguages += 'ru' }
    if ('en' -notin $recognizerLanguages) { throw 'The English OCR language model is missing.' }
    $engines['en'] = New-Object Tesseract.TesseractEngine($modelDirectory, 'eng', [Tesseract.EngineMode]::LstmOnly)
    [void]$engines['en'].SetVariable('user_defined_dpi', 150)

    Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
namespace TarkovPilotCapture {
    public static class Native {
        [StructLayout(LayoutKind.Sequential)]
        public struct Rect { public int Left, Top, Right, Bottom; }
        [StructLayout(LayoutKind.Sequential)]
        public struct Point { public int X, Y; }
        private delegate bool WindowVisitor(IntPtr window, IntPtr parameter);
        [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
        [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);
        [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr window);
        [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr window);
        [DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr window, out Rect rectangle);
        [DllImport("user32.dll")] public static extern bool ClientToScreen(IntPtr window, ref Point point);
        [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(Point point);
        [DllImport("user32.dll")] public static extern bool GetCursorPos(out Point point);
        [DllImport("user32.dll")] private static extern bool EnumWindows(WindowVisitor visitor, IntPtr parameter);
        [DllImport("user32.dll")] private static extern bool SetProcessDpiAwarenessContext(IntPtr value);
        [DllImport("user32.dll")] private static extern bool SetProcessDPIAware();

        public static void UsePhysicalPixels() {
            try { SetProcessDpiAwarenessContext(new IntPtr(-4)); }
            catch (EntryPointNotFoundException) { SetProcessDPIAware(); }
        }
        public static bool IsGameWindow(IntPtr window) {
            if (window == IntPtr.Zero || !IsWindowVisible(window) || IsIconic(window)) return false;
            uint processId;
            GetWindowThreadProcessId(window, out processId);
            try {
                using (var process = Process.GetProcessById((int)processId)) {
                    return string.Equals(process.ProcessName, "EscapeFromTarkov", StringComparison.OrdinalIgnoreCase);
                }
            } catch { return false; }
        }
        public static IntPtr FindGameWindow(bool automatic) {
            var foreground = GetForegroundWindow();
            if (IsGameWindow(foreground)) return foreground;
            if (automatic) return IntPtr.Zero;
            var found = IntPtr.Zero;
            EnumWindows(delegate(IntPtr window, IntPtr parameter) {
                if (!IsGameWindow(window)) return true;
                found = window;
                return false;
            }, IntPtr.Zero);
            return found;
        }
        public static bool IsGamePoint(int x, int y, IntPtr gameWindow) {
            uint gameId, pointId;
            GetWindowThreadProcessId(gameWindow, out gameId);
            GetWindowThreadProcessId(WindowFromPoint(new Point { X = x, Y = y }), out pointId);
            return gameId != 0 && pointId == gameId;
        }
        public static Bitmap PrepareText(Bitmap source, int width, int height, bool threshold) {
            return PrepareText(source, width, height, threshold, 145);
        }
        public static Bitmap PrepareText(Bitmap source, int width, int height, bool threshold, int thresholdValue) {
            var bitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb);
            using (var graphics = Graphics.FromImage(bitmap)) {
                graphics.InterpolationMode = InterpolationMode.HighQualityBicubic;
                graphics.DrawImage(source, 0, 0, width, height);
            }
            if (!threshold) return bitmap;
            var data = bitmap.LockBits(new Rectangle(0, 0, width, height), ImageLockMode.ReadWrite, PixelFormat.Format32bppArgb);
            try {
                var pixels = new byte[data.Stride * height];
                Marshal.Copy(data.Scan0, pixels, 0, pixels.Length);
                for (var y = 0; y < height; y++) {
                    for (var x = 0; x < width; x++) {
                        var offset = y * data.Stride + x * 4;
                        var gray = (pixels[offset + 2] * 299 + pixels[offset + 1] * 587 + pixels[offset] * 114) / 1000;
                        var value = (byte)(gray >= thresholdValue ? 0 : 255);
                        pixels[offset] = value;
                        pixels[offset + 1] = value;
                        pixels[offset + 2] = value;
                        pixels[offset + 3] = 255;
                    }
                }
                Marshal.Copy(pixels, 0, data.Scan0, pixels.Length);
            } finally { bitmap.UnlockBits(data); }
            return bitmap;
        }
        public static Bitmap AddTitleBorder(Bitmap source, bool threshold) {
            var bitmap = new Bitmap(source.Width + 20, source.Height + 20, PixelFormat.Format32bppArgb);
            using (var graphics = Graphics.FromImage(bitmap)) {
                graphics.Clear(threshold ? System.Drawing.Color.White : source.GetPixel(0, 0));
                graphics.DrawImageUnscaled(source, 10, 10);
            }
            return bitmap;
        }
    }

    // Independent glyph geometry: a hollow circle with a diagonal handle.
    // No game asset, third-party template or OCR is used by this detector.
    public static class InspectionDetector {
        private sealed class Pixels {
            public readonly int Width, Height;
            public readonly byte[] Gray, Bright;
            public readonly byte[] Color;
            public Pixels(Bitmap bitmap) {
                Width = bitmap.Width; Height = bitmap.Height;
                Gray = new byte[Width * Height]; Bright = new byte[Gray.Length];
                var data = bitmap.LockBits(new Rectangle(0, 0, Width, Height), ImageLockMode.ReadOnly, PixelFormat.Format32bppArgb);
                try {
                    Color = new byte[Width * Height * 4];
                    for (int y = 0; y < Height; y++) {
                        Marshal.Copy(IntPtr.Add(data.Scan0, y * data.Stride), Color, y * Width * 4, Width * 4);
                    }
                    for (int i = 0; i < Gray.Length; i++) {
                        int p = i * 4, b = Color[p], g = Color[p + 1], r = Color[p + 2];
                        Gray[i] = (byte)((r * 3 + g * 6 + b) / 10);
                        if (Gray[i] >= 100 && Math.Max(r, Math.Max(g, b)) - Math.Min(r, Math.Min(g, b)) < 80) Bright[i] = 1;
                    }
                } finally { bitmap.UnlockBits(data); }
            }
            public int At(int x, int y) { return x < 0 || y < 0 || x >= Width || y >= Height ? 255 : Gray[y * Width + x]; }
            public bool Ink(int x, int y, int radius) {
                for (int dy = -radius; dy <= radius; dy++) for (int dx = -radius; dx <= radius; dx++) {
                    int xx = x + dx, yy = y + dy;
                    if (xx >= 0 && yy >= 0 && xx < Width && yy < Height && Bright[yy * Width + xx] != 0) return true;
                }
                return false;
            }
            public bool Red(int x, int y) {
                if (x < 0 || y < 0 || x >= Width || y >= Height) return false;
                int p = (y * Width + x) * 4;
                int b = Color[p], g = Color[p + 1], r = Color[p + 2];
                return r >= 35 && r > g * 2 && r > b * 2;
            }
        }
        private static bool GlyphInk(Pixels p, Rectangle box, int x, int y, int radius) {
            for (int yy = Math.Max(box.Top, y - radius); yy <= Math.Min(box.Bottom - 1, y + radius); yy++)
                for (int xx = Math.Max(box.Left, x - radius); xx <= Math.Min(box.Right - 1, x + radius); xx++)
                    if (p.Bright[yy * p.Width + xx] != 0) return true;
            return false;
        }
        private static bool MagnifierDirection(Pixels p, Rectangle box, bool leftHandle) {
            // Accept either diagonal handle direction. The two empty corners
            // distinguish a magnifier from letters such as O, R and H.
            int empty = 0, cornerWidth = Math.Max(1, box.Width / 4);
            for (int y = box.Top; y < box.Top + box.Height / 2; y++) {
                for (int n = 0; n < cornerWidth; n++) {
                    int x = leftHandle ? box.Left + n : box.Right - 1 - n;
                    if (p.Bright[y * p.Width + x] != 0) empty++;
                }
            }
            for (int y = box.Top + box.Height * 3 / 4; y < box.Bottom; y++) {
                for (int n = 0; n < cornerWidth; n++) {
                    int x = leftHandle ? box.Right - 1 - n : box.Left + n;
                    if (p.Bright[y * p.Width + x] != 0) empty++;
                }
            }
            if (empty > Math.Max(2, box.Width / 4)) return false;
            double diameter = Math.Min(box.Width, box.Height) * 0.75;
            double cx = leftHandle ? box.Right - 1 - (diameter - 1) / 2 : box.X + (diameter - 1) / 2;
            double cy = box.Y + (diameter - 1) / 2;
            double radius = Math.Max(2, (diameter - 1) / 2);
            if (p.At((int)Math.Round(cx), (int)Math.Round(cy)) >= 95) return false;
            int hits = 0, tolerance = box.Width >= 20 ? 2 : 1;
            for (int i = 0; i < 8; i++) {
                double angle = i * Math.PI / 4;
                if (GlyphInk(p, box, (int)Math.Round(cx + radius * Math.Cos(angle)), (int)Math.Round(cy + radius * Math.Sin(angle)), tolerance)) hits++;
            }
            if (hits < 7) return false;
            int handleX = box.X + (int)(box.Width * (leftHandle ? 0.15 : 0.85));
            return GlyphInk(p, box, handleX, box.Y + (int)(box.Height * 0.85), tolerance);
        }
        private static bool IsMagnifier(Pixels p, Rectangle box, int count) {
            if (box.Width < 6 || box.Height < 6 || box.Width > 52 || box.Height > 52) return false;
            if (box.Width / (double)box.Height < 0.8 || box.Width / (double)box.Height > 1.25) return false;
            if (count < 12 || count > box.Width * box.Height * 0.5) return false;
            return MagnifierDirection(p, box, true) || MagnifierDirection(p, box, false);
        }
        private static int FrameLeft(Pixels p, Rectangle icon, int divider) {
            int size = Math.Max(icon.Width, icon.Height);
            for (int x = icon.X - Math.Max(4, size * 2 / 3); x >= Math.Max(2, icon.X - size * 2); x--) {
                // The same panel edge must run beside the header. Sampling only
                // below a button can accidentally borrow nearby toolbar borders.
                int headerHits = 0, headerSamples = 0;
                for (int y = icon.Top; y < icon.Bottom; y++) {
                    headerSamples++;
                    int middle = p.At(x, y), inside = p.At(x + 2, y);
                    if (middle >= 30 && middle - inside >= 12) headerHits++;
                }
                if (headerHits < headerSamples * 0.75) continue;
                int hits = 0;
                for (int n = 1; n <= 6; n++) {
                    int y = divider + n * Math.Max(3, size / 2);
                    if (y >= p.Height) continue;
                    int middle = p.At(x, y), inside = p.At(x + 2, y);
                    if (middle >= 30 && middle - inside >= 12) hits++;
                }
                if (hits >= 4) return x;
            }
            return -1;
        }
        private static bool HasPanelTop(Pixels p, Rectangle icon, int frameLeft) {
            int size = Math.Max(icon.Width, icon.Height);
            int end = Math.Min(p.Width - 2, icon.Right + size * 8);
            // A title belongs immediately below the panel's top border. A
            // magnifier inside its body or toolbar is not another inspection.
            for (int y = Math.Max(0, icon.Top - size); y < icon.Top; y++) {
                int hits = 0, samples = 0;
                for (int x = frameLeft + 1; x < end; x += Math.Max(1, size / 3)) {
                    samples++;
                    int edge = p.At(x, y), inside = p.At(x, y + 2);
                    // A subpixel top border loses brightness when downsized;
                    // its contrast with the dark header still remains visible.
                    if (edge >= 25 && edge - inside >= 12) hits++;
                }
                if (samples >= 12 && hits >= samples * 0.80) return true;
            }
            return false;
        }
        private static Rectangle FindTitle(Pixels p, Rectangle icon) {
            int size = Math.Max(icon.Width, icon.Height);
            int start = icon.Right + Math.Max(2, size / 7);
            if (start + size * 8 >= p.Width) return Rectangle.Empty;
            // A real inspection header has a long horizontal boundary above its body.
            // A search input or an isolated magnifier does not supply the dialog frame.
            for (int y = icon.Bottom + 1; y <= Math.Min(p.Height - 4, icon.Y + size * 3); y++) {
                int good = 0, samples = 0, end = start;
                int step = Math.Max(2, size / 3);
                for (int x = start; x < Math.Min(p.Width - 2, start + size * 85); x += step) {
                    int before = p.At(x, y - 2), after = p.At(x, y + 2);
                    bool boundary = before <= 110 && after <= 120 && Math.Abs(before - after) >= 5;
                    samples++;
                    if (boundary) good++;
                    if (samples >= 12 && good < samples * 0.65) break;
                    if (good >= samples * 0.65) end = x;
                }
                if (end - start < size * 8) continue;
                int frameLeft = FrameLeft(p, icon, y);
                if (frameLeft < 0 || !HasPanelTop(p, icon, frameLeft)) continue;
                int top = Math.Max(0, icon.Y - Math.Max(1, size / 5));
                // Keep the final antialiased row and letter descenders. A boundary
                // sample can fall within the last text row after UI rescaling.
                int bottom = Math.Min(p.Height, y + 1);
                if (bottom - top - 2 < 6 || bottom - top - 2 > size * 1.45) continue;
                int dark = 0, checkedPixels = 0;
                for (int yy = top; yy < bottom; yy += 3) for (int xx = start; xx < end; xx += 5) {
                    checkedPixels++; if (p.At(xx, yy) <= 100) dark++;
                }
                if (checkedPixels == 0 || dark < checkedPixels * 0.70) continue;
                // The boundary establishes the header, but is not the text's right
                // edge: a dark body detail can interrupt its contrast mid-title.
                end = Math.Min(p.Width - 2, Math.Max(end, start + size * 50));
                // The red close control, when visible, refines the right edge.
                int centerY = icon.Y + icon.Height / 2;
                for (int x = start + size * 8; x < end; x++) {
                    if (p.Red(x, centerY) && p.Red(x + 2, centerY)) { end = x - Math.Max(2, size / 4); break; }
                }
                // Stop at the end of contiguous title ink, excluding distant toolbar buttons.
                int firstInk = -1, lastInk = -1, ink = 0;
                for (int x = start; x < end; x++) {
                    bool column = false;
                    for (int yy = top; yy < bottom; yy++) if (p.Ink(x, yy, 0)) { column = true; ink++; }
                    if (column) {
                        if (firstInk < 0) firstInk = x;
                        lastInk = x;
                    } else if (lastInk >= 0 && x - lastInk > size * 3) break;
                }
                if (firstInk < 0 || firstInk - start > size * 2 || ink < size * 2 || lastInk - firstInk < size * 2) continue;
                return new Rectangle(Math.Max(start, firstInk - 2), top, Math.Min(end, lastInk + 3) - Math.Max(start, firstInk - 2), bottom - top);
            }
            return Rectangle.Empty;
        }
        public static Rectangle[] Detect(Bitmap source) {
            double scale = Math.Min(1.0, 2560.0 / Math.Max(source.Width, source.Height));
            using (var bitmap = Native.PrepareText(source, Math.Max(1, (int)(source.Width * scale)), Math.Max(1, (int)(source.Height * scale)), false)) {
                var p = new Pixels(bitmap);
                var visited = new byte[p.Gray.Length];
                var queue = new Queue<int>();
                var titles = new List<Rectangle>();
                for (int index = 0; index < p.Bright.Length; index++) {
                    if (visited[index] != 0 || p.Bright[index] == 0) continue;
                    visited[index] = 1; queue.Enqueue(index);
                    int left = index % p.Width, right = left, top = index / p.Width, bottom = top, count = 0;
                    while (queue.Count != 0) {
                        int cell = queue.Dequeue(), x = cell % p.Width, y = cell / p.Width;
                        left = Math.Min(left, x); right = Math.Max(right, x); top = Math.Min(top, y); bottom = Math.Max(bottom, y); count++;
                        for (int yy = Math.Max(0, y - 1); yy <= Math.Min(p.Height - 1, y + 1); yy++) for (int xx = Math.Max(0, x - 1); xx <= Math.Min(p.Width - 1, x + 1); xx++) {
                            int next = yy * p.Width + xx;
                            if (visited[next] == 0 && p.Bright[next] != 0) { visited[next] = 1; queue.Enqueue(next); }
                        }
                    }
                    var icon = Rectangle.FromLTRB(left, top, right + 1, bottom + 1);
                    if (!IsMagnifier(p, icon, count)) continue;
                    var title = FindTitle(p, icon);
                    if (title.IsEmpty) continue;
                    var physical = Rectangle.FromLTRB((int)Math.Floor(title.Left / scale), (int)Math.Floor(title.Top / scale), (int)Math.Ceiling(title.Right / scale), (int)Math.Ceiling(title.Bottom / scale));
                    physical.Intersect(new Rectangle(0, 0, source.Width, source.Height));
                    bool duplicate = false;
                    foreach (var old in titles) if (!Rectangle.Intersect(old, physical).IsEmpty) { duplicate = true; break; }
                    if (!duplicate && titles.Count < 8) titles.Add(physical);
                }
                titles.Sort(delegate(Rectangle a, Rectangle b) { int row = a.Y.CompareTo(b.Y); return row != 0 ? row : a.X.CompareTo(b.X); });
                return titles.ToArray();
            }
        }
        public static string Fingerprint(Bitmap source, Rectangle[] titles) {
            using (var digest = SHA256.Create()) {
                var bits = new List<byte>();
                foreach (var title in titles) {
                    using (var crop = source.Clone(title, PixelFormat.Format32bppArgb))
                    using (var normalized = Native.PrepareText(crop, 240, 20, true)) {
                        var pixels = new Pixels(normalized);
                        bits.AddRange(pixels.Gray);
                    }
                }
                return BitConverter.ToString(digest.ComputeHash(bits.ToArray())).Replace("-", "").ToLowerInvariant();
            }
        }
    }
}
'@ -ReferencedAssemblies 'System.Drawing', 'System'
    [TarkovPilotCapture.Native]::UsePhysicalPixels()
} catch {
    $initializationError = 'The local Tesseract text recognizer cannot be started. Check the ocr folder copied alongside the application and the Microsoft Visual C++ 2015–2022 x64 runtime.'
}

function Get-GameInspection([IntPtr]$window, [bool]$automatic) {
    $rect = New-Object TarkovPilotCapture.Native+Rect
    $origin = New-Object TarkovPilotCapture.Native+Point
    if (-not [TarkovPilotCapture.Native]::GetClientRect($window, [ref]$rect) -or
        -not [TarkovPilotCapture.Native]::ClientToScreen($window, [ref]$origin)) {
        throw 'The game window size cannot be read.'
    }
    $bounds = New-Object System.Drawing.Rectangle($origin.X, $origin.Y, ($rect.Right - $rect.Left), ($rect.Bottom - $rect.Top))
    $bounds = [System.Drawing.Rectangle]::Intersect($bounds, [System.Windows.Forms.SystemInformation]::VirtualScreen)
    if ($bounds.Width -lt 32 -or $bounds.Height -lt 32) { throw 'The game window is currently not visible.' }
    if ($bounds.Width -gt 16384 -or $bounds.Height -gt 16384 -or [long]$bounds.Width * $bounds.Height -gt 67108864) {
        throw 'The game window is too large for local item scanning.'
    }
    $bitmap = $null
    $graphics = $null
    try {
        if ($automatic -and [TarkovPilotCapture.Native]::GetForegroundWindow() -ne $window) { return $null }
        $bitmap = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
        $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
        $graphics.CopyFromScreen($bounds.X, $bounds.Y, 0, 0, $bounds.Size, [System.Drawing.CopyPixelOperation]::SourceCopy)
        $graphics.Dispose()
        $graphics = $null
        if ($automatic -and [TarkovPilotCapture.Native]::GetForegroundWindow() -ne $window) { return $null }
        $titles = @([TarkovPilotCapture.InspectionDetector]::Detect($bitmap) | Where-Object {
            [TarkovPilotCapture.Native]::IsGamePoint(($bounds.X + $_.X + [int]($_.Width / 2)), ($bounds.Y + $_.Y + [int]($_.Height / 2)), $window)
        })
        $fingerprint = ''
        if ($titles.Count -gt 0) { $fingerprint = [TarkovPilotCapture.InspectionDetector]::Fingerprint($bitmap, [System.Drawing.Rectangle[]]$titles) }
        if ($automatic -and [TarkovPilotCapture.Native]::GetForegroundWindow() -ne $window) { return $null }
        $gameScale = [Math]::Max(0.25, [Math]::Min(($rect.Right - $rect.Left) / 1920.0, ($rect.Bottom - $rect.Top) / 1080.0))
        $result = @{ Bitmap = $bitmap; Titles = $titles; Fingerprint = $fingerprint; GameScale = $gameScale }
        $bitmap = $null
        return $result
    } finally {
        if ($null -ne $graphics) { $graphics.Dispose() }
        if ($null -ne $bitmap) { $bitmap.Dispose() }
    }
}

function Get-Engine([string]$language) {
    if (-not $engines.ContainsKey($language)) {
        if ($language -notin $recognizerLanguages) {
            throw ('The bundled OCR language model is missing: ' + $language + '. Copy the entire ocr folder alongside the application.')
        }
        $model = 'eng'
        if ($language -eq 'ru') { $model = 'rus+eng' }
        $engines[$language] = New-Object Tesseract.TesseractEngine($modelDirectory, $model, [Tesseract.EngineMode]::LstmOnly)
        [void]$engines[$language].SetVariable('user_defined_dpi', 150)
    }
    return $engines[$language]
}

function Read-TitleText($engine, [System.Drawing.Bitmap]$bitmap, [System.Drawing.Rectangle]$title, [bool]$threshold, [double]$gameScale = 1.0, [bool]$rawLine = $false, [bool]$adaptive = $false) {
    $crop = $null
    $prepared = $null
    $bordered = $null
    $memory = $null
    $pix = $null
    $page = $null
    try {
        $crop = $bitmap.Clone($title, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
        # Keep the recognised font size stable across game resolutions. A second
        # colour variant also handles UI scaling that differs from the resolution.
        $scale = [Math]::Min(6.0, [Math]::Max(0.5, 2.0 / [Math]::Max(0.25, $gameScale)))
        if ($adaptive) { $scale = [Math]::Min(4.0, [Math]::Max(2.0, 36.0 / $crop.Height)) }
        $scale = [Math]::Min($scale, 2400.0 / $crop.Width)
        $prepared = [TarkovPilotCapture.Native]::PrepareText($crop, [Math]::Max(1, [int][Math]::Floor($crop.Width * $scale)), [Math]::Max(1, [int][Math]::Floor($crop.Height * $scale)), $threshold, 120)
        $source = $prepared
        if (-not $rawLine) {
            $bordered = [TarkovPilotCapture.Native]::AddTitleBorder($prepared, $threshold)
            $source = $bordered
        }
        $memory = New-Object System.IO.MemoryStream
        $source.Save($memory, [System.Drawing.Imaging.ImageFormat]::Png)
        $pix = [Tesseract.Pix]::LoadFromMemory($memory.ToArray())
        # The geometric gate has already isolated the single inspection title.
        $mode = [Tesseract.PageSegMode]::SingleLine
        if ($rawLine) { $mode = [Tesseract.PageSegMode]::RawLine }
        $page = $engine.Process($pix, $mode)
        return @([Regex]::Split($page.GetText().Trim(), '\r?\n') | ForEach-Object { $_.Trim() } | Where-Object { $_.Length -gt 0 -and $_.Length -le 512 })
    } finally {
        if ($null -ne $page) { $page.Dispose() }
        if ($null -ne $pix) { $pix.Dispose() }
        if ($null -ne $memory) { $memory.Dispose() }
        if ($null -ne $bordered) { $bordered.Dispose() }
        if ($null -ne $prepared) { $prepared.Dispose() }
        if ($null -ne $crop) { $crop.Dispose() }
    }
}

function Read-InspectionVariants($engine, [System.Drawing.Bitmap]$bitmap, [System.Drawing.Rectangle[]]$titles, [double]$gameScale, [IntPtr]$window, [bool]$automatic) {
    $groups = New-Object 'System.Collections.Generic.List[object]'
    $titleVariants = New-Object 'System.Collections.Generic.List[object]'
    $lines = New-Object 'System.Collections.Generic.HashSet[string]'
    foreach ($title in $titles) {
        if ($automatic -and [TarkovPilotCapture.Native]::GetForegroundWindow() -ne $window) {
            return @{ Lines = @(); Groups = @(); TitleVariants = @(); Completed = $false }
        }
        $variants = New-Object 'System.Collections.Generic.List[string]'
        foreach ($recipe in @(
            @{ Threshold = $false; Raw = $false; Adaptive = $false },
            @{ Threshold = $true; Raw = $true; Adaptive = $false },
            @{ Threshold = $false; Raw = $false; Adaptive = $true }
        )) {
            if ($automatic -and [TarkovPilotCapture.Native]::GetForegroundWindow() -ne $window) {
                return @{ Lines = @(); Groups = @(); TitleVariants = @(); Completed = $false }
            }
            $titleLines = [string[]]@(Read-TitleText $engine $bitmap $title $recipe.Threshold $gameScale $recipe.Raw $recipe.Adaptive)
            $groups.Add($titleLines)
            $fullTitle = ($titleLines -join ' ').Trim()
            if ($fullTitle -and -not $variants.Contains($fullTitle)) { $variants.Add($fullTitle) }
            foreach ($line in $titleLines) { [void]$lines.Add($line) }
        }
        $titleVariants.Add([string[]]$variants.ToArray())
    }
    if ($automatic -and [TarkovPilotCapture.Native]::GetForegroundWindow() -ne $window) {
        return @{ Lines = @(); Groups = @(); TitleVariants = @(); Completed = $false }
    }
    return @{ Lines = @($lines); Groups = $groups.ToArray(); TitleVariants = $titleVariants.ToArray(); Completed = $true }
}

while ($null -ne ($requestLine = [Console]::In.ReadLine())) {
    $response = @{ lines = @(); groups = @(); titleVariants = @(); languages = $recognizerLanguages; error = ''; active = $false; inspectOpen = $false; inspectCount = 0; fingerprint = '' }
    $inspection = $null
    try {
        if ($initializationError) { throw $initializationError }
        $request = $requestLine | ConvertFrom-Json
        $operation = [string]$request.operation
        if (-not $operation) { $operation = 'scan' }
        if ($operation -notin @('probe', 'scan')) { throw 'Unknown item scanning operation.' }
        $language = [string]$request.language
        if ($operation -eq 'scan' -and $language -notin @('en', 'ru')) { throw 'The supported recognition languages are English and Russian.' }
        $automatic = [bool]$request.automatic
        $window = [TarkovPilotCapture.Native]::FindGameWindow($automatic)
        if ($window -ne [IntPtr]::Zero) {
            $inspection = Get-GameInspection $window $automatic
            if ($null -ne $inspection) {
                $response.active = $true
                $response.inspectOpen = $inspection.Titles.Count -gt 0
                $response.inspectCount = $inspection.Titles.Count
                $response.fingerprint = $inspection.Fingerprint
                if ($operation -eq 'scan' -and $response.inspectOpen) {
                    $engine = Get-Engine $language
                    $recognized = Read-InspectionVariants $engine $inspection.Bitmap ([System.Drawing.Rectangle[]]$inspection.Titles) $inspection.GameScale $window $automatic
                    $response.lines = $recognized.Lines
                    $response.groups = $recognized.Groups
                    $response.titleVariants = $recognized.TitleVariants
                    if (-not $recognized.Completed) {
                        $response.active = $false
                        $response.inspectOpen = $false
                        $response.inspectCount = 0
                        $response.fingerprint = ''
                    }
                }
            }
        } elseif (-not $automatic) {
            $response.error = 'No visible Escape from Tarkov game window was found. Open the game in windowed or borderless mode.'
        }
    } catch {
        $response.error = $_.Exception.Message
    } finally {
        if ($null -ne $inspection -and $null -ne $inspection.Bitmap) { $inspection.Bitmap.Dispose() }
    }
    [Console]::Out.WriteLine(($response | ConvertTo-Json -Compress -Depth 3))
    [Console]::Out.Flush()
}
foreach ($engine in $engines.Values) { $engine.Dispose() }
