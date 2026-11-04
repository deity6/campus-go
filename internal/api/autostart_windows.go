//go:build windows

package api

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	autostartKeyName = "CampusAutoLogin"
	runKeyPath       = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKeyPath  = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// AutostartGet 读取“真正生效”的开机自启状态。
//
// 只看 Run 项是不够的：任务管理器/设置应用还会读 StartupApproved\Run。
// 那里存在一个奇数首字节的二进制值（常见为 03）时，即使 Run 项还在，
// Windows 也会认为这个启动项已被用户禁用。
func AutostartGet() (bool, string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, "", nil
	}
	v, _, err := k.GetStringValue(autostartKeyName)
	k.Close()
	if err != nil {
		if err == registry.ErrNotExist {
			return false, "", nil
		}
		return false, "", err
	}

	disabled, err := startupApprovedDisabled()
	if err != nil {
		return false, "", err
	}
	if disabled {
		return false, v, nil
	}
	return true, v, nil
}

// AutostartSet 写入/删除开机自启项，并同步 Windows 启动审批状态。
//
// 便携版的正确做法是指向 exe 自身的绝对路径（带引号，防空格），
// 而不是某个 .bat —— 用户可能把整个文件夹挪走，此时自启会失效，
// 属于便携式的固有取舍（下次启动时重开一次即可）。
func AutostartSet(enable bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if enable {
		// --autostart 让程序知道这是开机启动：不弹窗，只驻留托盘。
		// 注意这里不能再用 fmt %q：它会把 Windows 路径里的 \ 转义成 \\，
		// 注册表里留下的就不是最自然的可执行路径。
		cmd := "\"" + exe + "\" --autostart"
		if err := k.SetStringValue(autostartKeyName, cmd); err != nil {
			return err
		}
		return setStartupApproved(true)
	}

	if err := k.DeleteValue(autostartKeyName); err != nil && err != registry.ErrNotExist {
		return err
	}
	// Run 项删掉后，审批状态不再有意义；清掉它可以避免启动管理器里
	// 留下一个残影状态。
	if err := clearStartupApproved(); err != nil {
		return err
	}
	return nil
}

// startupApprovedDisabled 判断任务管理器里的启动开关是否为“已禁用”。
//
// 这个二进制值的首字节约定是：偶数（通常 02）= 启用，奇数（通常 03）= 禁用。
// 没有这个值时按系统默认处理：Run 项存在即启用。
func startupApprovedDisabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	defer k.Close()

	v, _, err := k.GetBinaryValue(autostartKeyName)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	return len(v) > 0 && v[0]&1 == 1, nil
}

// setStartupApproved 写入任务管理器可识别的“启用”状态。
//
// 只写 Run 项会让联想管家这类工具看到自启，但任务管理器仍可能维持
// 上一次用户禁用的状态；两边必须一起写才能同步。
func setStartupApproved(enable bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, approvedKeyPath,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if enable {
		approved := make([]byte, 12)
		approved[0] = 2
		return k.SetBinaryValue(autostartKeyName, approved)
	}
	return k.DeleteValue(autostartKeyName)
}

// clearStartupApproved 删除残留的审批状态。
func clearStartupApproved() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	defer k.Close()

	if err := k.DeleteValue(autostartKeyName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}
