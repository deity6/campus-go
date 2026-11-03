// Package watcher 是常驻检测循环 —— 整个工具真正干活的地方。
//
// 逻辑沿用 Python 版已在真机跑通的策略，其中几处是踩坑换来的，注释里写清了原因：
//
//  1. 自适应节奏：网络越好越懒。常态 3 分钟一次，出问题才加密到 15 秒，
//     重点时段 5 秒。之前一版为了「断网 30 秒内恢复」把全局间隔压到 10 秒，
//     结果一晚 2800+ 次 ping、日志被心跳刷满 —— 分档才是正解。
//  2. 指数退避：连续登录失败按 5→10→20→40s 递增（封顶 300s）。
//     实测高频重试会触发网关的失败次数保护，可能把账号临时锁住。
//  3. fail_count 在退避期内也必须继续累计，否则通知阈值永远不触发、
//     退避公式永远算出最小值（退避形同虚设）。
//  4. 循环体必须有 recover：任何一次意外 panic 都不能让守护静默死掉 ——
//     Python 版就是这里没兜住，进程还在、检测早停了，掉线永不重连。
package watcher

import (
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"campusautologin/internal/auth"
	"campusautologin/internal/config"
	"campusautologin/internal/logx"
	"campusautologin/internal/portal"
	"campusautologin/internal/state"
)

const (
	// failureThreshold 是常规时段的判定阈值（连续几次异常才认定掉线）。
	// 取 2 是为了抗抖动：偶发丢包不该触发重连。
	failureThreshold = 2

	// recoverGrace 是「刚恢复」的观察期，期内保持加密检测，防反复掉线。
	recoverGrace = 120 * time.Second

	// onlineHeartbeat 限制在线心跳日志频率（否则日志会被刷满）。
	onlineHeartbeat = 30 * time.Minute

	// srcIPRetry 是源 IP 探测失败后的退避窗口。
	srcIPRetry = 15 * time.Second

	// maxBackoff 是重试退避的上限。
	maxBackoff = 300 * time.Second
)

// Notifier 是通知通道的抽象（托盘气泡 / 系统通知）。
type Notifier interface {
	Notify(title, body string)
}

// Watcher 持有循环的全部可变状态。
type Watcher struct {
	mu  sync.RWMutex
	cfg config.Config

	store *state.Store
	notif Notifier

	// 循环内部状态，只有 run 的 goroutine 访问（stopCh 除外）
	failCount    int
	lastRecover  time.Time
	lastHB       time.Time
	backoffUntil time.Time

	// 网关认定的本机 MAC（登录响应里的 olmac）。
	// 网关首页不提供 MAC，只有登录响应有，所以登录时记下来，
	// 之后刷新会话信息时沿用。
	macMu sync.RWMutex
	mac   string

	// 源 IP 缓存
	srcMu       sync.RWMutex
	srcIP       string
	srcFailTill time.Time
	srcWarnAt   time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
	running  atomic.Bool

	// 供 API 触发一次性动作时互斥，避免「手动认证」和「自动重连」同时发请求
	actionMu sync.Mutex
}

// New 创建 Watcher。
func New(cfg config.Config, store *state.Store, notif Notifier) *Watcher {
	return &Watcher{
		cfg:    cfg,
		store:  store,
		notif:  notif,
		stopCh: make(chan struct{}),
	}
}

// Now 返回当前时间（抽出来便于测试注入）。
func (w *Watcher) Now() time.Time { return time.Now() }

// Config 返回当前配置副本。
func (w *Watcher) Config() config.Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cfg
}

// UpdateConfig 热替换配置（界面改设置后立即生效，无需重启）。
func (w *Watcher) UpdateConfig(cfg config.Config) {
	w.mu.Lock()
	w.cfg = cfg
	w.mu.Unlock()
}

// Start 启动守护循环（非阻塞）。
func (w *Watcher) Start() {
	if w.running.Swap(true) {
		return
	}
	go w.run()
}

// Stop 通知循环退出。
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// ---------------------------------------------------------------- 时间窗口

func (w *Watcher) inWatchWindow(now time.Time) bool {
	return w.Config().InWatchWindow(now.Hour())
}

