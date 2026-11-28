package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"vista/internal/appdirs"
	"vista/internal/instance"
	"vista/internal/mcphost"
	"vista/internal/network"
	"vista/internal/platform"
	"vista/internal/server"
	"vista/internal/worker"
)

var desktopLaunch bool

// Local instance control uses an explicit Direct policy, never the user's
// configured proxy or an environment proxy.
var instanceHTTPClient = network.NewClient(http.Client{
	Transport: network.Transport{Base: &http.Transport{}, Policy: &network.Policy{}},
}, network.ApplicationScope)

// Run owns every public entry point. Only the root action enters the desktop
// toolkit; the service, Workers and MCP hosts re-execute this same program.
func Run(openDesktop func(bool) error, startupError func(error)) {
	desktopLaunch = prepareLauncher()
	var openingDesktop bool
	command := newRootCommand(func(tray bool) error {
		openingDesktop = true
		return openDesktop(tray)
	})
	if desktopLaunch {
		command.SilenceErrors, command.SilenceUsage = true, true
	}
	if attempted, err := command.ExecuteC(); err != nil {
		if openingDesktop {
			startupError(err)
		} else if desktopLaunch {
			action := "start"
			if attempted != nil && (attempted.Name() == "restart" || attempted.Name() == "stop") {
				action = attempted.Name()
			}
			message := "Vista could not " + action + ".\n\n" + err.Error()
			if d, dirErr := dir(); dirErr == nil {
				message += "\n\nLog: " + filepath.Join(d, "server.log")
			}
			showLauncherError(message)
		}
		os.Exit(1)
	}
}

func newRootCommand(openDesktop func(bool) error) *cobra.Command {
	cobra.MousetrapHelpText = "" // Allow double-click startup on Windows.
	var port int
	var tray bool
	root := &cobra.Command{Use: "vista", Short: "Vista agent; run without a command to open the desktop", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return openDesktop(tray) }}
	root.CompletionOptions.DisableDefaultCmd = true
	root.Flags().BoolVar(&tray, "tray", false, "Launch in the tray at login")
	_ = root.Flags().MarkHidden("tray")
	// Keep the old CLI startup path for scripts and the desktop's private
	// service launch. New users choose web, serve, or the default desktop.
	startCommand := &cobra.Command{Use: "start", Hidden: true, Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return start(port, true) }}
	addPortFlag(startCommand, &port)
	root.AddCommand(startCommand)
	root.AddCommand(&cobra.Command{Use: "web", Short: "Open the WebUI in the default browser", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			// Local launch must not broaden the default listener.
			if strings.TrimSpace(os.Getenv("VISTA_HOST")) == "" {
				if err := os.Setenv("VISTA_HOST", "127.0.0.1"); err != nil {
					return err
				}
			}
			return start(0, true)
		}})
	root.AddCommand(&cobra.Command{Use: "stop", Short: "Stop the running service", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return stop() }})
	var noBrowser bool
	restartCommand := &cobra.Command{Use: "restart", Short: "Restart the running service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !noBrowser && !cmd.Flags().Changed("port") {
			return restartCurrent()
		}
		// The server's internal controller inherits the existing listener
		// environment. Accept the original internal flags across upgrades.
		if err := stop(); err != nil {
			return err
		}
		return start(port, false)
	}}
	restartCommand.Flags().BoolVar(&noBrowser, "no-browser", false, "Internal restart controller")
	_ = restartCommand.Flags().MarkHidden("no-browser")
	addPortFlag(restartCommand, &port)
	root.AddCommand(restartCommand)
	root.AddCommand(&cobra.Command{Use: "status", Short: "Show service status without starting it", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return status() }})
	serveCommand := &cobra.Command{Use: "serve", Short: "Run the service in the foreground", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		return server.Run(port, startProcessRestart, server.RunOptions{ReservePortRestart: reservePortRestart})
	}}
	addPortFlag(serveCommand, &port)
	root.AddCommand(serveCommand)
	reservedCommand := &cobra.Command{Use: "serve-reserved", Hidden: true, Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return serveReserved(port) }}
	addPortFlag(reservedCommand, &port)
	root.AddCommand(reservedCommand)
	var workerConfigDir string
	workerCommand := &cobra.Command{Use: "worker", Hidden: true, Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if workerConfigDir != "" {
			if err := appdirs.BindWorkerConfigDir(workerConfigDir); err != nil {
				return err
			}
		}
		return worker.Run()
	}}
	workerCommand.Flags().StringVar(&workerConfigDir, "config-dir", "", "Worker configuration directory")
	root.AddCommand(workerCommand)
	root.AddCommand(&cobra.Command{Use: "mcp-host", Hidden: true, Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return mcphost.Run() }})
	return root
}

