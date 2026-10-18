//go:build !windows

package helper

import "os/exec"

func hide(*exec.Cmd) {}
