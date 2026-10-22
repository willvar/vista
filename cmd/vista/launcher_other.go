//go:build !windows

package main

import "context"

func prepareLauncher() bool                       { return false }
func showLauncherError(string)                    {}
func startDesktopControls(context.Context) func() { return nil }
