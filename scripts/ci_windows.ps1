param([switch]$Desktop, [switch]$Bootstrap, [string]$Target = 'windows/amd64', [string]$Version = 'dev')

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$output = Join-Path $root '.vista/ci/windows'
$statePath = Join-Path $output 'launch.json'
$resultPath = Join-Path $output 'result.json'
$logPath = Join-Path $output 'desktop.log'

if ($Desktop) {
    $exitCode = 1
    "Desktop validation started: PID $PID, session $((Get-Process -Id $PID).SessionId)" |
        Set-Content -LiteralPath $logPath
    try {
        $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
        foreach ($entry in $state.environment.PSObject.Properties) {
            [Environment]::SetEnvironmentVariable($entry.Name, $entry.Value, 'Process')
        }
        if ($state.start_explorer) {
            Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class VistaCITaskbar {
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern IntPtr FindWindow(string className, string title);
}
'@
            if ([VistaCITaskbar]::FindWindow('Shell_TrayWnd', $null) -eq [IntPtr]::Zero) {
                Start-Process -FilePath (Join-Path $env:WINDIR 'explorer.exe')
                $shellDeadline = [DateTime]::UtcNow.AddSeconds(30)
                while ([VistaCITaskbar]::FindWindow('Shell_TrayWnd', $null) -eq [IntPtr]::Zero) {
                    if ([DateTime]::UtcNow -gt $shellDeadline) { throw 'Explorer did not create an interactive taskbar within 30 seconds.' }
                    Start-Sleep -Milliseconds 250
                }
            }
        }
        Set-Location -LiteralPath $root
        & $state.python -u scripts/ci_native.py --target $state.target --version $state.version *>> $logPath
        $exitCode = $LASTEXITCODE
    } catch {
        $_ | Out-String | Add-Content -LiteralPath $logPath
    } finally {
        @{ exit_code = $exitCode; pid = $PID } | ConvertTo-Json | Set-Content -LiteralPath ($resultPath + '.tmp')
        Move-Item -LiteralPath ($resultPath + '.tmp') -Destination $resultPath -Force
    }
    exit $exitCode
}

if ($Bootstrap) {
    Write-Output "Finding Explorer: PID $PID, session $((Get-Process -Id $PID).SessionId)."
    $shell = New-Object -ComObject Shell.Application
    $desktopHwnd = 0
    $desktopWindow = $shell.Windows().FindWindowSW(0, 0, 8, [ref]$desktopHwnd, 1)
    $powershell = (Get-Process -Id $PID).Path
    $arguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $PSCommandPath + '" -Desktop'
    if ($null -ne $desktopWindow) {
        $desktopWindow.Document.Application.ShellExecute($powershell, $arguments, $root, 'open', 0)
    } else {
        # Windows ARM runners may have no Explorer automation entry. An
        # interactive task runs outside the runner Job without storing a password.
        $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
        $state | Add-Member -NotePropertyName start_explorer -NotePropertyValue $true
        $state | ConvertTo-Json | Set-Content -LiteralPath $statePath
        $action = New-ScheduledTaskAction -Execute $powershell -Argument $arguments -WorkingDirectory $root
        $principal = New-ScheduledTaskPrincipal -UserId ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name) `
            -LogonType Interactive -RunLevel Limited
        $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 22)
        Register-ScheduledTask -TaskName $state.task_name -Action $action -Principal $principal -Settings $settings | Out-Null
        Start-ScheduledTask -TaskName $state.task_name
        Write-Output 'Launched native Windows checks through an interactive scheduled task.'
    }
    exit 0
}

New-Item -ItemType Directory -Force -Path $output | Out-Null
Remove-Item -LiteralPath $resultPath, $logPath -Force -ErrorAction SilentlyContinue
# Explorer must receive the selected toolchain and report paths, without copying
# the runner's credentials or complete environment into a diagnostic artifact.
$environment = @{}
foreach ($name in @('PATH', 'GOPATH', 'GOCACHE', 'GOTOOLCHAIN', 'GOWORK', 'CGO_ENABLED',
        'GITHUB_ACTIONS', 'GITHUB_SHA', 'GITHUB_STEP_SUMMARY', 'RUNNER_OS', 'RUNNER_ARCH',
        'TEMP', 'TMP')) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($null -ne $value) { $environment[$name] = $value }
}
$taskName = 'VistaNativeValidation-' + $PID
@{ python = (Get-Command python).Source; environment = $environment; target = $Target; version = $Version; task_name = $taskName } |
    ConvertTo-Json | Set-Content -LiteralPath $statePath

# The hosted runner's enclosing Job can forbid CREATE_BREAKAWAY_FROM_JOB.
# Launch through Explorer, as a desktop user would, so the real application's
# detach and process-supervision behavior is tested without changing its flags.
$powershell = (Get-Process -Id $PID).Path
Write-Output 'Launching native Windows checks through Explorer.'
# Keep COM discovery outside the waiting process too: an unresponsive Explorer
# must not prevent the startup deadline from being enforced.
$bootstrapLog = Join-Path $output 'bootstrap.log'
$bootstrapError = Join-Path $output 'bootstrap-error.log'
$bootstrapProcess = Start-Process -FilePath $powershell -ArgumentList (
    '-NoProfile -ExecutionPolicy Bypass -File "' + $PSCommandPath + '" -Bootstrap') -PassThru `
    -RedirectStandardOutput $bootstrapLog -RedirectStandardError $bootstrapError

$deadline = [DateTime]::UtcNow.AddMinutes(20)
$startDeadline = [DateTime]::UtcNow.AddSeconds(45)
$linesRead = 0
try {
    while (-not (Test-Path -LiteralPath $resultPath)) {
        if (Test-Path -LiteralPath $logPath) {
            $lines = @(Get-Content -LiteralPath $logPath)
            $lines | Select-Object -Skip $linesRead
            $linesRead = $lines.Count
        } elseif ($bootstrapProcess.HasExited -and $bootstrapProcess.ExitCode -ne 0) {
            throw 'Explorer bootstrap failed; see bootstrap-error.log.'
        } elseif ([DateTime]::UtcNow -gt $startDeadline) {
            throw 'Explorer did not start the native Windows validation process within 45 seconds.'
        }
        if ([DateTime]::UtcNow -gt $deadline) { throw 'Native Windows validation did not finish within 20 minutes.' }
        Start-Sleep -Seconds 1
    }
} finally {
    if (-not $bootstrapProcess.HasExited) { Stop-Process -Id $bootstrapProcess.Id -Force }
    $scheduled = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    if ($null -ne $scheduled) {
        $scheduled | Stop-ScheduledTask -ErrorAction SilentlyContinue
        $scheduled | Unregister-ScheduledTask -Confirm:$false
    }
    Get-Content -LiteralPath $bootstrapLog, $bootstrapError -ErrorAction SilentlyContinue
}
Get-Content -LiteralPath $logPath | Select-Object -Skip $linesRead
$result = Get-Content -LiteralPath $resultPath -Raw | ConvertFrom-Json
exit $result.exit_code
