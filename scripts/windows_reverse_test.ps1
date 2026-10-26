#requires -Version 5.1
<#
.SYNOPSIS
  Windows reverse_node SOCKS5 connectivity, download speed and drop detection.
.DESCRIPTION
  This tests the full domestic SOCKS5 -> reverse QUIC/WARP -> VPS exit path.
.EXAMPLE
  .\windows_reverse_test.ps1 -Mode Probe -SocksPort 1081
  .\windows_reverse_test.ps1 -Mode Speed -SocksPort 1081 -SizeMB 20
  .\windows_reverse_test.ps1 -Mode Watch -SocksPort 1081 -IntervalSeconds 15
#>
param(
    [ValidateSet('Probe', 'Speed', 'Watch')]
    [string]$Mode = 'Probe',
    [ValidateRange(1, 65535)]
    [int]$SocksPort = 1081,
    [ValidateRange(1, 200)]
    [int]$SizeMB = 20,
    [ValidateRange(5, 600)]
    [int]$IntervalSeconds = 15
)

$ErrorActionPreference = 'Stop'
$curlExe = (Get-Command curl.exe -ErrorAction Stop).Source
$proxyURL = 'socks5h://127.0.0.1:{0}' -f $SocksPort

function Invoke-StrictProbe {
    param([string]$Address, [int]$TimeoutSeconds = 25)
    $metricsFormat = '%{http_code}|%{size_download}|%{speed_download}|%{time_total}|%{time_connect}'
    $output = & $curlExe --silent --show-error --location --fail --proxy $proxyURL --connect-timeout 10 --max-time $TimeoutSeconds --output NUL --write-out $metricsFormat $Address 2>&1
    $code = $LASTEXITCODE
    if ($code -ne 0) {
        throw ("curl through SOCKS5 failed (exit={0}): {1}" -f $code, ($output -join ' '))
    }
    $line = ($output | Select-Object -Last 1).ToString().Trim()
    if ($line -notmatch '^(\d{3})\|(\d+)\|([\d.]+)\|([\d.]+)\|([\d.]+)$') {
        throw "Unrecognized curl measurement: $line"
    }
    $httpCode = [int]$Matches[1]
    if ($httpCode -lt 200 -or $httpCode -ge 300) {
        throw "The destination returned HTTP $httpCode"
    }
    [pscustomobject]@{
        HTTP = $httpCode
        Bytes = [int64]$Matches[2]
        Seconds = [double]::Parse($Matches[4], [Globalization.CultureInfo]::InvariantCulture)
        ConnectMs = [math]::Round(
            [double]::Parse($Matches[5], [Globalization.CultureInfo]::InvariantCulture) * 1000, 1)
        Mbps = [math]::Round(
            [double]::Parse($Matches[3], [Globalization.CultureInfo]::InvariantCulture) * 8 / 1000000, 2)
    }
}

function Print-Result {
    param([object]$Measurement, [string]$Label)
    $mebibytes = [math]::Round($Measurement.Bytes / 1MB, 2)
    Write-Host ('[{0}] {1}: HTTP {2}, {3} MiB, {4}s, {5} Mbps (TCP connect {6}ms)' -f
        (Get-Date -Format 'HH:mm:ss'), $Label, $Measurement.HTTP,
        $mebibytes, $Measurement.Seconds, $Measurement.Mbps, $Measurement.ConnectMs
    ) -ForegroundColor Green
}

$probeURL = 'https://www.cloudflare.com/cdn-cgi/trace'
Write-Host ("Windows SOCKS5 = 127.0.0.1:{0}; all HTTP traffic MUST go through this proxy." -f $SocksPort)

switch ($Mode) {
    'Probe' {
        try {
            Print-Result (Invoke-StrictProbe -Address $probeURL) 'END-TO-END OK'
        }
        catch {
            Write-Host "[DOWN] No end-to-end SOCKS5 response: $_" -ForegroundColor Red
            exit 1
        }
    }
    'Speed' {
        try {
            $bytes = $SizeMB * 1000000
            $url = 'https://speed.cloudflare.com/__down?bytes={0}' -f $bytes
            $result = Invoke-StrictProbe -Address $url -TimeoutSeconds 150
            if ($result.Bytes -lt ($bytes * 0.9)) {
                throw "Expected ~$bytes bytes but received $($result.Bytes); cannot trust speed"
            }
            Print-Result $result ("SPEED TEST ($SizeMB MB)")
            Write-Host 'This is full-chain average throughput, not QUIC/Brutal-only line rate.' -ForegroundColor DarkGray
        }
        catch {
            Write-Host "[FAILED] Speed measurement failed: $_" -ForegroundColor Red
            exit 1
        }
    }
    'Watch' {
        Write-Host ("Continuous check every {0}s; Ctrl+C to stop." -f $IntervalSeconds) -ForegroundColor Yellow
        $wasUp = $null
        while ($true) {
            try {
                $result = Invoke-StrictProbe -Address $probeURL
                Print-Result $result 'UP'
                if ($wasUp -eq $false) {
                    Write-Host '[RECOVERED] Reverse QUIC/WARP/exit path responds again.' -ForegroundColor Green
                }
                $wasUp = $true
            }
            catch {
                Write-Host ("[{0}] [DOWN] End-to-end SOCKS5 path failed: {1}" -f
                    (Get-Date -Format 'HH:mm:ss'), $_) -ForegroundColor Red
                $wasUp = $false
            }
            Start-Sleep -Seconds $IntervalSeconds
        }
    }
}