func (w *Watcher) inIntensive(now time.Time) bool {
	cfg := w.Config()
	return cfg.InIntensiveWindow(now.Hour()*60+now.Minute(), now.Hour())
}

// threshold 返回当前判定阈值：重点时段更激进。
func (w *Watcher) threshold(now time.Time) int {
	if w.inIntensive(now) {
		cfg := w.Config()
		if cfg.IntensiveThreshold > 0 {
			return cfg.IntensiveThreshold
		}
		return 1
	}
	return failureThreshold
}

// interval 按「危险程度」分档，返回本轮结束后的睡眠时长。
func (w *Watcher) interval(now time.Time) time.Duration {
	cfg := w.Config()

	// 1. 重点时段：为已知断网窗口准备，最激进
	if w.inIntensive(now) {
		d := time.Duration(cfg.IntensiveInterval) * time.Second
		if d < 3*time.Second {
			d = 3 * time.Second
		}
		return d
	}
	// 2. 自动认证时段外：只兜底
	if !cfg.InWatchWindow(now.Hour()) {
		return time.Duration(cfg.IdleInterval) * time.Second
	}
	// 3. 有异常迹象：赶紧确认
	if w.failCount > 0 {
		return time.Duration(cfg.SuspectInterval) * time.Second
	}
	// 4. 刚恢复的观察期：防反复
	if !w.lastRecover.IsZero() && now.Sub(w.lastRecover) < recoverGrace {
		return time.Duration(cfg.SuspectInterval) * time.Second
	}
	// 5. 稳定在线：常态
	return time.Duration(cfg.CheckInterval) * time.Second
}

// ---------------------------------------------------------------- 主循环

func (w *Watcher) run() {
	cfg := w.Config()
	logx.Log(logx.Info, "守护启动：自动认证 %s（间隔 %ds）／重点 %s（%ds，阈值 %d）",
		cfg.WatchText(), cfg.CheckInterval, cfg.IntensiveText(),
		cfg.IntensiveInterval, cfg.IntensiveThreshold)
	logx.Log(logx.Info, "目标网关 %s  账号 %s", cfg.Gateway, maskAccount(cfg.Account))
	if !cfg.Complete() {
		logx.Log(logx.Warn, "配置不完整（缺网关/账号/密码），自动认证不会生效 —— 请在界面里填写")
	}

	// 首次先拿一次源 IP，界面立刻有值可显示
	if ip := w.refreshSourceIP(); ip != "" {
		logx.Log(logx.Info, "源 IP %s", ip)
	}

	for {
		func() {
			// 每轮独立兜底：单次异常只影响这一轮
			defer func() {
				if r := recover(); r != nil {
					logx.Log(logx.Error, "检测循环异常（已捕获，继续运行）: %v", r)
					logx.Log(logx.Debug, "%s", string(debug.Stack()))
				}
			}()
			w.tick()
		}()

		wait := w.interval(time.Now())
		select {
		case <-w.stopCh:
			logx.Log(logx.Info, "守护已停止")
			return
		case <-time.After(wait):
		}
	}
}

