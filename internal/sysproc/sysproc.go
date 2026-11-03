// Package sysproc 负责「静默启动子进程」。
//
// 为什么需要单独一个包：Windows 上 exec.Command("ping", ...) 默认会
// 分配一个控制台窗口，结果是界面上每隔几秒闪一下黑框。
// 必须在 SysProcAttr 里指定 CREATE_NO_WINDOW + HideWindow 才不会闪。
package sysproc

import "os/exec"

// Hide 让 cmd 在 Windows 上不创建控制台窗口；其他平台是空操作。
func Hide(cmd *exec.Cmd) {
	setHide(cmd)
}
