//go:build !windows

package sysproc

import "os/exec"

func setHide(cmd *exec.Cmd) {}
