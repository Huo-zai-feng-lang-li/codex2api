$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$root = Split-Path -Parent $PSScriptRoot
$candidate = Join-Path $root 'codex2api.new.exe'
$executable = Join-Path $root 'codex2api.exe'
$rollback = Join-Path $root 'codex2api.rollback.exe'
$healthUrl = 'http://127.0.0.1:18080/health'

function Get-ListenerPid {
    $line = netstat -ano -p tcp | Where-Object { $_ -match '^\s*TCP\s+127\.0\.0\.1:18080\s+.*LISTENING\s+(\d+)\s*$' } | Select-Object -First 1
    if (-not $line) { return 0 }
    return [int]([regex]::Match($line, '(\d+)\s*$').Groups[1].Value)
}

function Get-HealthyState {
    $health = Invoke-RestMethod -Uri $healthUrl -TimeoutSec 3
    if ($health.status -ne 'ok') { throw "health status is $($health.status)" }
    return $health
}

function Start-ServiceExecutable {
    $outLog = Join-Path $root 'logs\start.out.log'
    $errLog = Join-Path $root 'logs\start.err.log'
    New-Item -ItemType Directory -Path (Split-Path -Parent $outLog) -Force | Out-Null
    return Start-Process -FilePath $executable -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $outLog -RedirectStandardError $errLog -PassThru
}

if (-not (Test-Path -LiteralPath $candidate)) { throw 'candidate executable not found' }
if (-not (Test-Path -LiteralPath $executable)) { throw 'current executable not found' }
if (Test-Path -LiteralPath $rollback) { throw 'rollback executable already exists' }

$candidateHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $candidate).Hash
$oldPid = Get-ListenerPid
if ($oldPid -le 0) { throw 'current listener not found' }

for ($i = 0; $i -lt 3; $i++) {
    $health = Get-HealthyState
    if ([int]$health.responses_memory.inflight_requests -ne 0) {
        throw "inflight requests did not drain: $($health.responses_memory.inflight_requests)"
    }
    Start-Sleep -Milliseconds 400
}

$adminSecret = [string]$env:ADMIN_SECRET
if ([string]::IsNullOrWhiteSpace($adminSecret)) {
    $sqlite = (Get-Command sqlite3 -ErrorAction Stop).Source
    $databasePath = 'data\codex2api.db'
    $envFile = Join-Path $root '.env'
    if (Test-Path -LiteralPath $envFile) {
        $databaseLine = Get-Content -LiteralPath $envFile | Where-Object { $_ -match '^DATABASE_PATH=' } | Select-Object -First 1
        if ($databaseLine) { $databasePath = $databaseLine.Substring('DATABASE_PATH='.Length).Trim() }
    }
    if (-not [IO.Path]::IsPathRooted($databasePath)) { $databasePath = Join-Path $root $databasePath }
    $adminSecret = (& $sqlite $databasePath 'SELECT admin_secret FROM system_settings LIMIT 1;').Trim()
}
if ([string]::IsNullOrWhiteSpace($adminSecret)) { throw 'admin secret unavailable' }

$shutdownIssued = $false
try {
    Invoke-WebRequest -Method Post -Uri 'http://127.0.0.1:18080/api/admin/system/shutdown' -Headers @{ 'X-Admin-Key' = $adminSecret } -TimeoutSec 10 | Out-Null
    $shutdownIssued = $true
} catch {
    if ($_.Exception.Message -notmatch 'ResponseEnded|The response ended prematurely') {
        throw
    }
    $shutdownIssued = $true
}
$deadline = [DateTime]::UtcNow.AddSeconds(30)
while (Get-Process -Id $oldPid -ErrorAction SilentlyContinue) {
    if ([DateTime]::UtcNow -ge $deadline) { throw 'graceful shutdown timeout' }
    Start-Sleep -Milliseconds 250
}
if (-not $shutdownIssued) { throw 'shutdown request was not issued' }

$deployed = $false
try {
    Move-Item -LiteralPath $executable -Destination $rollback
    Move-Item -LiteralPath $candidate -Destination $executable
    $process = Start-ServiceExecutable

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        Start-Sleep -Milliseconds 300
        try {
            $health = Get-HealthyState
            $newPid = Get-ListenerPid
            if ($newPid -gt 0) { $deployed = $true }
        } catch {
            $deployed = $false
        }
    } while (-not $deployed -and [DateTime]::UtcNow -lt $deadline)

    if (-not $deployed) { throw 'new service failed health or listener check' }
    $runningHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $executable).Hash
    if ($runningHash -ne $candidateHash) { throw 'running executable hash mismatch' }
    if (-not [bool]$health.responses_memory.continuity_persistent) { throw 'continuity persistence is disabled' }
    if ([int]$health.responses_memory.continuity_persistence_failures -ne 0) { throw 'continuity persistence failure detected' }

    Remove-Item -LiteralPath $rollback -Force
    [pscustomobject]@{
        old_pid = $oldPid
        new_pid = $newPid
        sha256 = $runningHash
        health = $health.status
        inflight = [int]$health.responses_memory.inflight_requests
        continuity_persistent = [bool]$health.responses_memory.continuity_persistent
        persistence_failures = [int]$health.responses_memory.continuity_persistence_failures
    } | ConvertTo-Json -Compress
} catch {
    $failedPid = Get-ListenerPid
    if ($failedPid -gt 0) { Stop-Process -Id $failedPid -Force -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $executable) { Remove-Item -LiteralPath $executable -Force }
    if (Test-Path -LiteralPath $rollback) { Move-Item -LiteralPath $rollback -Destination $executable }
    Start-ServiceExecutable | Out-Null
    throw
}
