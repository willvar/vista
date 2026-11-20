//go:build !windows

package main

func prepareLauncher() bool    { return false }
func showLauncherError(string) {}
