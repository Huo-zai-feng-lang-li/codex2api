$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$log = Join-Path $PSScriptRoot 'tmp-build-candidate.log'
$candidate = Join-Path $root 'codex2api.new.exe'

function Invoke-CheckedStep {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][scriptblock]$Command
    )

    & $Command *> $log
    if ($LASTEXITCODE -ne 0) {
        Write-Output "FAILED: $Name"
        Get-Content -LiteralPath $log -Tail 80
        exit $LASTEXITCODE
    }
    Write-Output "PASS: $Name"
}

Set-Location -LiteralPath $root
Invoke-CheckedStep 'frontend typecheck' { npm --prefix frontend run typecheck }
Invoke-CheckedStep 'frontend build' { npm --prefix frontend run build }
Invoke-CheckedStep 'go test ./...' { go test ./... -count=1 }
Invoke-CheckedStep 'go vet ./...' { go vet ./... }

$env:CGO_ENABLED = '0'
Invoke-CheckedStep 'candidate build' {
    go build -trimpath -ldflags '-s -w' -o $candidate .
}

$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $candidate).Hash
$size = (Get-Item -LiteralPath $candidate).Length
Remove-Item -LiteralPath $log -Force -ErrorAction SilentlyContinue
Write-Output "CANDIDATE_SHA256=$hash"
Write-Output "CANDIDATE_BYTES=$size"
