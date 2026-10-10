[CmdletBinding()]
param(
    [string]$RepositoryRoot = (Join-Path $PSScriptRoot '../..'),
    [string]$OutputPath = ''
)
$ErrorActionPreference = 'Stop'
$repoPath = [IO.Path]::GetFullPath($RepositoryRoot)
if (-not $OutputPath) { $OutputPath = Join-Path $repoPath 'artifacts/bid-mon-phase2-deploy.zip' }
$zipPath = [IO.Path]::GetFullPath($OutputPath)
if (-not $zipPath.StartsWith($repoPath + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetExtension($zipPath) -ne '.zip') {
    throw 'The output must be a .zip file inside the repository.'
}
# Deliberate allowlist. Never enumerate the whole project or runtime config tree.
$files = [ordered]@{
    'README.md' = 'deploy/phase2/README.release.md'
    'compose.yaml' = 'compose.yaml'
    'compose.bootstrap.yaml' = 'compose.bootstrap.yaml'
    'compose.demo.yaml' = 'compose.demo.yaml'
    'start.ps1' = 'start.ps1'
    'start.sh' = 'start.sh'
    'deploy/phase2/env.example' = 'deploy/phase2/env.example'
    'deploy/phase2/images.json' = 'deploy/phase2/images.json'
    'deploy/phase2/demo.template.json' = 'deploy/phase2/demo.template.json'
    'deploy/phase2/evidence.template.json' = 'deploy/phase2/evidence.template.json'
    'deploy/phase2/helper-profiles.template.json' = 'deploy/phase2/helper-profiles.template.json'
    'deploy/mysql/01-schema.sql' = 'deploy/mysql/01-schema.sql'
    'deploy/mysql/01-order-schema.sql' = 'deploy/mysql/01-order-schema.sql'
    'deploy/mysql/02-readonly.sh' = 'deploy/mysql/02-readonly.sh'
    'docs/deployment.md' = 'docs/deployment.md'
    'docs/phase-2-onboarding.md' = 'docs/phase-2-onboarding.md'
    'docs/phase-2-validation.md' = 'docs/phase-2-validation.md'
    'examples/order-callback/README.md' = 'examples/order-callback/README.md'
    'examples/order-callback/schema.sql' = 'examples/order-callback/schema.sql'
    'examples/order-callback/schema.postgres.sql' = 'examples/order-callback/schema.postgres.sql'
    'examples/order-callback/monitor.mysql.yaml' = 'examples/order-callback/monitor.mysql.yaml'
    'examples/order-callback/monitor.postgres.yaml' = 'examples/order-callback/monitor.postgres.yaml'
    'examples/order-callback/monitor.aggregate.yaml' = 'examples/order-callback/monitor.aggregate.yaml'
}
function Get-PackageHash([byte[]]$Bytes) {
    $algorithm = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($algorithm.ComputeHash($Bytes))).Replace('-', '').ToLowerInvariant() }
    finally { $algorithm.Dispose() }
}
Add-Type -AssemblyName System.IO.Compression
$outputDirectory = [IO.Path]::GetDirectoryName($zipPath)
[IO.Directory]::CreateDirectory($outputDirectory) | Out-Null
$temporaryPath = $zipPath + '.tmp'
if (Test-Path -LiteralPath $temporaryPath) { throw 'A package build temporary file already exists; inspect it before retrying.' }
$output = [IO.File]::Open($temporaryPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
$archive = [IO.Compression.ZipArchive]::new($output, [IO.Compression.ZipArchiveMode]::Create, $false)
$hashes = [ordered]@{}
try {
    foreach ($entryName in $files.Keys) {
        $source = Join-Path $repoPath $files[$entryName]
        $info = Get-Item -LiteralPath $source
        if ($info.PSIsContainer -or ($info.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "Package source must be a regular file: $entryName" }
        $bytes = [IO.File]::ReadAllBytes($source)
        $hashes[$entryName] = Get-PackageHash $bytes
        $entry = $archive.CreateEntry($entryName, [IO.Compression.CompressionLevel]::Optimal)
        $stream = $entry.Open()
        try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
    }
    $manifest = [ordered]@{ version = '2.0.0-dev'; images_published = $false; files = $hashes }
    $manifestBytes = [Text.Encoding]::UTF8.GetBytes(($manifest | ConvertTo-Json -Depth 5))
    $manifestEntry = $archive.CreateEntry('manifest.json')
    $stream = $manifestEntry.Open()
    try { $stream.Write($manifestBytes, 0, $manifestBytes.Length) } finally { $stream.Dispose() }
} finally { $archive.Dispose() }

$archiveInput = [IO.File]::OpenRead($temporaryPath)
$verify = [IO.Compression.ZipArchive]::new($archiveInput, [IO.Compression.ZipArchiveMode]::Read, $false)
try {
    if ($verify.Entries.Count -ne ($files.Count + 1)) { throw 'Unexpected package entry count.' }
    foreach ($entry in $verify.Entries) {
        if ($entry.FullName -eq 'manifest.json') { continue }
        if (-not $files.Contains($entry.FullName)) { throw 'Unexpected package file.' }
        $stream = $entry.Open()
        $buffer = [IO.MemoryStream]::new()
        try { $stream.CopyTo($buffer) } finally { $stream.Dispose() }
        $actual = Get-PackageHash $buffer.ToArray()
        $buffer.Dispose()
        if ($actual -ne $hashes[$entry.FullName]) { throw "Package integrity check failed: $($entry.FullName)" }
    }
} finally { $verify.Dispose() }
Move-Item -LiteralPath $temporaryPath -Destination $zipPath -Force
Write-Host "Deployment package created and every allowlisted file verified: $zipPath"
