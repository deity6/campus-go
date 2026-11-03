// Package winapp 实现 Windows 原生外壳：托盘图标 + WebView2 窗口 + 消息循环。
//
// 为什么不直接用第三方 webview 封装：那些库在 CreateWindow 之后立刻 ShowWindow，
// 再初始化 WebView2（几百毫秒）。结果就是「预热窗口」会闪一个白框。
// 这里自己建窗口并保持隐藏，WebView2 在隐藏状态下完成初始化，
// 用户点托盘时只剩一次 ShowWindow —— 真正做到秒开且无闪烁。
//
// 所有 Win32 调用都集中在本包，避免业务代码沾平台细节。
package winapp

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modComctl32 = windows.NewLazySystemDLL("comctl32.dll")
)

var (
	procRegisterClassExW     = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW      = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW       = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow        = modUser32.NewProc("DestroyWindow")
	procShowWindow           = modUser32.NewProc("ShowWindow")
	procUpdateWindow         = modUser32.NewProc("UpdateWindow")
	procSetForegroundWindow  = modUser32.NewProc("SetForegroundWindow")
	procGetMessageW          = modUser32.NewProc("GetMessageW")
	procTranslateMessage     = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW     = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage      = modUser32.NewProc("PostQuitMessage")
	procPostMessageW         = modUser32.NewProc("PostMessageW")
	procSendMessageW         = modUser32.NewProc("SendMessageW")
	procSetWindowPos         = modUser32.NewProc("SetWindowPos")
	procGetWindowRect        = modUser32.NewProc("GetWindowRect")
	procGetClientRect        = modUser32.NewProc("GetClientRect")
	procSetWindowTextW       = modUser32.NewProc("SetWindowTextW")
	procSetTimer             = modUser32.NewProc("SetTimer")
	procKillTimer            = modUser32.NewProc("KillTimer")
	procGetCursorPos         = modUser32.NewProc("GetCursorPos")
	procGetSystemMetrics     = modUser32.NewProc("GetSystemMetrics")
	procFindWindowW          = modUser32.NewProc("FindWindowW")
	procRegisterWindowMsgW   = modUser32.NewProc("RegisterWindowMessageW")
	procLoadCursorW          = modUser32.NewProc("LoadCursorW")
	procLoadImageW           = modUser32.NewProc("LoadImageW")
	procCreatePopupMenu      = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW          = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu       = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu          = modUser32.NewProc("DestroyMenu")
	procSetMenuDefaultItem   = modUser32.NewProc("SetMenuDefaultItem")
	procMessageBoxW          = modUser32.NewProc("MessageBoxW")
	procCreateIconFromRes    = modUser32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon          = modUser32.NewProc("DestroyIcon")
	procSystemParametersInfo = modUser32.NewProc("SystemParametersInfoW")
	procSetProcessDPIAware   = modUser32.NewProc("SetProcessDPIAware")
	procSetDPICtx            = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procGetModuleHandleW     = modKernel32.NewProc("GetModuleHandleW")
	procCreateMutexW         = modKernel32.NewProc("CreateMutexW")
	procIsZoomed             = modUser32.NewProc("IsZoomed")
	procGetForegroundWindow  = modUser32.NewProc("GetForegroundWindow")
	procGetDC                = modUser32.NewProc("GetDC")
	procReleaseDC            = modUser32.NewProc("ReleaseDC")
	procGetDpiForWindow      = modUser32.NewProc("GetDpiForWindow")
	procGetWindowLongPtrW    = modUser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW    = modUser32.NewProc("SetWindowLongPtrW")
)

var (
	procGetDeviceCaps    = modGdi32.NewProc("GetDeviceCaps")
	procCreateSolidBrush = modGdi32.NewProc("CreateSolidBrush")
)

var (
	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = modShell32.NewProc("ShellExecuteW")
)

// ---------------------------------------------------------------- 结构体

// wndClassEx 对应 WNDCLASSEXW。
type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

