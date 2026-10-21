package main

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
	"time"

	"github.com/spf13/cobra"
	"vista/internal/appdirs"
	"vista/internal/instance"
	"vista/internal/platform"
	"vista/internal/server"
	"vista/internal/worker"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var port int
	root := &cobra.Command{Use: "vista", Short: "Vista local agent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return start(port) }}
	root.CompletionOptions.DisableDefaultCmd = true
	addPortFlag(root, &port)
	startCommand := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return start(port) }}
	addPortFlag(startCommand, &port)
	root.AddCommand(startCommand)
	root.AddCommand(&cobra.Command{Use: "stop", RunE: func(*cobra.Command, []string) error { return stop() }})
	restartCommand := &cobra.Command{Use: "restart", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if err := stop(); err != nil {
			return err
		}
		return start(port)
	}}
	addPortFlag(restartCommand, &port)
	root.AddCommand(restartCommand)
	root.AddCommand(&cobra.Command{Use: "status", RunE: func(*cobra.Command, []string) error { return status() }})
	serveCommand := &cobra.Command{Use: "serve", Hidden: true, Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return server.Run(port) }}
	addPortFlag(serveCommand, &port)
	root.AddCommand(serveCommand)
	root.AddCommand(&cobra.Command{Use: "worker", Hidden: true, RunE: func(*cobra.Command, []string) error { return worker.Run() }})
	return root
}

func addPortFlag(command *cobra.Command, port *int) {
	command.Flags().IntVar(port, "port", 0, fmt.Sprintf("HTTP port (VISTA_PORT or server.json; default %d)", server.DefaultPort))
	command.PreRunE = func(command *cobra.Command, _ []string) error {
		if command.Flags().Changed("port") && (*port < 1 || *port > 65535) {
			return errors.New("--port must be from 1 to 65535")
		}
		configDir, err := appdirs.Config()
		if err != nil {
			return err
		}
		config, err := server.LoadRuntimeConfig(configDir, *port)
		if err == nil {
			*port = config.Port
		}
		return err
	}
}
func dir() (string, error) { return instance.Dir() }
func probe(dir string) (instance.Info, bool) {
	x, err := instance.Read(dir)
	if err != nil || !instance.IsLoopbackAddress(x.Address) || !instance.IsAlive(x.PID) {
		return x, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", x.Address+"/api/status", nil)
	if err != nil {
		return x, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return x, false
	}
	defer resp.Body.Close()
	return x, resp.StatusCode == 200
}
func start(portOverride int) error {
	configDir, err := appdirs.Config()
	if err != nil {
		return err
	}
	config, err := server.LoadRuntimeConfig(configDir, portOverride)
	if err != nil {
		return err
	}
	d, err := dir()
	if err != nil {
		return err
	}
	if x, ok := probe(d); ok {
		printStartupURL(x.Address + "/")
		return nil
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(d, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	proc := exec.Command(exe, "serve", "--port", fmt.Sprint(config.Port))
	proc.Stdin = nil
	proc.Stdout = log
	proc.Stderr = log
	platform.Detach(proc)
	if err := proc.Start(); err != nil {
		return err
	}
	_ = proc.Process.Release()
	for i := 0; i < 80; i++ {
		if x, ok := probe(d); ok {
			printStartupURL(x.Address + "/")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("Vista failed to start; see %s", filepath.Join(d, "server.log"))
}
func request(method, path string) (*http.Response, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	x, ok := probe(d)
	if !ok {
		return nil, os.ErrNotExist
	}
	req, err := http.NewRequest(method, x.Address+path, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
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
		if _, ok := probe(d); !ok {
			lock, err := instance.Lock(d)
			if err == nil {
				_ = instance.Unlock(lock)
				return nil
			}
			if !platform.IsLockBusy(err) {
				return err
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("Vista did not release its instance lock within 30 seconds")
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
		fmt.Fprintln(os.Stderr, "Could not open the browser automatically. Paste the URL above into your browser's address bar, not the terminal.")
		return
	}
	fmt.Fprintln(os.Stderr, "If no browser window opens, paste the URL above into your browser's address bar, not the terminal.")
}

func printStartupURL(url string) {
	fmt.Println(url)
	openBrowser(url)
}
