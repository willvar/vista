package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The native launcher test uses a private browser command, never the user's
// browser or associations. It records the actual URL passed to rundll32.
func TestMain(m *testing.M) {
	if path := os.Getenv("VISTA_LAUNCH_BROWSER_LOG"); path != "" && strings.EqualFold(filepath.Base(os.Args[0]), "rundll32.exe") {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		err = json.NewEncoder(file).Encode(os.Args[1:])
		_ = file.Close()
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNativeWindowsDesktopLauncher(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_BINARY")
	if binary == "" {
		t.Skip("set VISTA_TEST_BINARY to exercise the real application")
	}
	root := t.TempDir()
	config := filepath.Join(root, "vista")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	// Keep the real application offline throughout this test.
	if err := os.WriteFile(filepath.Join(config, "model-catalog-status.json"), []byte(`{"nextCheckAt":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	helpers := filepath.Join(root, "launcher helpers")
	if err := os.MkdirAll(helpers, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Explorer.EXE", "rundll32.exe"} {
		file, err := os.Create(filepath.Join(helpers, name))
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(file, source)
		_ = source.Close()
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	setEnv := func(env []string, key, value string) []string {
		for i := 0; i < len(env); i++ {
			if strings.HasPrefix(strings.ToUpper(env[i]), strings.ToUpper(key)+"=") {
				env = append(env[:i], env[i+1:]...)
				i--
			}
		}
		return append(env, key+"="+value)
	}
	env := setEnv(os.Environ(), "APPDATA", root)
	for _, key := range []string{"VISTA_OPENAI_API_KEY", "VISTA_ANTHROPIC_API_KEY", "VISTA_GEMINI_API_KEY", "VISTA_MISTRAL_API_KEY", "VISTA_DEFAULT_MODEL"} {
		env = setEnv(env, key, "")
	}
	env = setEnv(env, "VISTA_PORT", strconv.Itoa(port))
	env = setEnv(env, "VISTA_NO_BROWSER", "0")
	env = setEnv(env, "VISTA_LAUNCH_BROWSER_LOG", filepath.Join(root, "browser.jsonl"))
	env = setEnv(env, "PATH", helpers+";"+os.Getenv("PATH"))
	cli := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	t.Cleanup(func() {
		if output, err := cli("stop"); err != nil {
			t.Errorf("stop isolated instance: %v %s", err, output)
		}
	})
	launch := func(launchEnv []string) *exec.Cmd {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, filepath.Join(helpers, "Explorer.EXE"), "-test.run=^TestExplorerLaunchHelper$", "--", binary, root)
		cmd.Env = launchEnv
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd
	}
	readPID := func() int {
		t.Helper()
		path := filepath.Join(root, "launcher.pid")
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			data, err := os.ReadFile(path)
			if err == nil {
				pid, _ := strconv.Atoi(string(data))
				if pid > 0 {
					return pid
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("desktop helper did not start Vista")
		return 0
	}
	var serverPID int
	for attempt := 0; attempt < 2; attempt++ {
		cmd := launch(env)
		if err := cmd.Wait(); err != nil {
			t.Fatalf("Explorer launch %d failed: %v", attempt+1, err)
		}
		output, err := cli("status")
		var status struct {
			State      string `json:"state"`
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			Default    string `json:"defaultModel"`
			DefaultKey string `json:"defaultModelKey"`
		}
		if err != nil || json.Unmarshal([]byte(output), &status) != nil || status.State != "running" {
			t.Fatalf("Explorer did not start the isolated instance: %v %s", err, output)
		}
		if status.Provider != "" || status.Model != "" || status.Default != "" || status.DefaultKey != "" {
			t.Fatalf("fresh instance reported an unconfigured model: %s", output)
		}
		data, err := os.ReadFile(filepath.Join(config, "state", "instance.json"))
		if err != nil {
			t.Fatal(err)
		}
		var instance struct{ PID int }
		if err := json.Unmarshal(data, &instance); err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			serverPID = instance.PID
		} else if instance.PID != serverPID {
			t.Fatal("reopening Vista created a second Server")
		}
	}
	until := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(root, "browser.jsonl"))
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) == 2 {
			for _, line := range lines {
				var args []string
				if json.Unmarshal([]byte(line), &args) != nil || len(args) != 2 || args[0] != "url.dll,FileProtocolHandler" || args[1] != fmt.Sprintf("http://127.0.0.1:%d/", port) {
					t.Fatalf("incorrect browser launch: %q", line)
				}
			}
			break
		}
		if time.Now().After(until) {
			t.Fatalf("expected a browser open for both launches, got %q", data)
		}
		time.Sleep(30 * time.Millisecond)
	}
	_ = os.Remove(filepath.Join(root, "launcher.pid"))
	cmd := launch(setEnv(append([]string(nil), env...), "VISTA_PORT", "invalid"))
	pid := readPID()
	t.Cleanup(func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
			_ = process.Release()
		}
	})
	until = time.Now().Add(10 * time.Second)
	for {
		dialog, text := launcherDialog(pid)
		if dialog != 0 {
			if !strings.Contains(text, "Vista could not start") || !strings.Contains(text, "VISTA_PORT") || !strings.Contains(text, "server.log") {
				t.Fatalf("desktop error omitted the cause or log: %q", text)
			}
			_, _, _ = windows.NewLazySystemDLL("user32.dll").NewProc("PostMessageW").Call(dialog, 0x0010, 0, 0) // WM_CLOSE
			break
		}
		if time.Now().After(until) {
			t.Fatal("startup failure did not show a native Vista error dialog")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf("invalid desktop launch did not exit cleanly with status 1: %v", err)
	}
}

func TestExplorerLaunchHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	binary, root := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(filepath.Join(root, "launcher.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			os.Exit(3)
		}
		os.Exit(1)
	}
	os.Exit(0)
}

func launcherDialog(pid int) (uintptr, string) {
	user := windows.NewLazySystemDLL("user32.dll")
	var dialog uintptr
	var text strings.Builder
	windowText := func(window uintptr) string {
		var value [4096]uint16
		var result uintptr
		// GetWindowText cannot read controls belonging to another process.
		_, _, _ = user.NewProc("SendMessageTimeoutW").Call(window, 0x000D, uintptr(len(value)), uintptr(unsafe.Pointer(&value[0])), 0x0002, 200, uintptr(unsafe.Pointer(&result))) // WM_GETTEXT, SMTO_ABORTIFHUNG
		return windows.UTF16ToString(value[:])
	}
	callback := windows.NewCallback(func(window, _ uintptr) uintptr {
		var owner uint32
		_, _, _ = user.NewProc("GetWindowThreadProcessId").Call(window, uintptr(unsafe.Pointer(&owner)))
		if int(owner) == pid && windowText(window) == "Vista" {
			dialog = window
			return 0
		}
		return 1
	})
	_, _, _ = user.NewProc("EnumWindows").Call(callback, 0)
	if dialog != 0 {
		children := windows.NewCallback(func(window, _ uintptr) uintptr {
			text.WriteString(windowText(window))
			return 1
		})
		_, _, _ = user.NewProc("EnumChildWindows").Call(dialog, children, 0)
	}
	return dialog, text.String()
}
