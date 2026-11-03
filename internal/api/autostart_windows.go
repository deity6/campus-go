//go:build windows

package api

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	autostartKeyName = "CampusAutoLogin"
	runKeyPath       = `Software\Microsoft\Windows\CurrentVersion\Run`
)

// AutostartGet 读取开机自启状态。
func AutostartGet() (bool, string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, "", err
	}
	defer k.Close()

	v, _, err := k.GetStringValue(autostartKeyName)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, "", nil
		}
		return false, "", err
	}
	return true, v, nil
}

// AutostartSet 写入/删除开机自启项。
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
		// --autostart 让程序知道这是开机启动：不弹窗，只驻留托盘
		cmd := fmt.Sprintf("%q --autostart", exe)
		return k.SetStringValue(autostartKeyName, cmd)
	}
	if err := k.DeleteValue(autostartKeyName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}
