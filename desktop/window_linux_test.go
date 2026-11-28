package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const windowRecoveryChild = "VISTA_TEST_WINDOW_RECOVERY_CHILD"

// Wails/GTK must run on the process's main OS thread, not a testing goroutine.
func TestMain(m *testing.M) {
	if os.Getenv(windowRecoveryChild) == "1" {
		if err := checkGTKWindowRecovery(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestNativeLinuxWindowRecovery(t *testing.T) {
	if os.Getenv("VISTA_TEST_DESKTOP") == "" {
		t.Skip("requires the isolated native desktop check")
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Fatal("requires a Wayland desktop")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), windowRecoveryChild+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GTK window recovery: %v\n%s", err, out)
	}
}

// Use an actual GTK window but inject availability so that testing the loss
// of a panel does not require stopping the user's desktop environment.
func checkGTKWindowRecovery() error {
	d := &desktop{flags: map[string]any{}}
	options := application.Options{Name: "Vista window recovery test"}
	if err := d.platformOptions(&options); err != nil {
		return err
	}
	d.app = application.New(options)
	d.home = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Vista window recovery test", HTML: "<!doctype html><title>Vista</title>",
	})
	d.home.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { d.hideOnClose(d.home, e) })
	done := make(chan error, 1)
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go func() { done <- exerciseGTKWindow(d); d.app.Quit() }()
	})
	if err := d.app.Run(); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	default:
		return errors.New("native window checks did not finish")
	}
}

func exerciseGTKWindow(d *desktop) error {
	visible := func(want bool) bool {
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(25 * time.Millisecond) {
			if d.home.IsVisible() == want {
				return true
			}
		}
		return false
	}
	application.InvokeSync(d.showHome)
	if !visible(true) {
		return errors.New("the desktop window did not show")
	}
	application.InvokeSync(func() { d.trayAvailability(true) })
	d.home.Close()
	if !visible(false) {
		return errors.New("closing with a tray did not hide the desktop window")
	}
	application.InvokeSync(d.showHome)
	if !visible(true) {
		return errors.New("tray activation did not restore the desktop window")
	}
	d.home.Close()
	if !visible(false) {
		return errors.New("the restored window was not hidden a second time")
	}
	application.InvokeSync(func() { d.trayAvailability(false) })
	if !visible(true) {
		return errors.New("losing the tray left the desktop window hidden")
	}
	return nil
}
