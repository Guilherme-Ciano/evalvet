$ErrorActionPreference = 'Stop'
$repo = 'Guilherme-Ciano/evalvet'
$version = if ($env:EVALVET_VERSION) { $env:EVALVET_VERSION } else { 'latest' }
$arch = switch ($env:RUNNER_ARCH) { 'ARM64' { 'arm64' } default { 'amd64' } }

if ($version -eq 'latest') {
  $resp = Invoke-WebRequest -Uri "https://github.com/$repo/releases/latest" -MaximumRedirection 5 -UseBasicParsing
  $version = Split-Path -Leaf $resp.BaseResponse.RequestMessage.RequestUri.AbsolutePath
}
$num = $version.TrimStart('v')
$archive = "evalvet_${num}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$version"
$dir = Join-Path $env:RUNNER_TEMP "evalvet-$num"
New-Item -ItemType Directory -Force -Path $dir | Out-Null

Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $dir $archive) -UseBasicParsing
Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $dir 'checksums.txt') -UseBasicParsing

$line = Get-Content (Join-Path $dir 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -eq $archive }
if (-not $line) { throw "no checksum for $archive" }
$expected = ($line -split '\s+')[0].ToLower()
$actual = (Get-FileHash -Algorithm SHA256 (Join-Path $dir $archive)).Hash.ToLower()
if ($expected -ne $actual) { throw "sha256 mismatch for ${archive}: expected $expected, got $actual" }

Expand-Archive -Path (Join-Path $dir $archive) -DestinationPath $dir -Force
Add-Content -Path $env:GITHUB_PATH -Value $dir
& (Join-Path $dir 'evalvet.exe') version
