param([string]$OutputFilename = "TarkovPilotPlus.exe")

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if ($OutputFilename -notmatch '^[A-Za-z0-9._-]+\.exe$') {
    throw "Az EXE neve csak betűt, számot, pontot, aláhúzást és kötőjelet tartalmazhat."
}

$previousGoos = [Environment]::GetEnvironmentVariable("GOOS", "Process")

Push-Location -LiteralPath $PSScriptRoot
try {
    $wails = Get-Command wails -ErrorAction SilentlyContinue
    if ($null -eq $wails) {
        throw "Wails CLI was not found. Install it with: go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0"
    }

    [Environment]::SetEnvironmentVariable("GOOS", "windows", "Process")

    # A helyi OCR-motor és a modellek csak a build során töltődnek le.
    & (Join-Path $PSScriptRoot "plus\ocr\prepare.ps1")

    # A saját verzió külön forrásból jön; az upstream verzió csak olvasható.
    # read via ReadAllText: Get-Content in PS 5.1 treats BOM-less UTF-8 as ANSI and mangles non-ASCII
    $versionGoPath = Join-Path $PSScriptRoot "plus\version.go"
    $versionGo = [IO.File]::ReadAllText($versionGoPath)
    if ($versionGo -notmatch 'const Version = "([^"]+)"') {
        throw "Could not parse Version from plus/version.go"
    }
    $version = $Matches[1]

    $originalVersionGo = [IO.File]::ReadAllText((Join-Path $PSScriptRoot "version.go"))
    if ($originalVersionGo -notmatch 'const Version = "([^"]+)"') {
        throw "Could not parse the original Version from version.go"
    }
    $originalVersion = $Matches[1]
    $upstream = [IO.File]::ReadAllText((Join-Path $PSScriptRoot "plus\upstream.json")) | ConvertFrom-Json
    if ($upstream.version -ne $originalVersion) {
        throw "The upstream manifest and original version.go do not match. Update them together after merging upstream."
    }

    # if the version wasn't bumped since the last commit — auto-bump the patch.
    # git via cmd: in PS 5.1 redirected stderr of native commands turns into errors
    $headGo = cmd /c "git -C `"$PSScriptRoot`" show HEAD:app/plus/version.go 2>nul"
    if ($LASTEXITCODE -eq 0 -and "$headGo" -match 'const Version = "([^"]+)"') {
        $headVersion = $Matches[1]
        if ([version]$version -le [version]$headVersion) {
            $v = [version]$headVersion
            $version = "{0}.{1}.{2}" -f $v.Major, $v.Minor, ($v.Build + 1)
            $versionGo = $versionGo -replace 'const Version = "[^"]+"', ('const Version = "' + $version + '"')
            [IO.File]::WriteAllText($versionGoPath, $versionGo, (New-Object System.Text.UTF8Encoding $false))
            Write-Host "plus/version.go: $headVersion -> $version (auto-bump)" -ForegroundColor Yellow
        }
    }
    $wailsJsonPath = Join-Path $PSScriptRoot "wails.json"
    $wailsJson = [IO.File]::ReadAllText($wailsJsonPath)
    $updated = $wailsJson -replace '"productVersion":\s*"[^"]*"', ('"productVersion": "' + $version + '"')
    if ($updated -ne $wailsJson) {
        [IO.File]::WriteAllText($wailsJsonPath, $updated, (New-Object System.Text.UTF8Encoding $false))
        Write-Host "wails.json: productVersion -> $version"
    }

    & $wails.Source build -o $OutputFilename
    if ($LASTEXITCODE -ne 0) {
        throw "Wails build failed with exit code $LASTEXITCODE"
    }

    $outputPath = Join-Path $PSScriptRoot ("build\bin\" + $OutputFilename)
    if (-not (Test-Path -LiteralPath $outputPath)) {
        throw "Build completed, but the output file was not found: $outputPath"
    }

    # A terjeszthető EXE neve mindig ugyanaz, a teljes csomag verziózott mappába kerül.
    $packageDirectory = Join-Path $PSScriptRoot ("build\bin\TarkovPilotPlus-" + $version + "-windows-x64")
    New-Item -ItemType Directory -Path $packageDirectory -Force | Out-Null
    $packageExe = Join-Path $packageDirectory "TarkovPilotPlus.exe"
    Move-Item -LiteralPath $outputPath -Destination $packageExe -Force
    $outputPath = $packageExe
    $ocrOutputPath = Join-Path $packageDirectory "ocr"
    New-Item -ItemType Directory -Path $ocrOutputPath -Force | Out-Null
    Copy-Item -Path (Join-Path $PSScriptRoot "plus\ocr\runtime\*") -Destination $ocrOutputPath -Recurse -Force

    $versionMetadataPath = Join-Path $packageDirectory "versions.json"
    $versionMetadata = [ordered]@{
        tarkovPilot = [ordered]@{ version = $originalVersion; repository = $upstream.repository; commit = $upstream.commit }
        tarkovPilotPlus = [ordered]@{ version = $version; repository = "https://github.com/ColorsASD/TarkovPilotPlus" }
    } | ConvertTo-Json -Depth 3
    [IO.File]::WriteAllText($versionMetadataPath, $versionMetadata, (New-Object Text.UTF8Encoding $false))

    # A futtatható fájl és az OCR összetevők együtt terjeszthetők.
    $bundlePath = Join-Path $PSScriptRoot ("build\bin\TarkovPilotPlus-" + $version + "-windows-x64.zip")
    $bundleTemporaryPath = $bundlePath + ".partial"
    $bundleStream = [IO.File]::Open($bundleTemporaryPath, [IO.FileMode]::Create, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    $bundle = New-Object IO.Compression.ZipArchive($bundleStream, [IO.Compression.ZipArchiveMode]::Create, $false)
    try {
        [void][IO.Compression.ZipFileExtensions]::CreateEntryFromFile($bundle, $outputPath, "TarkovPilotPlus.exe", [IO.Compression.CompressionLevel]::Optimal)
        [void][IO.Compression.ZipFileExtensions]::CreateEntryFromFile($bundle, $versionMetadataPath, "versions.json", [IO.Compression.CompressionLevel]::Optimal)
        foreach ($file in Get-ChildItem -LiteralPath $ocrOutputPath -Recurse -File) {
            $entryName = "ocr/" + $file.FullName.Substring($ocrOutputPath.Length + 1).Replace('\', '/')
            [void][IO.Compression.ZipFileExtensions]::CreateEntryFromFile($bundle, $file.FullName, $entryName, [IO.Compression.CompressionLevel]::Optimal)
        }
    }
    finally {
        $bundle.Dispose()
        $bundleStream.Dispose()
    }
    Move-Item -LiteralPath $bundleTemporaryPath -Destination $bundlePath -Force
    $bundleHash = (Get-FileHash -LiteralPath $bundlePath -Algorithm SHA256).Hash
    [IO.File]::WriteAllText($bundlePath + ".sha256", ($bundleHash + "  " + [IO.Path]::GetFileName($bundlePath) + [Environment]::NewLine), (New-Object Text.UTF8Encoding $false))

    # A sikeresen elkészült új csomagból a korábbi EXE-név eltávolítható.
    $legacyPackageExe = Join-Path $packageDirectory "TarkovPilot.exe"
    if (Test-Path -LiteralPath $legacyPackageExe) {
        Remove-Item -LiteralPath $legacyPackageExe -Force
    }

    Write-Host "Done: $outputPath" -ForegroundColor Green
    Write-Host "Csomag: $bundlePath" -ForegroundColor Green
}
finally {
    [Environment]::SetEnvironmentVariable("GOOS", $previousGoos, "Process")
    Pop-Location
}
