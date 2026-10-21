//go:build !linux && !windows && !darwin

package platform

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

func HostOS() string                { return runtime.GOOS }
func TryLock(*os.File) error        { return ErrUnsupported }
func IsLockBusy(error) bool         { return false }
func IsAlive(int) bool              { return false }
func Detach(*exec.Cmd)              {}
func OpenBrowser(string) error      { return ErrUnsupported }
func AttachToParent(*exec.Cmd)      {}
func PrepareCommand(*exec.Cmd)      {}
func Start(*exec.Cmd) error         { return ErrUnsupported }
func Terminate(p *os.Process) error { return p.Kill() }
func ShellCommand(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", script)
}
func KillProcessGroup(p *os.Process) error   { return p.Kill() }
func ProcessSignal(*os.ProcessState) string  { return "" }
func KillSessionDescendants(int) error       { return ErrUnsupported }
func WorkerSessionStart(int) (uint64, error) { return 0, ErrUnsupported }
func KillOwnedSession(int, uint64) error     { return ErrUnsupported }
func RSS() uint64                            { return 0 }
func PeakRSS() uint64                        { return 0 }
func AvailableMemory() (uint64, error)       { return 0, ErrUnsupported }
