package winapp

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"

	"campusautologin/internal/logx"
)

// Actions 是托盘菜单触发的业务回调。
type Actions struct {
	OnCheck           func()
	OnLogin           func()
	OnOpenLogs        func()
	OnOpenFolder      func()
	OnToggleAutostart func() bool // 返回切换后的状态
	IsAutostartOn     func() bool
	OnQuit            func()
}

// Options 是创建窗口的参数。
type Options struct {
	Title string
	// HTML 直接交给 WebView 渲染（NavigateToString）。
	// 前端资源被合成一个自包含文档，因此不需要本地服务、不需要端口。
	HTML string
	// OnMessage 处理页面发来的消息，返回要执行的 JS（空串表示不回）。
	OnMessage   func(msg string) string
	Width       int
	Height      int
	MinWidth    int
	MinHeight   int
	DataPath    string // WebView2 用户数据目录
	Debug       bool
	StartHidden bool
	Actions     Actions
}

// App 是托盘 + 窗口的整体。
type App struct {
	opts Options

	hwnd     uintptr
	normalX  int
	normalY  int
	chromium *edge.Chromium

	// 按 DPI 换算后的物理像素尺寸（CreateWindowEx 只认物理像素）
	pxW, pxH          int
	pxMinW, pxMinH    int
	nid               notifyIconData
	hicon             windows.Handle
	taskbarCreatedMsg uint32

	visible atomic.Bool
	quitReq atomic.Bool
	isTool  atomic.Bool // 当前是否为工具窗样式

	statusMu  sync.Mutex
	status    Status
	statusTip string

	// EvalAsync 通过 UI 线程队列执行脚本，避免后台 goroutine 直接触碰 COM。
	evalCh chan string

	menuCmd int
}

// 菜单命令 ID
const (
	cmdShow = 1001 + iota
	cmdCheck
	cmdLogin
	cmdLogs
	cmdFolder
	cmdAutostart
	cmdQuit
)

// 托盘回调 / 窗口自定义消息
const (
	wmApplyStatus = WM_APP + 3
	wmShowFromAny = WM_APP + 4
	wmEval        = WM_APP + 5
)

const windowClass = "CampusAutoLoginWnd"
const trayUID = 1
const timerStatusID = 1

var (
	current   *App
	currentMu sync.Mutex
)

// New 创建 App（未启动）。
func New(opts Options) *App {
	if opts.Width == 0 {
		opts.Width = 1040
	}
	if opts.Height == 0 {
		opts.Height = 720
	}
	if opts.MinWidth == 0 {
		opts.MinWidth = 900
	}
	if opts.MinHeight == 0 {
		opts.MinHeight = 600
	}

	// CSS 像素 -> 物理像素
	scale := systemScale()
	a := &App{
		opts:   opts,
		status: StatusSuspend, statusTip: "正在启动…",
		evalCh: make(chan string, 256),
	}
	a.pxW = scalei(opts.Width, scale)
	a.pxH = scalei(opts.Height, scale)
	a.pxMinW = scalei(opts.MinWidth, scale)
	a.pxMinH = scalei(opts.MinHeight, scale)
	return a
}

