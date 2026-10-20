package ninja

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a console window flashing up when the CLI runs from a
// process that has a desktop (the "ninja push" command).
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