// tick 做一次「检测 + 必要时重连」。
func (w *Watcher) tick() {
	cfg := w.Config()
	now := time.Now()

	watching := cfg.InWatchWindow(now.Hour())
	mayRelogin := watching || cfg.ReloginOutsideWindow

	status := auth.CheckOnline(cfg)
	w.setSrcIP(status.Source)
	w.store.SetNetwork(status.Source, watching, cfg.Complete())
	w.store.SetBackoff(w.currentBackoff())

	if status.Online {
		w.refreshSession(cfg, status.Source)
		if w.failCount > 0 {
			logx.Log(logx.Info, "连接已恢复 —— %s", status.Detail)
			w.setFromStatus(state.PhaseOnline, status)
			w.store.AddEvent("ok", "网络已恢复："+status.Detail)
			w.notif.Notify("网络已恢复", "校园网已重新连通")
			w.lastRecover = now
			// 掉线期间 DHCP 可能已重分配地址
			w.refreshSourceIP()
		} else {
			w.setFromStatus(state.PhaseOnline, status)
			// 心跳限流：半小时一条，能确认「还活着」又不刷屏
			if w.lastHB.IsZero() || now.Sub(w.lastHB) >= onlineHeartbeat {
				w.lastHB = now
				logx.Log(logx.Debug, "在线 —— %s", status.Detail)
			}
		}
		w.failCount = 0
		w.backoffUntil = time.Time{}
		return
	}

	// ---- 异常分支 ----
	// 注意：failCount 必须在退避判断之前累计，否则退避期一到
	// 计数被冻结，通知阈值永不触发、退避永远停在最小值。
	w.failCount++

	if !mayRelogin {
		w.setFromStatus(state.PhaseSuspended, status)
		if w.failCount == 1 {
			logx.Log(logx.Warn, "[挂起时段] 检测到异常（不重连）—— %s", status.Detail)
			w.store.AddEvent("warn", "时段外检测到异常，未自动重连")
		}
		return
	}

	thr := w.threshold(now)
	if w.failCount < thr {
		w.store.SetPhase(state.PhaseChecking,
			fmt.Sprintf("检测异常 %d/%d —— %s", w.failCount, thr, status.Detail))
		logx.Log(logx.Debug, "检测异常 (%d/%d) —— %s", w.failCount, thr, status.Detail)
		return
	}

	w.setFromStatus(state.PhaseOffline, status)

	// 退避期内不发起登录，但计数已累计（见上）
	if !w.backoffUntil.IsZero() && now.Before(w.backoffUntil) {
		remain := int(time.Until(w.backoffUntil).Seconds())
		logx.Log(logx.Debug, "重登退避中（剩 %ds），本轮不重试", remain)
		return
	}

	firstDetect := w.failCount == thr
	if firstDetect {
		logx.Log(logx.Info, "判定为掉线（阈值 %d），开始自动重认证 —— %s", thr, status.Detail)
		w.store.AddEvent("warn", "检测到断网，开始自动认证")
		w.notif.Notify("检测到断网", "正在尝试自动重新登录校园网…")
	}

	if ok, detail := w.doLogin("auto"); ok {
		logx.Log(logx.Info, "自动重认证成功")
		w.store.NoteLogin(true, "校园网已连接")
		w.setOnline("校园网已连接")
		w.store.AddEvent("ok", "自动认证成功")
		w.notif.Notify("登录成功", "校园网已自动重新认证，连接恢复")
		w.failCount = 0
		w.backoffUntil = time.Time{}
		w.lastRecover = time.Now()
		w.refreshSourceIP()
		return
	} else {
		logx.Log(logx.Error, "自动重认证失败：%s", detail)
		w.store.NoteLogin(false, detail)
	}

	// 指数退避
	shift := w.failCount - thr
	if shift < 0 {
		shift = 0
	}
	if shift > 10 {
		shift = 10
	}
	backoff := 5 * time.Second * time.Duration(1<<shift)
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	w.backoffUntil = time.Now().Add(backoff)
	logx.Log(logx.Warn, "下次重登退避 %ds", int(backoff.Seconds()))

	if w.failCount == thr*3 || w.failCount == thr*10 {
		w.notif.Notify("重连失败",
			fmt.Sprintf("已连续失败 %d 次。可能原因：账号在其他设备登录／密码已改／账号被临时锁定", w.failCount))
	}
}

func (w *Watcher) currentBackoff() time.Time {
	if w.backoffUntil.IsZero() || time.Now().After(w.backoffUntil) {
		return time.Time{}
	}
	return w.backoffUntil
}

// ---------------------------------------------------------------- 动作接口

// LoginResult 是一次手动认证的结果。
type LoginResult struct {
	OK            bool   `json:"ok"`
	AlreadyOnline bool   `json:"already_online"`
	Detail        string `json:"detail"`
}