// Run 创建窗口与托盘并进入消息循环。阻塞，直到用户选择退出。
//
// 必须在独立 goroutine 里调用并 LockOSThread —— GetMessage 只能在与
// 创建窗口相同的线程上跑，而 Go 默认会在系统线程之间调度 goroutine。
func (a *App) Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	currentMu.Lock()
	current = a
	currentMu.Unlock()

	setDPIAware()

	hinst, _, _ := procGetModuleHandleW.Call(0)

	// ---- 注册窗口类 ----
	className := utf16Ptr(windowClass)
	cursor, _, _ := procLoadCursorW.Call(0, IDC_ARROW)

	// 从 exe 资源里取图标（app.rc 里的 ID 1）。
	// 类图标只影响新创建的窗口，个别情况下系统不认，所以创建完
	// 还会用 WM_SETICON 再显式设一次。
	bigIcon := loadResIcon(hinst, 32)
	smallIcon := loadResIcon(hinst, 16)
	if bigIcon == 0 && smallIcon == 0 {
		logx.Log(logx.Warn, "资源图标加载失败，标题栏/任务栏将使用默认图标")
	}

	wc := wndClassEx{
		CbSize:      uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc: windows.NewCallback(wndProc),
		HInstance:   windows.Handle(hinst),
		HIcon:       bigIcon,
		HIconSm:     smallIcon,
		HCursor:     windows.Handle(cursor),
		// 用界面底色而不是白色：WebView 出首帧之前用户看到的是这个颜色，
		// 白底会闪一下，浅灰底与页面无缝衔接。
		HbrBackground: appBgBrush(),
		LpszClassName: className,
	}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("注册窗口类失败: %v", err)
	}

	// ---- 创建窗口 ----
	// 关键：创建成「可见但在屏幕外」。
	// 之前试过不带 WS_VISIBLE 的隐藏窗口，WebView2 会一直白屏 ——
	// 它需要一个真正可见的宿主窗口才会建立合成表面。
	// 屏幕外 + WS_EX_TOOLWINDOW：用户看不到、任务栏也没有多余图标。
	x, y := a.centerPos()
	a.normalX, a.normalY = x, y
	off := offscreenPos()

	title := utf16Ptr(a.opts.Title)
	hwnd, _, err := procCreateWindowExW.Call(
		WS_EX_TOOLWINDOW,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		WS_OVERLAPPEDWINDOW|WS_VISIBLE,
		off, off,
		uintptr(a.pxW), uintptr(a.pxH),
		0, 0, hinst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("创建窗口失败: %v", err)
	}
	a.hwnd = hwnd

	// 显式设置窗口图标（标题栏 + 任务栏 + Alt+Tab 都取这里）
	if bigIcon != 0 {
		procSendMessageW.Call(hwnd, WM_SETICON, ICON_BIG, uintptr(bigIcon))
	}
	if smallIcon != 0 {
		procSendMessageW.Call(hwnd, WM_SETICON, ICON_SMALL, uintptr(smallIcon))
	}

	// 创建时就是工具窗样式，必须同步记下来，
	// 否则 setToolWindow(false) 会以为"已经是普通窗口"直接返回，
	// 结果标题栏一直没有最小化/最大化按钮。
	a.isTool.Store(true)

	// ---- WebView2 在隐藏状态下初始化（用户看不到任何过程）----
	a.chromium = edge.NewChromium()
	if a.opts.DataPath != "" {
		a.chromium.DataPath = a.opts.DataPath
	}
	if !a.chromium.Embed(hwnd) {
		// WebView2 运行时缺失是最常见的失败原因
		procMessageBoxW.Call(hwnd,
			uintptr(unsafe.Pointer(utf16Ptr(
				"未检测到 WebView2 运行时，界面无法显示。\n\n"+
					"Windows 11 与较新的 Windows 10 已自带；\n"+
					"若缺失，请安装「Microsoft Edge WebView2 Runtime」后重试。\n\n"+
					"（认证功能不受影响，程序仍在托盘运行）"))),
			uintptr(unsafe.Pointer(utf16Ptr("校园网认证"))),
			MB_OK|MB_ICONERROR)
	} else {
		if settings, err := a.chromium.GetSettings(); err == nil {
			_ = settings.PutAreDefaultContextMenusEnabled(a.opts.Debug)
			_ = settings.PutAreDevToolsEnabled(a.opts.Debug)
			_ = settings.PutIsStatusBarEnabled(false)
			_ = settings.PutAreBrowserAcceleratorKeysEnabled(a.opts.Debug)
			_ = settings.PutIsZoomControlEnabled(a.opts.Debug)
		}
		if a.opts.OnMessage != nil {
			a.chromium.MessageCallback = func(msg string) {
				// 页面消息可能触发网络探测、登录、注销，不能占用 UI 线程；
				// 否则点击动作时 WebView 会短暂进入“未响应”。
				go func(msg string) {
					if js := a.opts.OnMessage(msg); js != "" {
						a.EvalAsync(js)
					}
				}(msg)
			}
		}
		if a.opts.HTML != "" {
			a.chromium.NavigateToString(a.opts.HTML)
		}
		// 关键：窗口是隐藏创建的，WM_SIZE 在 Embed 之前就发过了，
		// 控制器不会自己拿到尺寸。不主动 Resize 一次，界面会是全白。
		a.chromium.Resize()
	}

	// ---- 托盘 ----
	createdMsg, _, _ := procRegisterWindowMsgW.Call(
		uintptr(unsafe.Pointer(utf16Ptr("TaskbarCreated"))))
	a.taskbarCreatedMsg = uint32(createdMsg)
	a.addTray()

	if !a.opts.StartHidden {
		a.Show()
	}

	// ---- 消息循环 ----
	var msg msgT
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = 错误
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	a.removeTray()
	if a.opts.Actions.OnQuit != nil {
		a.opts.Actions.OnQuit()
	}
	return nil
}

