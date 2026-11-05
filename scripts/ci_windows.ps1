param([switch]$Desktop, [switch]$Bootstrap, [switch]$CaptureDesktop, [string]$Target = 'windows/amd64', [string]$Version = 'dev')

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$output = Join-Path $root '.vista/ci/windows'
$statePath = Join-Path $output 'launch.json'
$resultPath = Join-Path $output 'result.json'
$logPath = Join-Path $output 'desktop.log'

function Initialize-TaskbarProbe {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class VistaCITaskbar {
    public static string LastProbe;
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern IntPtr FindWindow(string className, string title);
    [DllImport("user32.dll")] static extern IntPtr GetProcessWindowStation();
    [DllImport("kernel32.dll")] static extern uint GetCurrentThreadId();
    [DllImport("user32.dll")] static extern IntPtr GetThreadDesktop(uint thread);
    [DllImport("user32.dll")] static extern IntPtr OpenInputDesktop(uint flags, bool inherit, uint access);
    [DllImport("user32.dll")] static extern bool CloseDesktop(IntPtr desktop);
    [DllImport("user32.dll")] static extern IntPtr GetShellWindow();
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    static extern bool GetUserObjectInformation(IntPtr obj, int index, System.Text.StringBuilder text, uint size, out uint needed);
    static string ObjectName(IntPtr obj) {
        var name = new System.Text.StringBuilder(256);
        uint needed;
        return obj != IntPtr.Zero && GetUserObjectInformation(obj, 2, name, 512, out needed) ? name.ToString() : "unavailable";
    }
    public static string Context() {
        var input = OpenInputDesktop(0, false, 1);
        try {
            return String.Format("Desktop context: interactive={0}, station={1}, desktop={2}, input={3}, shell={4}",
                Environment.UserInteractive, ObjectName(GetProcessWindowStation()),
                ObjectName(GetThreadDesktop(GetCurrentThreadId())), ObjectName(input), GetShellWindow());
        } finally { if (input != IntPtr.Zero) CloseDesktop(input); }
    }
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    public struct NotifyData {
        public uint Size;
        public IntPtr Window;
        public uint ID, Flags, Callback;
        public IntPtr Icon;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string Tip;
        public uint State, StateMask;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 256)] public string Info;
        public uint Version;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 64)] public string InfoTitle;
        public uint InfoFlags;
        public Guid GUID;
        public IntPtr BalloonIcon;
    }
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    static extern IntPtr CreateWindowEx(uint style, string className, string title, uint windowStyle,
        int x, int y, int width, int height, IntPtr parent, IntPtr menu, IntPtr instance, IntPtr parameter);
    [DllImport("user32.dll")] static extern bool DestroyWindow(IntPtr window);
    [DllImport("user32.dll")] static extern IntPtr LoadIcon(IntPtr instance, IntPtr name);
    [DllImport("shell32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    static extern bool Shell_NotifyIcon(uint message, ref NotifyData data);
    public static bool Probe() {
        var window = CreateWindowEx(0, "STATIC", "Vista CI taskbar probe", 0,
            0, 0, 0, 0, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero);
        if (window == IntPtr.Zero) throw new Exception("Could not create the native taskbar probe window.");
        var data = new NotifyData {
            Size = (uint)Marshal.SizeOf<NotifyData>(), Window = window, ID = 1,
            Flags = 1 | 2 | 4, Callback = 0x8001,
            Icon = LoadIcon(IntPtr.Zero, new IntPtr(32512)), Tip = "Vista CI probe"
        };
        if (data.Icon == IntPtr.Zero) {
            DestroyWindow(window);
            throw new Exception("Could not load the native system icon for the taskbar probe.");
        }
        try {
            bool result = Shell_NotifyIcon(0, ref data);
            LastProbe = String.Format("Native system-icon probe: registered={0}, data_size={1}, window={2}, icon={3}, last_error={4}",
                result, data.Size, window, data.Icon, Marshal.GetLastWin32Error());
            if (result) Shell_NotifyIcon(2, ref data);
            return result;
        } finally { DestroyWindow(window); }
    }
}
'@
}

if ($CaptureDesktop) {
    Add-Type -AssemblyName System.Windows.Forms, System.Drawing
    $screen = [System.Windows.Forms.SystemInformation]::VirtualScreen
    $bitmap = New-Object System.Drawing.Bitmap $screen.Width, $screen.Height
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    try {
        $graphics.CopyFromScreen($screen.Left, $screen.Top, 0, 0, $screen.Size)
        $bitmap.Save((Join-Path $output 'desktop.png'))
    } finally { $graphics.Dispose(); $bitmap.Dispose() }
    exit 0
}

