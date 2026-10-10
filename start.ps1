[CmdletBinding()]
param(
    [ValidateSet('business', 'demo')][string]$Mode = 'business',
    [switch]$Build,
    [string]$Image = '',
    [string]$DemoImage = '',
    [string]$SSHImage = '',
    [ValidatePattern('^[a-z0-9][a-z0-9_-]{0,47}$')][string]$Instance = 'bid-mon',
    [ValidateRange(1, 65535)][int]$Port = 18080
)
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

function Read-InstallationValue([string]$Name, [string]$Fallback) {
    if (Test-Path -LiteralPath '.env') {
        foreach ($line in [System.IO.File]::ReadAllLines((Join-Path $PSScriptRoot '.env'))) {
            if ($line.StartsWith($Name + '=')) { return $line.Substring($Name.Length + 1).Trim('"', "'") }
        }
    }
    return $Fallback
}
function Invoke-Docker([string[]]$DockerArguments) {
    & docker @DockerArguments
    if ($LASTEXITCODE -ne 0) { throw "Docker command failed (exit $LASTEXITCODE). Check Docker Desktop, image availability, configuration, and ports." }
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Install Docker Desktop with Docker Compose first.' }
Invoke-Docker -DockerArguments @('info', '--format', '{{.ServerVersion}}')
Invoke-Docker -DockerArguments @('compose', 'version', '--short')
if (-not $PSBoundParameters.ContainsKey('Mode')) { $Mode = Read-InstallationValue 'BID_MON_MODE' 'business' }
if (-not $Image) { $Image = Read-InstallationValue 'BID_MON_IMAGE' 'bid-mon:2.0.0-dev' }
if (-not $DemoImage) { $DemoImage = Read-InstallationValue 'BID_MON_LAB_IMAGE' 'bid-mon-lab:2.0.0-dev' }
if (-not $SSHImage) { $SSHImage = Read-InstallationValue 'BID_MON_SSH_IMAGE' 'bid-mon-go-ssh:2.0.0-dev' }
$env:BID_MON_IMAGE = $Image
$env:BID_MON_LAB_IMAGE = $DemoImage
$env:BID_MON_SSH_IMAGE = $SSHImage

if ($Build) {
    Invoke-Docker -DockerArguments @('build', '-f', 'Dockerfile.go', '--target', 'runtime', '-t', $Image, '.')
} elseif ($Image -eq 'bid-mon:2.0.0-dev') {
    & docker image inspect $Image *> $null
    if ($LASTEXITCODE -ne 0) { throw 'The development image has not been published. Use .\start.ps1 -Build, or provide -Image with an available fixed release image.' }
}
Invoke-Docker -DockerArguments @('compose', '-p', ($Instance + '-bootstrap'), '-f', 'compose.bootstrap.yaml', 'run', '--rm', '-T', 'initializer', 'init', '--dir', '/workspace', '--mode', $Mode, '--image', $Image, '--demo-image', $DemoImage, '--ssh-image', $SSHImage, '--instance', $Instance, '--port', "$Port", '--uid', '10001', '--gid', '10001')

$compose = @('compose', '--env-file', '.env', '-f', 'compose.yaml')
if ($Mode -eq 'demo') {
    $compose += @('-f', 'compose.demo.yaml')
    if ($Build) {
        Invoke-Docker -DockerArguments @('build', '-f', 'Dockerfile', '-t', $DemoImage, '.')
        Invoke-Docker -DockerArguments @('build', '-f', 'Dockerfile.go', '--target', 'ssh-target', '-t', $SSHImage, '.')
    }
}
Invoke-Docker -DockerArguments ($compose + @('up', '-d', '--wait', '--wait-timeout', '180'))
$actualPort = Read-InstallationValue 'BID_MON_HTTP_PORT' "$Port"
Write-Host "Application: http://127.0.0.1:$actualPort"
Write-Host 'Read your administrator token from deploy/phase2/config/credentials.json. Existing credentials were preserved.'