// ---------------------------------------------------------------- 窗口操作

func (a *App) centerPos() (int, int) {
	sw, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	sh, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
	x := (int(sw) - a.pxW) / 2
	y := (int(sh) - a.pxH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// Show 显示并置前窗口。可从任意 goroutine 调用。
func (a *App) Show() {
	if a.hwnd == 0 {
		return
	}
	procPostMessageW.Call(a.hwnd, uintptr(wmShowFromAny), 0, 0)
}

func (a *App) doShow() {
	if a.hwnd == 0 {
		return
	}
	// 切回普通窗口：工具窗（WS_EX_TOOLWINDOW）只有关闭按钮、没有最小化/最大化，
	// 但预热时又必须用它把窗口藏出任务栏。所以按需切换。
	a.setToolWindow(false)

	// 从屏幕外挪回正常位置并置前
	procSetWindowPos.Call(a.hwnd, HWND_TOP,
		uintptr(a.normalX), uintptr(a.normalY),
		uintptr(a.pxW), uintptr(a.pxH),
		SWP_SHOWWINDOW)
	procShowWindow.Call(a.hwnd, SW_SHOWNORMAL)
	procSetForegroundWindow.Call(a.hwnd)
	procUpdateWindow.Call(a.hwnd)
	// 尺寸变化不一定触发 WM_SIZE（窗口一直可见），补一次布局
	if a.chromium != nil {
		a.chromium.Resize()
	}
	a.visible.Store(true)
}

// 隐藏到「屏幕外」而不是 SW_HIDE：
// 保持窗口可见状态，WebView2 的渲染管线就不会停，下次显示是纯合成，零等待。
func (a *App) doHide() {
	if a.hwnd == 0 {
		return
	}
	off := offscreenPos()
	procSetWindowPos.Call(a.hwnd, 0, off, off, 0, 0,
		SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE)
	// 藏起来时切回工具窗，避免任务栏留一个空按钮
	a.setToolWindow(true)
	a.visible.Store(false)
}

// loadResIcon 从当前 exe 的资源里加载图标（ID 1）。
func loadResIcon(hinst uintptr, size int) windows.Handle {
	h, _, _ := procLoadImageW.Call(hinst, 1, IMAGE_ICON,
		uintptr(size), uintptr(size), 0)
	return windows.Handle(h)
}

// gwlExStyle 返回 GWL_EXSTYLE 的传参形式（-20 需按 32 位有符号截断）。
func gwlExStyle() uintptr {
	v := int32(GWL_EXSTYLE)
	return uintptr(uint32(v))
}

// setToolWindow 切换「工具窗 / 普通窗口」。
//
// WS_EX_TOOLWINDOW 的副作用是标题栏只有关闭按钮，且不进任务栏 ——
// 预热阶段正好需要（不能让用户看到），真正显示时要还原成普通窗口。
func (a *App) setToolWindow(tool bool) {
	if a.hwnd == 0 || a.isTool.Load() == tool {
		return
	}
	ex, _, _ := procGetWindowLongPtrW.Call(a.hwnd, gwlExStyle())
	style := uintptr(uint32(ex))
	if tool {
		style |= WS_EX_TOOLWINDOW
		style &^= WS_EX_APPWINDOW
	} else {
		style &^= WS_EX_TOOLWINDOW
		style |= WS_EX_APPWINDOW
	}
	procSetWindowLongPtrW.Call(a.hwnd, gwlExStyle(), style)
	procSetWindowPos.Call(a.hwnd, 0, 0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_NOZORDER|SWP_FRAMECHANGED)
	a.isTool.Store(tool)
}

// Quit 退出程序。可从任意 goroutine 调用。
func (a *App) Quit() {
	a.quitReq.Store(true)
	if a.hwnd != 0 {
		procPostMessageW.Call(a.hwnd, WM_CLOSE, 0, 0)
	}
}

// ---------------------------------------------------------------- 托盘

func (a *App) addTray() {
	a.hicon = a.iconFor(a.currentStatus())

	nid := notifyIconData{
		HWnd:             windows.Handle(a.hwnd),
		UID:              trayUID,
		UFlags:           NIF_MESSAGE | NIF_ICON | NIF_TIP | NIF_SHOWTIP,
		UCallbackMessage: WM_TRAYICON,
		HIcon:            a.hicon,
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	utf16Into(nid.SzTip[:], a.tipText())

	a.nid = nid
	procShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&a.nid)))

	// 让单击就触发回调（NOTIFYICON_VERSION_4）
	v := notifyIconData{
		HWnd:     windows.Handle(a.hwnd),
		UID:      trayUID,
		UVersion: NOTIFYICON_VERSION_4,
	}
	v.CbSize = uint32(unsafe.Sizeof(v))
	procShellNotifyIconW.Call(NIM_SETVERSION, uintptr(unsafe.Pointer(&v)))
}

