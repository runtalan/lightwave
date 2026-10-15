//go:build !windows

package tapo

import "os/exec"

func hide(*exec.Cmd) {}
