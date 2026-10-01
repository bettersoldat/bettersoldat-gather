//go:build !windows

package e2e

import "os/exec"

func hideWindow(*exec.Cmd) {}