func (a *App) removeTray() {
	if a.nid.CbSize == 0 {
		return
	}
	procShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&a.nid)))
	if a.hicon != 0 {
		procDestroyIcon.Call(uintptr(a.hicon))
		a.hicon = 0
	}
}

// iconFor 按状态生成 HICON。
//
// 尺寸按当前 DPI 生成（100% -> 16px，125% -> 20px，150% -> 24px），
// 交给系统缩放会让 16px 的细弧线糊成一团。
func (a *App) iconFor(s Status) windows.Handle {
	px := scalei(16, systemScale())
	if px < 16 {
		px = 16
	}
	if px > 32 {
		px = 32
	}
	data := PNGFor(px, s)
	h, _, _ := procCreateIconFromRes.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		1,          // fIcon
		0x00030000, // 版本
		uintptr(px), uintptr(px),
		0,
	)
	return windows.Handle(h)
}

func (a *App) currentStatus() Status {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	return a.status
}

func (a *App) tipText() string {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	t := a.statusTip
	if len(t) > 120 {
		t = t[:120]
	}
	return t
}

// EvalAsync 把脚本交给 UI 线程执行。可从任意 goroutine 调用。
//
// WebView2 的 COM 对象应在创建它的 UI 线程上操作；这里只排队并把
// 消息发给窗口，真正的 ExecuteScript 由 wndProc 在 UI 线程上执行。
func (a *App) EvalAsync(script string) {
	if a == nil || a.hwnd == 0 || a.evalCh == nil {
		return
	}
	select {
	case a.evalCh <- script:
		procPostMessageW.Call(a.hwnd, uintptr(wmEval), 0, 0)
	default:
		logx.Log(logx.Warn, "界面回包队列已满，丢弃一次界面响应")
	}
}

// SetStatus 更新托盘图标与提示。可从任意 goroutine 调用。
func (a *App) SetStatus(s Status, tip string) {
	a.statusMu.Lock()
	changed := a.status != s
	a.status = s
	if tip != "" {
		a.statusTip = tip
	}
	a.statusMu.Unlock()

	if changed && a.hwnd != 0 {
		// 让 wndproc 在 UI 线程上换图标
		procPostMessageW.Call(a.hwnd, wmApplyStatus, 0, 0)
	} else if a.hwnd != 0 {
		a.applyTipOnly()
	}
}

func (a *App) applyTipOnly() {
	if a.nid.CbSize == 0 {
		return
	}
	utf16Into(a.nid.SzTip[:], a.tipText())
	procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&a.nid)))
}

