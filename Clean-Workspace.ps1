# This plan is specific to the reviewed TarkovPilotPlus 1.0.0 workspace.
# Run without arguments to inspect it. Only a manual -Apply run can remove files.
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param([switch]$Apply)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$cleanupRoot = [IO.Path]::GetFullPath($PSScriptRoot).TrimEnd([IO.Path]::DirectorySeparatorChar)
$cleanupRootPrefix = $cleanupRoot + [IO.Path]::DirectorySeparatorChar
$retainedVersion = '1.0.0'

function Test-CleanupPathWithin([string]$Path, [string]$Parent) {
    return $Path.Equals($Parent, [StringComparison]::OrdinalIgnoreCase) -or
        $Path.StartsWith($Parent + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)
}

function Assert-CleanupNoReparse([string]$Path) {
    $entry = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing a symbolic link, junction or other reparse point: $Path"
    }
    return $entry
}

function Resolve-CleanupPath([string]$RelativePath) {
    if ([string]::IsNullOrWhiteSpace($RelativePath) -or [IO.Path]::IsPathRooted($RelativePath) -or
        $RelativePath.IndexOfAny([char[]]'*?[]') -ge 0 -or
        @($RelativePath.Split([char[]]'\/') | Where-Object { $_ -eq '.' -or $_ -eq '..' }).Count -gt 0) {
        throw "Only literal workspace-relative paths are allowed: $RelativePath"
    }
    $fullPath = [IO.Path]::GetFullPath((Join-Path $cleanupRoot $RelativePath))
    if (-not $fullPath.StartsWith($cleanupRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "The target must stay strictly inside this workspace: $RelativePath"
    }

    # Check every existing ancestor inside the workspace before resolving the leaf.
    $ancestor = $fullPath
    while (Test-CleanupPathWithin $ancestor $cleanupRoot) {
        if (Test-Path -LiteralPath $ancestor) {
            [void](Assert-CleanupNoReparse $ancestor)
        }
        if ($ancestor.Equals($cleanupRoot, [StringComparison]::OrdinalIgnoreCase)) { break }
        $ancestor = [IO.Path]::GetDirectoryName($ancestor)
    }
    if (Test-Path -LiteralPath $fullPath) {
        $resolved = Resolve-Path -LiteralPath $fullPath -ErrorAction Stop
        if ($resolved.Provider.Name -ne 'FileSystem' -or
            -not [IO.Path]::GetFullPath($resolved.ProviderPath).Equals($fullPath, [StringComparison]::OrdinalIgnoreCase)) {
            throw "The target did not resolve to its expected filesystem path: $RelativePath"
        }
    }
    return $fullPath
}

function Assert-CleanupUnprotected([string]$Path) {
    foreach ($protected in $protectedPaths) {
        if ((Test-CleanupPathWithin $Path $protected) -or (Test-CleanupPathWithin $protected $Path)) {
            throw "Refusing a protected source, tool, cache or current release path: $Path"
        }
    }
}

function Get-CleanupTargetInfo([string]$RelativePath) {
    if (-not ($allowedTargets -contains $RelativePath)) {
        throw "The target is not on the explicit cleanup allowlist: $RelativePath"
    }
    $fullPath = Resolve-CleanupPath $RelativePath
    Assert-CleanupUnprotected $fullPath
    if (-not (Test-Path -LiteralPath $fullPath)) { return $null }

    # Walk explicitly so recursive enumeration cannot follow directory links.
    [long]$bytes = 0
    [long]$files = 0
    $rootEntry = Assert-CleanupNoReparse $fullPath
    $stack = New-Object 'System.Collections.Generic.Stack[string]'
    if ($rootEntry.PSIsContainer) { $stack.Push($fullPath) }
    else { $bytes = $rootEntry.Length; $files = 1 }
    while ($stack.Count -gt 0) {
        $directory = $stack.Pop()
        foreach ($entry in Get-ChildItem -LiteralPath $directory -Force -ErrorAction Stop) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing a reparse point inside a cleanup target: $($entry.FullName)"
            }
            if (-not $entry.FullName.StartsWith($cleanupRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
                throw "A child escaped the workspace: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) { $stack.Push($entry.FullName) }
            else { $bytes += $entry.Length; $files++ }
        }
    }
    return [PSCustomObject]@{ RelativePath = $RelativePath; FullPath = $fullPath; Files = $files; Bytes = $bytes }
}

if ((Split-Path $cleanupRoot -Leaf) -cne 'TarkovPilotPlus') {
    throw 'Run this script only from the named TarkovPilotPlus repository.'
}
[void](Assert-CleanupNoReparse $cleanupRoot)
$resolvedRoot = Resolve-Path -LiteralPath $cleanupRoot -ErrorAction Stop
if ($resolvedRoot.Provider.Name -ne 'FileSystem' -or
    -not [IO.Path]::GetFullPath($resolvedRoot.ProviderPath).Equals($cleanupRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'The workspace did not resolve to its expected filesystem path.'
}
foreach ($marker in @('.git', 'app\go.mod', 'app\main.go', 'app\plus\version.go')) {
    $markerPath = Resolve-CleanupPath $marker
    if (-not (Test-Path -LiteralPath $markerPath)) { throw "Required repository marker is missing: $marker" }
}
$versionSource = [IO.File]::ReadAllText((Resolve-CleanupPath 'app\plus\version.go'))
if ($versionSource -notmatch 'const Version = "([^"]+)"' -or $Matches[1] -ne $retainedVersion) {
    throw 'This reviewed plan preserves Plus 1.0.0. Review and update the plan before using it with another version.'
}

$currentPackage = 'app\build\bin\TarkovPilotPlus-1.0.0-windows-x64'
$currentExe = $currentPackage + '\TarkovPilotPlus.exe'
$currentZip = 'app\build\bin\TarkovPilotPlus-1.0.0-windows-x64.zip'
$currentHash = $currentZip + '.sha256'
foreach ($current in @($currentPackage, $currentExe, $currentZip, $currentHash)) {
    if (-not (Test-Path -LiteralPath (Resolve-CleanupPath $current))) {
        throw "The current release must be present before old builds are removed: $current"
    }
}

$protectedRelativePaths = @(
    '.git', 'Clean-Workspace.ps1', 'README.md', 'THIRD_PARTY_NOTICES.md',
    'app\go.mod', 'app\go.sum', 'app\main.go', 'app\app.go', 'app\version.go',
    'app\plus', 'app\frontend\dist', 'app\build\appicon.png', 'app\build\windows',
    '.local\tools\go125', '.local\tools\bin125', '.local\tools\ocr',
    $currentPackage, $currentZip, $currentHash
)
$protectedPaths = @($protectedRelativePaths | ForEach-Object { Resolve-CleanupPath $_ })

# Literal names only: no wildcard deletion, discovery of unrelated paths or shell handoff.
$allowedTargets = @(
    '.local\native-profile',
    '.local\ocr-research',
    '.local\ui-cdp-profile-rUDWIL',
    '.local\native-ui-cdp-qa.cjs',
    '.local\native-window-qa.json',
    '.local\native-window-qa.ps1',
    '.local\qa-main-region-english.cjs',
    '.local\qa-main-versions-browser.cjs',
    '.local\qa-pe-version.go',
    '.local\verify-plus-bundle.ps1',
    '.local\tools\go',
    '.local\tools\bin',
    '.local\tools\go1.25.14.windows-amd64.zip',
    '.local\tools\go1.27.2.windows-amd64.zip',
    'app\.local',
    'output',
    '.playwright-cli',
    'images',
    'app\frontend\wailsjs',
    'app\build\bin\TarkovPilot.exe',
    'app\build\bin\ocr',
    'app\build\bin\TarkovPilotPlus-2.1.1.exe',
    'app\build\bin\TarkovPilotPlus-2.1.1-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.1-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.1-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.2.exe',
    'app\build\bin\TarkovPilotPlus-2.1.2-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.2-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.2-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.3.exe',
    'app\build\bin\TarkovPilotPlus-2.1.3-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.3-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.3-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.4.exe',
    'app\build\bin\TarkovPilotPlus-2.1.4-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.4-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.4-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.5.exe',
    'app\build\bin\TarkovPilotPlus-2.1.5-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.5-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.5-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.6.exe',
    'app\build\bin\TarkovPilotPlus-2.1.6-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.6-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.6-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.7.exe',
    'app\build\bin\TarkovPilotPlus-2.1.7-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.7-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.7-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.8.exe',
    'app\build\bin\TarkovPilotPlus-2.1.8-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.8-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.8-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.9.exe',
    'app\build\bin\TarkovPilotPlus-2.1.9-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.9-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.9-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.10.exe',
    'app\build\bin\TarkovPilotPlus-2.1.10-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.10-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.10-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.11.exe',
    'app\build\bin\TarkovPilotPlus-2.1.11-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.11-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.11-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.12.exe',
    'app\build\bin\TarkovPilotPlus-2.1.12-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.12-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.12-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.13.exe',
    'app\build\bin\TarkovPilotPlus-2.1.13-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.13-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.13-windows-x64.zip.sha256',
    'app\build\bin\TarkovPilotPlus-2.1.14.exe',
    'app\build\bin\TarkovPilotPlus-2.1.14-windows-x64',
    'app\build\bin\TarkovPilotPlus-2.1.14-windows-x64.zip',
    'app\build\bin\TarkovPilotPlus-2.1.14-windows-x64.zip.sha256'
)

# The current standalone EXE is disposable only if it exactly duplicates the retained package EXE.
$standaloneRelativePath = 'app\build\bin\TarkovPilotPlus-1.0.0.exe'
$standalonePath = Resolve-CleanupPath $standaloneRelativePath
if (Test-Path -LiteralPath $standalonePath) {
    $packageExePath = Resolve-CleanupPath $currentExe
    if ((Get-FileHash -LiteralPath $standalonePath -Algorithm SHA256).Hash -eq
        (Get-FileHash -LiteralPath $packageExePath -Algorithm SHA256).Hash) {
        $allowedTargets += $standaloneRelativePath
    } else {
        Write-Host 'Keeping the standalone 1.0.0 EXE: its SHA256 differs from the retained package EXE.'
        $protectedPaths += $standalonePath
    }
}

# Complete validation and measurement before any -Apply operation can start.
$cleanupPlan = @($allowedTargets | ForEach-Object { Get-CleanupTargetInfo $_ } | Where-Object { $null -ne $_ })
[long]$totalFiles = 0
[long]$totalBytes = 0
foreach ($target in $cleanupPlan) { $totalFiles += $target.Files; $totalBytes += $target.Bytes }
Write-Host "Workspace: $cleanupRoot"
Write-Host 'Retained: source, .git, current Go/Wails tools, pinned OCR cache/runtime and the 1.0.0 ZIP/hash/package.'
$cleanupPlan | Select-Object RelativePath, Files, Bytes | Format-Table -AutoSize | Out-Host
Write-Host ('Checked plan: {0} targets; {1} files; {2} bytes ({3:N2} GiB).' -f $cleanupPlan.Count, $totalFiles, $totalBytes, ($totalBytes / 1GB))

[int]$removedTargets = 0
if ($Apply) {
    foreach ($target in $cleanupPlan) {
        # Recheck immediately before each removal; filesystem changes must not redirect it.
        $checked = Get-CleanupTargetInfo $target.RelativePath
        if ($null -eq $checked) { continue }
        if ($PSCmdlet.ShouldProcess($checked.FullPath, 'Remove this explicitly allowlisted cleanup target')) {
            Remove-Item -LiteralPath $checked.FullPath -Recurse -Force -ErrorAction Stop
            $removedTargets++
        }
    }
    Write-Host "Removed targets: $removedTargets"
} else {
    Write-Host 'DRY RUN: no files were removed. Review the plan, close apps using these files, then run .\Clean-Workspace.ps1 -Apply manually if wanted.'
}

[PSCustomObject]@{
    Mode = $(if ($Apply) { 'Apply' } else { 'DryRun' })
    PlannedTargets = $cleanupPlan.Count
    PlannedFiles = $totalFiles
    PlannedBytes = $totalBytes
    RemovedTargets = $removedTargets
}
