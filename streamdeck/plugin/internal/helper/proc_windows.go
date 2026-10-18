package helper

import (
	"os/exec"
	"syscall"
)

// hide keeps Windows from flashing a console window for the helper.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