// msgT 对应 MSG。
type msgT struct {
	Hwnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type point struct {
	X, Y int32
}

type rect struct {
	Left, Top, Right, Bottom int32
}

type minMaxInfo struct {
	Reserved     point
	MaxSize      point
	MaxPosition  point
	MinTrackSize point
	MaxTrackSize point
}

// notifyIconData 对应 NOTIFYICONDATAW（Unicode 版，x64 布局）。
type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

// ---------------------------------------------------------------- 常量

const (
	// 窗口消息
	WM_DESTROY       = 0x0002
	WM_CLOSE         = 0x0010
	WM_PAINT         = 0x000F
	WM_SIZE          = 0x0005
	WM_COMMAND       = 0x0111
	WM_TIMER         = 0x0113
	WM_GETMINMAXINFO = 0x0024
	WM_SETICON       = 0x0080
	WM_DPICHANGED    = 0x02E0
	WM_NULL          = 0x0000
	WM_APP           = 0x8000

	// 托盘回调
	WM_TRAYICON = WM_APP + 1
	// 第二个实例请求显示窗口
	WM_SHOWEXISTING = WM_APP + 2

	// 鼠标消息
	WM_LBUTTONUP     = 0x0202
	WM_RBUTTONUP     = 0x0205
	WM_LBUTTONDBLCLK = 0x0203
	WM_CONTEXTMENU   = 0x007B

	// 窗口样式
	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_VISIBLE          = 0x10000000

	// 扩展样式
	WS_EX_TOOLWINDOW = 0x00000080
	WS_EX_APPWINDOW  = 0x00040000

	// Get/SetWindowLongPtr 索引
	GWL_EXSTYLE = -20

	// ShowWindow 命令
	SW_HIDE       = 0
	SW_SHOWNORMAL = 1
	SW_SHOW       = 5
	SW_RESTORE    = 9

	// SetWindowPos
	HWND_TOP         = 0
	SWP_NOSIZE       = 0x0001
	SWP_NOMOVE       = 0x0002
	SWP_NOZORDER     = 0x0004
	SWP_NOACTIVATE   = 0x0010
	SWP_SHOWWINDOW   = 0x0040
	SWP_FRAMECHANGED = 0x0020

	// 系统度量
	SM_CXSCREEN = 0
	SM_CYSCREEN = 1

	// 图标
	IMAGE_ICON      = 1
	LR_DEFAULTSIZE  = 0x0040
	LR_SHARED       = 0x8000
	ICON_SMALL      = 0
	ICON_BIG        = 1
	IDI_APPLICATION = 32512

	// 托盘
	NIM_ADD        = 0
	NIM_MODIFY     = 1
	NIM_DELETE     = 2
	NIM_SETVERSION = 4

	NIF_MESSAGE = 0x0001
	NIF_ICON    = 0x0002
	NIF_TIP     = 0x0004
	NIF_INFO    = 0x0010
	NIF_SHOWTIP = 0x0080

	NIIF_INFO    = 0x0001
	NIIF_WARNING = 0x0002
	NIIF_ERROR   = 0x0003
	NIIF_NOSOUND = 0x0010

	NOTIFYICON_VERSION_4 = 4

	// 菜单
	MF_STRING       = 0x0000
	MF_SEPARATOR    = 0x0800
	MF_GRAYED       = 0x0001
	MF_CHECKED      = 0x0008
	MF_DEFAULT      = 0x1000
	TPM_RIGHTBUTTON = 0x0002
	TPM_RETURNCMD   = 0x0100

	// MessageBox
	MB_OK              = 0x0
	MB_ICONERROR       = 0x10
	MB_ICONINFORMATION = 0x40
	MB_TOPMOST         = 0x40000

	// 光标
	IDC_ARROW = 32512

	ERROR_ALREADY_EXISTS = 183
)

// utf16Ptr 把 Go 字符串转成以 NUL 结尾的 UTF-16 指针。
func utf16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

// utf16Into 把字符串拷进固定长度的 UTF-16 缓冲区。
func utf16Into(dst []uint16, s string) {
	for i := range dst {
		dst[i] = 0
	}
	src, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	n := len(src) - 1
	if n > len(dst)-1 {
		n = len(dst) - 1
	}
	copy(dst[:n], src[:n])
}

var _ = unsafe.Pointer(nil)

// offscreenPos 返回「屏幕外」坐标。窗口保持 WS_VISIBLE 但位于所有显示器之外，
// WebView2 因此能正常初始化并渲染，而用户完全看不到。
// Win32 的 X/Y 参数按 32 位有符号解释，所以这里按 uint32 截断即可。
func offscreenPos() uintptr {
	v := int32(-32100)
	return uintptr(uint32(v))
}

// appBgBrush 创建与界面底色一致的画刷（#f5f7fa）。
//
// 颜色字节序是 0x00BBGGRR，不是 RGB。
func appBgBrush() windows.Handle {
	const colorref = 0x00FAF7F5 // #F5F7FA
	h, _, _ := procCreateSolidBrush.Call(colorref)
	return windows.Handle(h)
}

// systemScale 返回系统 DPI 缩放比（96 DPI 时为 1.0）。
//
// 必须做这个换算：CreateWindowEx 用的是物理像素，而页面布局用的是
// CSS 像素。125% 缩放下若直接把 1040 当物理像素传进去，页面实际只有
// 832 CSS px，会误触发窄屏断点（侧栏文字被折叠）。
func systemScale() float64 {
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return 1
	}
	defer procReleaseDC.Call(0, hdc)
	dpi, _, _ := procGetDeviceCaps.Call(hdc, 88) // LOGPIXELSX
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96.0
}

func scalei(v int, scale float64) int {
	return int(float64(v)*scale + 0.5)
}
