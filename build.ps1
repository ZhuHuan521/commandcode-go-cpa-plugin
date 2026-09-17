$ErrorActionPreference = 'Stop'

$SourceDir = $PSScriptRoot
$PluginName = 'commandcode'
$PluginVersion = if ($env:PLUGIN_VERSION) { $env:PLUGIN_VERSION } else { '1.0.0' }
$OutputDir = if ($env:PLUGIN_OUT_DIR) {
    $env:PLUGIN_OUT_DIR
} else {
    Join-Path $SourceDir '..\plugins'
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'go is required to build the Command Code CPA plugin'
}

$GoOS = $env:GOOS
if (-not $GoOS) {
    $GoOS = (& go env GOOS).Trim()
}
$GoArch = $env:GOARCH
if (-not $GoArch) {
    $GoArch = (& go env GOARCH).Trim()
}

$Extension = switch ($GoOS) {
    'windows' { '.dll' }
    'darwin'  { '.dylib' }
    default   { '.so' }
}

$TargetDir = Join-Path $OutputDir $GoOS
$TargetDir = Join-Path $TargetDir $GoArch
New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null

$FinalFile = Join-Path $TargetDir ("{0}-v{1}{2}" -f $PluginName, $PluginVersion, $Extension)
# MinGW/GNU ld mis-parses the generated DEF when the output name contains a
# version dot, so link with a plain temporary name and rename afterwards.
$OutFile = Join-Path $TargetDir ("{0}-plugin-tmp{1}" -f $PluginName, $Extension)
$ModulePath = 'github.com/router-for-me/commandcode-go-cpa-plugin'

Push-Location $SourceDir
try {
    go mod tidy
    go vet ./...
    go test ./...
    go build -buildvcs=false -buildmode=c-shared `
        -ldflags ("-s -w -X main.pluginVersion={0} -X {1}.pluginVersion={0}" -f $PluginVersion, $ModulePath) `
        -o $OutFile ./cmd/commandcode
    $header = [System.IO.Path]::ChangeExtension($OutFile, '.h')
    if (Test-Path $header) {
        Remove-Item -LiteralPath $header
    }
    Copy-Item -LiteralPath $OutFile -Destination $FinalFile -Force
    Remove-Item -LiteralPath $OutFile -Force
    $finalHeader = [System.IO.Path]::ChangeExtension($FinalFile, '.h')
    if (Test-Path $finalHeader) {
        Remove-Item -LiteralPath $finalHeader
    }
    Write-Host "built $FinalFile"
} finally {
    Pop-Location
}