func addPortFlag(command *cobra.Command, port *int) {
	command.Flags().IntVar(port, "port", 0, fmt.Sprintf("HTTP port (VISTA_PORT or config.json; default %d)", server.DefaultPort))
	_ = command.Flags().MarkHidden("port")
	command.PreRunE = func(command *cobra.Command, _ []string) error {
		if command.Flags().Changed("port") && (*port < 1 || *port > 65535) {
			return errors.New("--port must be from 1 to 65535")
		}
		configDir, err := appdirs.Config()
		if err != nil {
			return err
		}
		_, err = server.PreviewRuntimeConfig(configDir, *port)
		return err
	}
}
func dir() (string, error) { return instance.Dir() }
func readLiveInstance(dir string) (instance.Info, bool) {
	x, err := instance.Read(dir)
	if err != nil || !instance.IsLocalAddress(x.Address) || !instance.IsAlive(x.PID) {
		return x, false
	}
	return x, true
}
func probe(dir string) (instance.Info, bool) {
	x, alive := readLiveInstance(dir)
	if !alive {
		return x, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// Readiness needs only the static UI. Detailed status can involve slower
	// native memory sampling and must not decide whether the service is alive.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, x.Address+"/", nil)
	if err != nil {
		return x, false
	}
	resp, err := instanceHTTPClient.Do(req)
	if err != nil {
		return x, false
	}
	defer resp.Body.Close()
	return x, resp.StatusCode == 200
}
func start(portOverride int, showBrowser bool) error {
	configDir, err := appdirs.Config()
	if err != nil {
		return err
	}
	if _, err := server.PreviewRuntimeConfig(configDir, portOverride); err != nil {
		return err
	}
	d, err := dir()
	if err != nil {
		return err
	}
	if x, ok := probe(d); ok {
		printStartupURL(x.Address+"/", showBrowser)
		return nil
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		return err
	}
	args := []string{"serve"}
	if portOverride != 0 {
		args = append(args, "--port", fmt.Sprint(portOverride))
	}
	proc, err := startDetached(args...)
	if err != nil {
		return err
	}
	_ = proc.Process.Release()
	for i := 0; i < 80; i++ {
		if x, ok := probe(d); ok {
			printStartupURL(x.Address+"/", showBrowser)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("Vista failed to start; see %s", filepath.Join(d, "server.log"))
}

// startDetached is shared by startup and service restart. These
// processes must survive the old Server, unlike managed Workers and their tools.
func startDetached(args ...string) (*exec.Cmd, error) {
	// os.Executable resolves the installed path even after Linux unlinks the
	// running image during replacement; never execute /proc/self/exe instead.
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	d, err := dir()
	if err != nil {
		return nil, err
	}
	output, err := os.OpenFile(filepath.Join(d, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer output.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = output, output
	platform.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func startProcessRestart(port int) (func() error, error) {
	cmd, err := startDetached("restart", "--port", fmt.Sprint(port), "--no-browser")
	if err != nil {
		return nil, err
	}
	return cmd.Wait, nil
}

func request(method, path string) (*http.Response, error) {
	return requestContext(context.Background(), method, path)
}
func requestContext(ctx context.Context, method, path string) (*http.Response, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	x, ok := readLiveInstance(d)
	if !ok {
		return nil, os.ErrNotExist
	}
	req, err := http.NewRequestWithContext(ctx, method, x.Address+path, nil)
	if err != nil {
		return nil, err
	}
	return instanceHTTPClient.Do(req)
}
func stop() error {
	resp, err := request("POST", "/api/stop")
	if errors.Is(err, os.ErrNotExist) {
		d, dirErr := dir()
		if dirErr != nil {
			return dirErr
		}
		if _, statErr := os.Stat(d); statErr == nil {
			if err := waitStopped(d); err != nil {
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		fmt.Println("Vista is stopped")
		return nil
	}
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 202 {
		return fmt.Errorf("stop failed: HTTP %d", resp.StatusCode)
	}
	d, _ := dir()
	if err := waitStopped(d); err != nil {
		return err
	}
	fmt.Println("Vista stopped")
	return nil
}
func waitStopped(d string) error {
	for i := 0; i < 300; i++ {
		// The lock outlives all runtime cleanup; no status request is needed.
		lock, err := instance.Lock(d)
		if err == nil {
			_ = instance.Unlock(lock)
			return nil
		}
		if !platform.IsLockBusy(err) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("Vista did not release its instance lock within 30 seconds")
}

// Restart through the running service so its port and bind address survive a
// restart from a different shell. Never retry an uncertain lifecycle POST.
func restartCurrent() error {
	d, err := dir()
	if err != nil {
		return err
	}
	before, ok := readLiveInstance(d)
	if !ok {
		fmt.Println("Vista is stopped")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, before.Address+"/api/restart", strings.NewReader(`{"confirm":"restart"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := instanceHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not confirm Vista restart: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Vista rejected restart: %s", resp.Status)
	}
	for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if next, ok := readLiveInstance(d); ok && next.Address == before.Address && next.PID != before.PID {
			if ready, ok := probe(d); ok && ready == next {
				fmt.Println("Vista restarted")
				return nil
			}
		}
	}
	return errors.New("Vista accepted restart, but its replacement is not ready")
}

func status() error {
	resp, err := request("GET", "/api/status")
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("Vista is stopped")
		return nil
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status failed: HTTP %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(b))
	return nil
}
func openBrowser(url string) {
	if os.Getenv("VISTA_NO_BROWSER") == "1" {
		return
	}
	if err := platform.OpenBrowser(url); err != nil {
		if desktopLaunch {
			showLauncherError("Vista is running, but could not open the browser.\n\nOpen this address in your browser:\n" + url)
			return
		}
		fmt.Fprintln(os.Stderr, "Could not open the browser automatically. Paste the URL above into your browser's address bar, not the terminal.")
		return
	}
	fmt.Fprintln(os.Stderr, "If no browser window opens, paste the URL above into your browser's address bar, not the terminal.")
}

func printStartupURL(url string, showBrowser bool) {
	fmt.Println(url)
	if showBrowser {
		openBrowser(url)
	}
}
