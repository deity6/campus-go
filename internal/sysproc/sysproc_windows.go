//go:build windows

package sysproc

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW：进程不创建控制台窗口（比 HideWindow 更彻底，
// HideWindow 只是把窗口藏起来，仍会短暂占用一次窗口创建）。
const createNoWindow = 0x08000000

func setHide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
