param(
  [string]$InputPath = "build\bin\NapCatFileMover.exe"
)

$missing = @()
if (-not $env:WINDOWS_CERT_PATH) { $missing += "WINDOWS_CERT_PATH" }
if (-not $env:WINDOWS_CERT_PASSWORD) { $missing += "WINDOWS_CERT_PASSWORD" }
if (-not $env:WINDOWS_TIMESTAMP_URL) { $missing += "WINDOWS_TIMESTAMP_URL" }

if ($missing.Count -gt 0) {
  Write-Error ("missing required environment variables: " + ($missing -join ", "))
  exit 2
}

if (-not (Test-Path $InputPath)) {
  Write-Error "input not found: $InputPath"
  exit 2
}

$signtool = Get-Command signtool.exe -ErrorAction SilentlyContinue
if (-not $signtool) {
  Write-Error "signtool.exe not found. Install Windows SDK and run from a Developer PowerShell."
  exit 2
}

& $signtool.Source sign `
  /fd SHA256 `
  /td SHA256 `
  /tr $env:WINDOWS_TIMESTAMP_URL `
  /f $env:WINDOWS_CERT_PATH `
  /p $env:WINDOWS_CERT_PASSWORD `
  $InputPath

& $signtool.Source verify /pa /v $InputPath
