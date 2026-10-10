[CmdletBinding()]
param(
    [string]$OutputDirectory = (Join-Path $PSScriptRoot 'runtime'),
    [string]$CacheDirectory = (Join-Path (Split-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) -Parent) '.local\tools\ocr')
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Add-Type -AssemblyName System.IO.Compression.FileSystem

$packageVersion = '5.2.0'
$modelCommit = '87416418657359cb625c412a48b6e1d6d41c29bd'
$wrapperCommit = '2c993543f7fa66576a8890a6c4ab053c4598aaed'
$interopCommit = '0b15eee809716c50562458d6fe95d52bea46b9c6'
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$CacheDirectory = [IO.Path]::GetFullPath($CacheDirectory)
New-Item -ItemType Directory -Path $OutputDirectory, $CacheDirectory, (Join-Path $OutputDirectory 'x64'), (Join-Path $OutputDirectory 'tessdata'), (Join-Path $OutputDirectory 'licenses') -Force | Out-Null

function Assert-Hash([string]$Path, [string]$ExpectedHash) {
    if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -ne $ExpectedHash) {
        throw ('Az OCR összetevő SHA256 ellenőrzése sikertelen: ' + [IO.Path]::GetFileName($Path))
    }
}

function Get-PinnedFile([string]$Name, [string]$Uri, [string]$ExpectedHash) {
    $path = Join-Path $CacheDirectory $Name
    if (Test-Path -LiteralPath $path) {
        Assert-Hash $path $ExpectedHash
        return $path
    }
    $download = $path + '.download'
    Invoke-WebRequest -Uri $Uri -OutFile $download -UseBasicParsing
    Assert-Hash $download $ExpectedHash
    Move-Item -LiteralPath $download -Destination $path -Force
    return $path
}

$package = Get-PinnedFile ('tesseract.' + $packageVersion + '.nupkg') ('https://api.nuget.org/v3-flatcontainer/tesseract/' + $packageVersion + '/tesseract.' + $packageVersion + '.nupkg') '202D82FC7C7D8384DF7DA57206D5E1F456CCDABD648C46E67CDFAA3A911D4795'
$files = @(
    @{ Entry = 'lib/net48/Tesseract.dll'; Destination = 'Tesseract.dll'; Hash = 'BC2AD67AB5FB9E80E5E60F1A422A1F44F830FB1B9A77579E85A634B1A9630840' },
    @{ Entry = 'x64/leptonica-1.82.0.dll'; Destination = 'x64/leptonica-1.82.0.dll'; Hash = 'DFCB3E6ED0B16BC55BFDBCF53543CFE42A354B87C3E35BD3A95EEBF005D73E76' },
    @{ Entry = 'x64/tesseract50.dll'; Destination = 'x64/tesseract50.dll'; Hash = 'DE4D04EC75095374D98F5DD7A60D14D7E2E0F76589DB693ECCF7AE658BE8CB2B' }
)
$archive = [IO.Compression.ZipFile]::OpenRead($package)
try {
    foreach ($file in $files) {
        $entry = $archive.GetEntry($file.Entry)
        if ($null -eq $entry) { throw ('Az OCR csomagból hiányzik: ' + $file.Entry) }
        # Extract only named files, never paths supplied by the archive.
        $destination = Join-Path $OutputDirectory $file.Destination
        $inputStream = $entry.Open()
        try {
            $outputStream = [IO.File]::Create($destination)
            try { $inputStream.CopyTo($outputStream) } finally { $outputStream.Dispose() }
        } finally { $inputStream.Dispose() }
        Assert-Hash $destination $file.Hash
    }
} finally { $archive.Dispose() }

$resources = @(
    @{ Name = 'eng.traineddata'; Destination = 'tessdata/eng.traineddata'; Uri = ('https://raw.githubusercontent.com/tesseract-ocr/tessdata_fast/' + $modelCommit + '/eng.traineddata'); Hash = '7D4322BD2A7749724879683FC3912CB542F19906C83BCC1A52132556427170B2' },
    @{ Name = 'rus.traineddata'; Destination = 'tessdata/rus.traineddata'; Uri = ('https://raw.githubusercontent.com/tesseract-ocr/tessdata_fast/' + $modelCommit + '/rus.traineddata'); Hash = 'E16E5E036CCE1D9EC2B00063CF8B54472625B9E14D893A169E2B0DEDEB4DF225' },
    @{ Name = 'Tesseract-NET-LICENSE.txt'; Destination = 'licenses/Tesseract-NET-LICENSE.txt'; Uri = ('https://raw.githubusercontent.com/charlesw/tesseract/' + $wrapperCommit + '/LICENSE.txt'); Hash = 'CFC7749B96F63BD31C3C42B5C471BF756814053E847C10F3EB003417BC523D30' },
    @{ Name = 'Tesseract-LICENSE.txt'; Destination = 'licenses/Tesseract-LICENSE.txt'; Uri = 'https://raw.githubusercontent.com/tesseract-ocr/tesseract/5.0.0/LICENSE'; Hash = 'CFC7749B96F63BD31C3C42B5C471BF756814053E847C10F3EB003417BC523D30' },
    @{ Name = 'Leptonica-LICENSE.txt'; Destination = 'licenses/Leptonica-LICENSE.txt'; Uri = 'https://raw.githubusercontent.com/DanBloomberg/leptonica/1.82.0/leptonica-license.txt'; Hash = '87829ABB5BBB00B55A107365DA89E9A33F86C4250169E5A1E5588505BE7D5806' },
    @{ Name = 'tessdata-fast-LICENSE.txt'; Destination = 'licenses/tessdata-fast-LICENSE.txt'; Uri = ('https://raw.githubusercontent.com/tesseract-ocr/tessdata_fast/' + $modelCommit + '/LICENSE'); Hash = 'CFC7749B96F63BD31C3C42B5C471BF756814053E847C10F3EB003417BC523D30' },
    @{ Name = 'InteropDotNet-LICENSE.txt'; Destination = 'licenses/InteropDotNet-LICENSE.txt'; Uri = ('https://raw.githubusercontent.com/AndreyAkinshin/InteropDotNet/' + $interopCommit + '/LICENSE.md'); Hash = '8FB016CD2A9EFAA281ECCE3075F69D20FFA8314BA8ECF8586D6744DD346CEA25' }
)
foreach ($resource in $resources) {
    $cached = Get-PinnedFile $resource.Name $resource.Uri $resource.Hash
    Copy-Item -LiteralPath $cached -Destination (Join-Path $OutputDirectory $resource.Destination) -Force
    Assert-Hash (Join-Path $OutputDirectory $resource.Destination) $resource.Hash
}

$repositoryRoot = Split-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) -Parent
Copy-Item -LiteralPath (Join-Path $repositoryRoot 'THIRD_PARTY_NOTICES.md') -Destination (Join-Path $OutputDirectory 'THIRD_PARTY_NOTICES.md') -Force
$manifest = [ordered]@{
    wrapperVersion = $packageVersion
    nativeVersion = '5.0.0'
    modelCommit = $modelCommit
    source = 'https://api.nuget.org/v3-flatcontainer/tesseract/5.2.0/tesseract.5.2.0.nupkg'
    hashes = [ordered]@{}
}
foreach ($file in $files) { $manifest.hashes[$file.Destination] = $file.Hash }
foreach ($resource in $resources) { $manifest.hashes[$resource.Destination] = $resource.Hash }
$manifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'manifest.json') -Encoding UTF8
Write-Host ('A helyi Tesseract OCR csomag előkészítve és SHA256 alapján ellenőrizve: ' + $OutputDirectory)