if ($Desktop) {
    $exitCode = 1
    "Desktop validation started: PID $PID, session $((Get-Process -Id $PID).SessionId)" |
        Set-Content -LiteralPath $logPath
    try {
        $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
        "Launch profile before restoration: USERPROFILE=$env:USERPROFILE; APPDATA=$env:APPDATA; LOCALAPPDATA=$env:LOCALAPPDATA" |
            Add-Content -LiteralPath $logPath
        foreach ($entry in $state.environment.PSObject.Properties) {
            [Environment]::SetEnvironmentVariable($entry.Name, $entry.Value, 'Process')
        }
        "Desktop profile: USERPROFILE=$env:USERPROFILE; APPDATA=$env:APPDATA; LOCALAPPDATA=$env:LOCALAPPDATA" |
            Add-Content -LiteralPath $logPath
        if ($env:GITHUB_ACTIONS -eq 'true' -and $state.target -eq 'windows/arm64') {
            # Hosted ARM64 images can leave first-logon privacy setup over
            # the desktop: actions/runner-images#14069. The policy prevents
            # relaunch; WWAHost must also exit if the wizard is already open.
            $policy = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\OOBE'
            New-Item -Path $policy -Force | Out-Null
            New-ItemProperty -Path $policy -Name DisablePrivacyExperience -Value 1 -PropertyType DWord -Force | Out-Null
            Get-Process -Name WWAHost -ErrorAction SilentlyContinue |
                Where-Object SessionId -EQ (Get-Process -Id $PID).SessionId | Stop-Process -Force
            # Closing privacy setup can leave the full-screen Start menu.
            (New-Object -ComObject WScript.Shell).SendKeys('{ESC}')
            'Dismissed the hosted ARM64 first-logon privacy experience.' | Add-Content -LiteralPath $logPath
        }
        if ($state.start_explorer) {
            Initialize-TaskbarProbe
            [VistaCITaskbar]::Context() | Add-Content -LiteralPath $logPath
            if ([VistaCITaskbar]::FindWindow('Shell_TrayWnd', $null) -eq [IntPtr]::Zero) {
                Start-Process -FilePath (Join-Path $env:WINDIR 'explorer.exe') -WindowStyle Normal
                $shellDeadline = [DateTime]::UtcNow.AddSeconds(30)
                while ([VistaCITaskbar]::FindWindow('Shell_TrayWnd', $null) -eq [IntPtr]::Zero) {
                    if ([DateTime]::UtcNow -gt $shellDeadline) { throw 'Explorer did not create an interactive taskbar within 30 seconds.' }
                    Start-Sleep -Milliseconds 250
                }
            }
            # Shell_TrayWnd appears before the notification area can accept
            # icons. Wait for the real API rather than treating window creation
            # as a completed Explorer startup.
            $trayDeadline = [DateTime]::UtcNow.AddSeconds(45)
            while (-not [VistaCITaskbar]::Probe()) {
                if ([DateTime]::UtcNow -gt $trayDeadline) {
                    [VistaCITaskbar]::LastProbe | Add-Content -LiteralPath $logPath
                    [VistaCITaskbar]::Context() | Add-Content -LiteralPath $logPath
                    Get-Process -Name explorer, ShellExperienceHost, ShellHost -IncludeUserName -ErrorAction SilentlyContinue |
                        Select-Object Id, SessionId, UserName, Path | Format-List | Out-String |
                        Add-Content -LiteralPath $logPath
                    [Security.Principal.WindowsIdentity]::GetCurrent().Name | Add-Content -LiteralPath $logPath
                    $frameworkShell = Join-Path $env:WINDIR 'System32/WindowsPowerShell/v1.0/powershell.exe'
                    & $frameworkShell -NoProfile -NonInteractive -STA -ExecutionPolicy Bypass -File $PSCommandPath -CaptureDesktop *>> $logPath
                    throw 'The runner taskbar rejected an independent native system-icon probe for 45 seconds.'
                }
                Start-Sleep -Milliseconds 500
            }
            [VistaCITaskbar]::LastProbe | Add-Content -LiteralPath $logPath
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
        # Windows ARM runners may have no Explorer automation entry. Local WMI
        # can create a process on the user's desktop outside the runner Job.
        # A scheduled task cannot: its own Job also denies process breakaway.
        $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
        $state | Add-Member -NotePropertyName start_explorer -NotePropertyValue $true
        $state | ConvertTo-Json | Set-Content -LiteralPath $statePath
        $startup = New-CimInstance -ClassName Win32_ProcessStartup -ClientOnly -Property @{
            CreateFlags = [uint32]0x01000000 # CREATE_BREAKAWAY_FROM_JOB
            ShowWindow = [uint16]0
            WinstationDesktop = 'winsta0\default'
        }
        $created = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine = '"' + $powershell + '" ' + $arguments
            CurrentDirectory = $root
            ProcessStartupInformation = $startup
        }
        if ($created.ReturnValue -ne 0) { throw "Local WMI desktop launch failed: $($created.ReturnValue)." }
        Write-Output "Launched native Windows checks through local WMI: PID $($created.ProcessId)."
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
        'TEMP', 'TMP', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'USERNAME', 'USERDOMAIN',
        'HOMEDRIVE', 'HOMEPATH')) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($null -ne $value) { $environment[$name] = $value }
}
@{ python = (Get-Command python).Source; environment = $environment; target = $Target; version = $Version } |
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
    Get-Content -LiteralPath $bootstrapLog, $bootstrapError -ErrorAction SilentlyContinue
}
Get-Content -LiteralPath $logPath | Select-Object -Skip $linesRead
$result = Get-Content -LiteralPath $resultPath -Raw | ConvertFrom-Json
exit $result.exit_code