func (a *App) applyStatus() {
	if a.nid.CbSize == 0 {
		return
	}
	newIcon := a.iconFor(a.currentStatus())
	if newIcon != 0 {
		old := a.hicon
		a.hicon = newIcon
		a.nid.HIcon = newIcon
		if old != 0 {
			procDestroyIcon.Call(uintptr(old))
		}
	}
	utf16Into(a.nid.SzTip[:], a.tipText())
	a.nid.UFlags |= NIF_ICON | NIF_TIP
	procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&a.nid)))
}

// Notify 弹一个气泡通知。
func (a *App) Notify(title, body string) {
	if a.hwnd == 0 || a.nid.CbSize == 0 {
		return
	}
	utf16Into(a.nid.SzInfoTitle[:], title)
	utf16Into(a.nid.SzInfo[:], body)
	a.nid.DwInfoFlags = NIIF_INFO
	if r, _, _ := procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&a.nid))); r == 0 {
		// 托盘还没准备好（例如 Explorer 刚重启），补一次
		procShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&a.nid)))
		procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&a.nid)))
	}
}

// ---------------------------------------------------------------- 右键菜单

func (a *App) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	add := func(id int, text string, disabled, checked bool) {
		flags := uint32(MF_STRING)
		if disabled {
			flags |= MF_GRAYED
		}
		if checked {
			flags |= MF_CHECKED
		}
		procAppendMenuW.Call(menu, uintptr(flags), uintptr(id),
			uintptr(unsafe.Pointer(utf16Ptr(text))))
	}
	sep := func() {
		procAppendMenuW.Call(menu, MF_SEPARATOR, 0, 0)
	}

	add(cmdShow, "打开主界面", false, false)
	sep()
	add(cmdCheck, "立即检测", false, false)
	add(cmdLogin, "立即认证", false, false)
	add(cmdLogs, "查看日志", false, false)
	add(cmdFolder, "打开程序目录", false, false)
	on := a.opts.Actions.IsAutostartOn != nil && a.opts.Actions.IsAutostartOn()
	add(cmdAutostart, "开机自启", false, on)
	sep()
	add(cmdQuit, "退出", false, false)

	procSetMenuDefaultItem.Call(menu, cmdShow, 0)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// 必须先置前，否则点菜单外部无法关闭（Win32 的经典坑）
	procSetForegroundWindow.Call(a.hwnd)

	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		TPM_RIGHTBUTTON|TPM_RETURNCMD,
		uintptr(pt.X), uintptr(pt.Y),
		0, a.hwnd, 0,
	)
	procPostMessageW.Call(a.hwnd, WM_NULL, 0, 0)

	if cmd != 0 {
		a.handleCommand(int(cmd))
	}
}

func (a *App) handleCommand(id int) {
	switch id {
	case cmdShow:
		a.doShow()
	case cmdCheck:
		if a.opts.Actions.OnCheck != nil {
			go a.opts.Actions.OnCheck()
		}
	case cmdLogin:
		if a.opts.Actions.OnLogin != nil {
			go a.opts.Actions.OnLogin()
		}
	case cmdLogs:
		if a.opts.Actions.OnOpenLogs != nil {
			go a.opts.Actions.OnOpenLogs()
		}
	case cmdFolder:
		if a.opts.Actions.OnOpenFolder != nil {
			go a.opts.Actions.OnOpenFolder()
		}
	case cmdAutostart:
		if a.opts.Actions.OnToggleAutostart != nil {
			go func() {
				on := a.opts.Actions.OnToggleAutostart()
				tip := "已关闭开机自启"
				if on {
					tip = "已开启开机自启"
				}
				a.Notify("校园网认证", tip)
			}()
		}
	case cmdQuit:
		a.Quit()
	}
}