// Login 手动认证。
//
// 关键设计：先在时段外也允许 —— 用户点「立即认证」就该真的发请求，
// 不该因为不在自动时段而被静默拦住（Python 版曾因签名 bug 完全失效）。
func (w *Watcher) Login() LoginResult {
	w.actionMu.Lock()
	defer w.actionMu.Unlock()

	cfg := w.Config()
	if !cfg.Complete() {
		return LoginResult{OK: false, Detail: "配置不完整：请先在「账号」页填写网关、账号与密码"}
	}

	status := auth.CheckOnline(cfg)
	w.setSrcIP(status.Source)
	if status.Online {
		w.setFromStatus(state.PhaseOnline, status)
		return LoginResult{OK: true, AlreadyOnline: true, Detail: "当前已在线，无需重复认证"}
	}

	w.setAuthing("正在认证…")
	logx.Log(logx.Info, "手动认证：发起登录请求")
	ok, detail := w.doLogin("manual")
	w.store.NoteLogin(ok, detail)

	if ok {
		w.setOnline("校园网已连接")
		w.store.AddEvent("ok", "手动认证成功")
		logx.Log(logx.Info, "手动认证成功")
		w.failCount = 0
		w.backoffUntil = time.Time{}
		w.lastRecover = time.Now()
		w.refreshSourceIP()
		w.refreshSession(cfg, w.SourceIP())
		return LoginResult{OK: true, Detail: "校园网已连接"}
	}

	w.store.SetStatus(state.PhaseOffline, auth.SituationNeedAuth,
		"认证没成功", "检查账号密码是否填对，或者稍后再试", detail)
	w.store.AddEvent("error", "手动认证失败："+detail)
	logx.Log(logx.Error, "手动认证失败：%s", detail)
	return LoginResult{OK: false, Detail: detail}
}

// doLogin 执行一次登录并做后置确认。
//
// 后置确认很重要：网关说 -98（账号已有会话）时，会话可能挂在
// 手机或别的设备上，本机其实还没通。只有本机真的能出外网才算成功。
func (w *Watcher) doLogin(trigger string) (bool, string) {
	cfg := w.Config()
	src := w.SourceIP()
	if src == "" {
		src = w.refreshSourceIP()
	}
	if src == "" {
		return false, "未找到可用的校园网地址（请先连接有线或 Wi-Fi）"
	}

	res := auth.Login(cfg, src)
	logx.Log(logx.Info, "登录响应（%s）：%s", trigger, res.Detail)
	if res.MAC != "" {
		w.macMu.Lock()
		w.mac = res.MAC
		w.macMu.Unlock()
	}

	if res.Outcome == auth.OutcomeSuccess {
		// 认证刚通过时 Windows 往往还停在「无 Internet」：它上一次探测
		// 被网关劫持到了认证页，而 NCSI 有自己的重试周期。
		// 后台主动请求一次它用的那个地址，让图标尽早恢复。
		go auth.WarmNCSI(src)
		return true, res.Detail
	}
	if res.Outcome == auth.OutcomeAlready {
		// 服务端认为已有会话，但本机不一定真的通了
		if online, detail := w.verifyWired(cfg); online {
			go auth.WarmNCSI(src)
			return true, "该账号已在线，本机网络正常"
		} else {
			return false, "账号在其他设备在线，本机仍未认证（" + detail + "）"
		}
	}
	return false, res.Detail
}

// refreshSession 读一次网关页面上的会话信息（姓名、时长、流量）。
//
// 只是本地一跳 HTTP，几毫秒；失败就保持上一次的值，不影响主流程。
func (w *Watcher) refreshSession(cfg config.Config, src string) {
	if src == "" {
		return
	}
	info, err := portal.Fetch(cfg, src)
	if err != nil || !info.Valid {
		return
	}
	w.store.SetSession(state.SessionInfo{
		Valid:          true,
		Name:           portal.MaskName(info.Name), // 「X同学」，界面不出现全名
		UID:            info.UID,
		IP:             info.IP,
		MAC:            w.currentMAC(src),
		MACFromGateway: w.hasGatewayMAC(),
		StartAt:        info.StartAt,
		Minutes:        info.Minutes,
		FlowKB:         info.FlowKB,
		Fee:            info.Fee,
	})
}

// hasGatewayMAC 表示是否已从登录响应拿到过 olmac。
func (w *Watcher) hasGatewayMAC() bool {
	w.macMu.RLock()
	defer w.macMu.RUnlock()
	return w.mac != ""
}

