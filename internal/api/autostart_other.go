//go:build !windows

package api

// 非 Windows 平台暂不实现开机自启：
// Linux 下应写 ~/.config/autostart/*.desktop，但目标用户目前都在 Windows，
// 先返回明确错误，避免界面显示一个假的「已启用」。

// AutostartGet 返回未启用。
func AutostartGet() (bool, string, error) {
	return false, "", nil
}

// AutostartSet 在非 Windows 平台返回不支持。
func AutostartSet(enable bool) error {
	if !enable {
		return nil
	}
	return errUnsupported
}

type unsupportedError struct{}

func (unsupportedError) Error() string {
	return "当前平台暂不支持开机自启（Windows 版本可用）"
}

var errUnsupported = unsupportedError{}