// ---------------------------------------------------------------- 窗口过程

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	// 回调跨越 Go/系统边界，panic 会直接崩进程，必须兜住
	defer func() {
		if r := recover(); r != nil {
			procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		}
	}()

	currentMu.Lock()
	a := current
	currentMu.Unlock()

	if a == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	}

	// Explorer 重启后托盘图标会消失，需要重新注册
	if a.taskbarCreatedMsg != 0 && uint32(msg) == a.taskbarCreatedMsg {
		a.nid.CbSize = 0
		a.addTray()
		return 0
	}

	switch uint32(msg) {
	case WM_TRAYICON:
		// NOTIFYICON_VERSION_4 下 lParam 低位是事件，高位的坐标不用管
		ev := uint32(lparam) & 0xFFFF
		switch ev {
		case WM_LBUTTONUP, WM_LBUTTONDBLCLK:
			// 单击即拉起 —— 用户明确要求「单击迅速拉起」
			if a.visible.Load() {
				a.doShow() // 已在前面则再置前一次
			} else {
				a.doShow()
			}
		case WM_RBUTTONUP, WM_CONTEXTMENU:
			a.showMenu()
		}
		return 0

	case wmShowFromAny:
		a.doShow()
		return 0

	case wmApplyStatus:
		a.applyStatus()
		return 0

	case wmEval:
		select {
		case js := <-a.evalCh:
			if a.chromium != nil {
				a.chromium.Eval(js)
			}
		default:
		}
		return 0

	case WM_CLOSE:
		// 关闭窗口 = 隐藏到托盘，认证继续跑
		if a.quitReq.Load() {
			procDestroyWindow.Call(hwnd)
		} else {
			a.doHide()
		}
		return 0

	case WM_DESTROY:
		procPostQuitMessage.Call(0)
		return 0

	case WM_SIZE:
		if a.chromium != nil {
			a.chromium.Resize()
		}
		return 0

	case WM_GETMINMAXINFO:
		mmi := (*minMaxInfo)(unsafe.Pointer(lparam))
		mmi.MinTrackSize.X = int32(a.pxMinW)
		mmi.MinTrackSize.Y = int32(a.pxMinH)
		return 0

	case WM_DPICHANGED:
		// lParam 指向系统建议的新窗口矩形，照它调整才能在不同 DPI 显示器间正确缩放
		if lparam != 0 {
			r := (*rect)(unsafe.Pointer(lparam))
			procSetWindowPos.Call(hwnd, 0,
				uintptr(r.Left), uintptr(r.Top),
				uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top),
				SWP_NOZORDER)
		}
		if a.chromium != nil {
			a.chromium.Resize()
		}
		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// ---------------------------------------------------------------- 杂项

func setDPIAware() {
	// -4 = DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
	if r, _, _ := procSetDPICtx.Call(^uintptr(3)); r == 0 {
		procSetProcessDPIAware.Call()
	}
}

// SingleInstance 用命名互斥量保证只跑一个实例。
// 返回 already=true 表示已有实例在运行（调用方应通知它显示窗口后退出）。
func SingleInstance(name string) (handle windows.Handle, already bool) {
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16Ptr(name))))
	if h == 0 {
		return 0, false
	}
	if errno, ok := err.(windows.Errno); ok && errno == ERROR_ALREADY_EXISTS {
		return windows.Handle(h), true
	}
	return windows.Handle(h), false
}

// NotifyExisting 找到已有实例的窗口并请它显示界面。
func NotifyExisting() bool {
	cls := utf16Ptr(windowClass)
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if h == 0 {
		return false
	}
	procPostMessageW.Call(h, uintptr(wmShowFromAny), 0, 0)
	procSetForegroundWindow.Call(h)
	return true
}

// ShellOpen 用系统默认程序打开文件或文件夹。
//
// 用 ShellExecuteW 而不是起一个辅助进程：后者每次都会多创建一个进程
// （还会被安全软件盯上），前者是系统调用，没有窗口、没有控制台。
func ShellOpen(path string) error {
	op := utf16Ptr("open")
	target := utf16Ptr(path)
	if op == nil || target == nil {
		return errBadPath
	}
	r, _, err := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(op)),
		uintptr(unsafe.Pointer(target)),
		0, 0, SW_SHOWNORMAL)
	if r <= 32 { // ShellExecute 的返回值 <= 32 均表示失败
		return err
	}
	return nil
}

var errBadPath = &shellErr{"路径无效"}

type shellErr struct{ msg string }

func (e *shellErr) Error() string { return e.msg }

// MessageBox 弹出原生提示框。
func MessageBox(title, body string) {
	procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr(body))),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		MB_OK|MB_ICONINFORMATION)
}
