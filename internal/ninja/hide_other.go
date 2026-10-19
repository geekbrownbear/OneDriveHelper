//go:build !windows

package ninja

import "os/exec"

func hideWindow(*exec.Cmd) {}
