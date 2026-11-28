//go:build !windows

package launch

func prepareLauncher() bool    { return false }
func showLauncherError(string) {}