// setFromStatus 把一次检测的结果连同文案落到 store。
func (w *Watcher) setFromStatus(phase string, st auth.Status) {
	headline, hint := auth.Texts(st.Situation)
	w.store.SetStatus(phase, st.Situation, headline, hint, st.Detail)
}

// setOnline 用于"已确认在线但没有 status 结构"的场景。
func (w *Watcher) setOnline(detail string) {
	w.store.SetStatus(state.PhaseOnline, auth.SituationOK,
		"网络正常", "可以正常上网", detail)
}

// setAuthing 认证过程只有几秒，文案固定。
func (w *Watcher) setAuthing(detail string) {
	w.store.SetStatus(state.PhaseAuthing, "",
		"正在认证", "正在向网关提交，请稍候", detail)
}

// currentMAC 返回要显示的 MAC：优先用网关认定的 olmac，
// 没有（比如刚启动还没登录过）则退回本机网卡。
func (w *Watcher) currentMAC(src string) string {
	w.macMu.RLock()
	m := w.mac
	w.macMu.RUnlock()
	if m != "" {
		return m
	}
	return auth.InterfaceMAC(src)
}

// verifyWired 用绑定校园网源 IP 的方式复核真实连通性。
// 名字保留 Wired 是历史路径；它对有线和 Wi-Fi 同样适用。
func (w *Watcher) verifyWired(cfg config.Config) (bool, string) {
	st := auth.CheckOnline(cfg)
	w.setSrcIP(st.Source)
	return st.Online, st.Detail
}

// Logout 主动注销（会断网，供用户测试）。
func (w *Watcher) Logout() (bool, string) {
	w.actionMu.Lock()
	defer w.actionMu.Unlock()

	cfg := w.Config()
	src := w.SourceIP()
	if src == "" {
		src = w.refreshSourceIP()
	}

	w.store.SetStatus(state.PhaseAuthing, "", "正在断开", "正在通知网关，请稍候", "正在注销…")
	w.store.AddEvent("info", "开始注销认证")

	// 先记下网关页面给出的另一种注销路径（a=Logout），备用
	var info portal.Info
	if i, err := portal.Fetch(cfg, src); err == nil {
		info = i
	}

	// ── 第一条路：Dr.COM 的 /drcom/logout ──
	err1 := auth.Logout(cfg, src)
	if err1 != nil {
		logx.Log(logx.Warn, "drcom 注销接口返回：%s", err1)
	}

	// 必须等一会儿再验：该校开了「无感知认证」，注销瞬间确实断了，
	// 但网关发现该 MAC 还有流量就会自动重新登录 —— 立刻检测会误判成功。
	if !w.stillOnline(cfg, 1200*time.Millisecond) {
		return w.logoutOK("drcom")
	}

	// ── 第二条路：网关页面自带的 a=Logout 接口 ──
	logx.Log(logx.Info, "drcom 注销未生效，改用网关的 eportal 注销接口")
	if err2 := portal.Logout(cfg, src, info); err2 != nil {
		logx.Log(logx.Warn, "eportal 注销接口返回：%s", err2)
	}
	if !w.stillOnline(cfg, 1600*time.Millisecond) {
		return w.logoutOK("eportal")
	}

	// 两条都不行：如实告知，并给出去自助服务注销的入口
	logx.Log(logx.Warn, "两种注销接口都未生效（网关可能开启了无感知认证）")
	_, detail := w.verifyWired(cfg)
	w.setOnline(detail)
	w.store.AddEvent("warn", "两种注销接口均未生效")
	return false, "两种注销接口都没能断开（网关可能启用了无感知认证，注销后会立即自动重连）。" +
		"可在自助服务里注销，或用「打开自助服务」按钮"
}

// stillOnline 等待一段时间后复核，仍在线返回 true。
func (w *Watcher) stillOnline(cfg config.Config, wait time.Duration) bool {
	time.Sleep(wait)
	online, detail := w.verifyWired(cfg)
	if online {
		logx.Log(logx.Debug, "注销复核：仍在线（%s）", detail)
	}
	return online
}

func (w *Watcher) logoutOK(via string) (bool, string) {
	logx.Log(logx.Info, "已注销认证（经由 %s）", via)
	w.store.SetStatus(state.PhaseOffline, auth.SituationNeedAuth,
		"已断开认证", "需要上网时点「立即认证」", "已注销认证")
	w.store.AddEvent("warn", "已注销认证")
	w.store.SetSession(state.SessionInfo{})
	w.failCount = 0
	w.backoffUntil = time.Time{}
	return true, "已注销认证"
}

// Check 立即检测一次，返回结果。
func (w *Watcher) Check() (bool, string) {
	w.actionMu.Lock()
	defer w.actionMu.Unlock()

	cfg := w.Config()
	status := auth.CheckOnline(cfg)
	w.setSrcIP(status.Source)
	kind := "warn"
	if status.Online {
		w.setFromStatus(state.PhaseOnline, status)
		kind = "ok"
	} else {
		w.setFromStatus(state.PhaseOffline, status)
	}
	w.store.SetNetwork(status.Source, cfg.InWatchWindow(time.Now().Hour()), cfg.Complete())
	w.store.SetBackoff(w.currentBackoff())
	w.store.AddEvent(kind, status.Detail)
	return status.Online, status.Detail
}

// TestAccount 用传入的凭据试登录一次，不修改已保存的配置。
func (w *Watcher) TestAccount(gateway, account, password, ethGW string) (bool, string) {
	testCfg := config.Config{
		Gateway:    gateway,
		Account:    account,
		Password:   password,
		EthGW:      ethGW,
		PingTarget: w.Config().PingTarget,
	}
	// 已在线时网关不会重复校验密码，只会返回各种拒绝码
	// （实测 result=0 msg=1 hidn=-5 / Error code 205 (-98)）。
	// 直接发请求会让用户看到"密码错误"，所以先看状态再说。
	if auth.CheckOnline(w.Config()).Online {
		return true, "本机当前已在线，网关不会重复校验密码。" +
			"要验证密码是否正确，请先点「断开测试」注销，再来测试"
	}

	src := auth.DiscoverSourceIP(gateway, ethGW)
	if src == "" {
		return false, "校园网不可达（检查网络接入与网关 IP）"
	}
	res := auth.Login(testCfg, src)
	switch res.Outcome {
	case auth.OutcomeSuccess:
		return true, res.Detail
	case auth.OutcomeAlready:
		// 已在线不代表密码验证通过，但绝不是失败，如实说明
		return true, "账号当前已在线（网关未重复校验密码）。要验证密码，请先注销再测试"
	default:
		return false, res.Detail
	}
}

// ---------------------------------------------------------------- 源 IP 缓存

// SourceIP 只读缓存的源 IP，零网络 I/O。
// 供轮询型接口使用 —— 轮询接口里绝不能做探测。
func (w *Watcher) SourceIP() string {
	w.srcMu.RLock()
	defer w.srcMu.RUnlock()
	return w.srcIP
}

func (w *Watcher) setSrcIP(ip string) {
	if ip == "" {
		return
	}
	w.srcMu.Lock()
	w.srcIP = ip
	w.srcMu.Unlock()
}

// refreshSourceIP 主动探测源 IP，带失败退避与日志限流。
func (w *Watcher) refreshSourceIP() string {
	w.srcMu.RLock()
	failTill := w.srcFailTill
	cached := w.srcIP
	w.srcMu.RUnlock()

	now := time.Now()
	if now.Before(failTill) {
		return cached
	}

	cfg := w.Config()
	ip := auth.DiscoverSourceIP(cfg.Gateway, cfg.EthGW)

	w.srcMu.Lock()
	defer w.srcMu.Unlock()

	if ip == "" {
		w.srcFailTill = now.Add(srcIPRetry)
		// 限流：同一句话 30 秒最多一条
		if w.srcWarnAt.IsZero() || now.Sub(w.srcWarnAt) >= 30*time.Second {
			w.srcWarnAt = now
			logx.Log(logx.Warn, "源 IP 自动发现失败（内网网关与认证网关均不可达）")
		}
		return ""
	}
	w.srcIP = ip
	w.srcFailTill = time.Time{}
	return ip
}

func maskAccount(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[:3] + "****" + s[len(s)-2:]
}
