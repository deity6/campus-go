// Package auth 实现 Dr.COM 校园网认证：在线判定、登录、注销。
//
// 判据沿用旧版 Python 实现（已在真机验证过数月）：
//   - 外网可达            -> 在线
//   - 外网不通 + 网关通   -> 网卡正常但认证失效（需要重新登录）
//   - 外网不通 + 网关不通 -> 接入/网卡问题（登录也没用）
//
// 网络探测用系统 ping 而非原始 ICMP：后者需要管理员权限，
// 且 Windows 上还需要加载 ws2_32 的扩展，收益不抵复杂度。
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"campusautologin/internal/config"
	"campusautologin/internal/sysproc"
)

// CallbackVersion 是 Dr.COM 客户端版本号，固定值。
//
// 必须是 4391 —— 这是旧版 Python 实现在真机上跑通的值。
// v 参数不对时网关会返回各种莫名其妙的错误码（实测 -5），
// 看起来像"密码错误"，实际是版本握手失败。
const CallbackVersion = "4391"

// DiscoverSourceIP 找出「能到达认证网关」的本机源 IP。
//
// 有线和 Wi-Fi 都适用：认证前网关地址必须可达，内核路由会选出
// 正确的出接口。用 UDP connect 只做路由选择和本地地址绑定，
// 不发任何报文，因此不需要目标端口开放。
// 先试认证网关，再试本机内网网关 —— 后者只是辅助判据。
func DiscoverSourceIP(gateway, ethGW string) string {
	targets := make([]string, 0, 2)
	if gateway != "" {
		targets = append(targets, net.JoinHostPort(gateway, "80"))
	}
	if ethGW != "" {
		targets = append(targets, net.JoinHostPort(ethGW, "53"))
	}
	for _, t := range targets {
		conn, err := net.DialTimeout("udp", t, 3*time.Second)
		if err != nil {
			continue
		}
		addr, ok := conn.LocalAddr().(*net.UDPAddr)
		conn.Close()
		if ok && addr.IP != nil && !addr.IP.IsUnspecified() {
			return addr.IP.String()
		}
	}
	return ""
}

// InterfaceMAC 返回 sourceIP 所属网卡的 MAC，格式与网关页面一致
// （AA-BB-CC-DD-EE-FF）。有线和 Wi-Fi 网卡都支持。
//
// 网关页面只给出账号与 IP，不告诉本机 MAC；而「在线信息」里
// MAC 是有用的（多设备时用来确认是哪一台）。本机取最准。
func InterfaceMAC(sourceIP string) string {
	ip := net.ParseIP(strings.TrimSpace(sourceIP))
	if ip == nil {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if len(ifc.HardwareAddr) != 6 { // 只认以太网/WiFi 的 6 字节地址
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || !ipnet.IP.Equal(ip) {
				continue
			}
			return strings.ToUpper(strings.ReplaceAll(ifc.HardwareAddr.String(), ":", "-"))
		}
	}
	return ""
}

// sameIPv4Subnet 粗判两个 IPv4 是否在同一 /24。
// 只用于决定「本机内网网关」这个辅助判据是否还适用于当前源 IP；
// 校园网 IP 换到 Wi-Fi 网段时，旧的家用路由网关不应继续参与判断。
func sameIPv4Subnet(a, b string) bool {
	ia, ib := net.ParseIP(strings.TrimSpace(a)).To4(), net.ParseIP(strings.TrimSpace(b)).To4()
	if ia == nil || ib == nil {
		return false
	}
	return ia[0] == ib[0] && ia[1] == ib[1] && ia[2] == ib[2]
}

// Ping 调用系统 ping 判断可达性。
func Ping(target string, timeoutMs int, sourceIP string) bool {
	if target == "" {
		return false
	}
	args := []string{"-n", "1", "-w", fmt.Sprint(timeoutMs)}
	if sourceIP != "" {
		args = append(args, "-S", sourceIP)
	}
	args = append(args, target)

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeoutMs+1500)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ping", args...)
	// 关键：不加这个，每次检测都会闪一个黑色控制台窗口
	sysproc.Hide(cmd)
	err := cmd.Run()
	return err == nil
}

// Status 描述一次在线检测的结果。
type Status struct {
	Online bool
	Detail string
	Source string
	// Situation 是给界面用的归类（ok / need_auth / no_cable / unreachable），
	// Detail 保留技术细节供日志排查，两者分开。
	Situation string
}

// PingAny 依次尝试多个目标，任意一个通即认为外网可达。
func PingAny(targets []string, timeoutMs int, sourceIP string) (string, bool) {
	for _, t := range targets {
		if Ping(t, timeoutMs, sourceIP) {
			return t, true
		}
	}
	return "", false
}

// CheckOnline 双判据判断当前是否在线。
func CheckOnline(cfg config.Config) Status {
	src := DiscoverSourceIP(cfg.Gateway, cfg.EthGW)

	// 没有能到达认证网关的源 IP 就无法可靠判断校园网状态。
	// 此时若用未绑定的 ping，在多网卡机器上可能走别的网络得到"在线"，
	// 得出完全错误的结论 —— 宁可报"未连接校园网"，也不要报假的在线。
	if src == "" {
		return Status{Online: false, Situation: SituationNoCable,
			Detail: "未找到能访问认证网关的本机地址（请先连接校园网有线或 Wi-Fi）"}
	}

	// 1) ping 最快，通了直接算在线
	if hit, ok := PingAny(cfg.PingTargets(), 1500, src); ok {
		return Status{Online: true, Situation: SituationOK,
			Detail: fmt.Sprintf("ping %s 通", hit), Source: src}
	}

	// 2) ping 不通时，用应用层判据再确认：
	//    有些校园网关禁 ICMP，但网页打得开，只 ping 会误判成离线。
	//    HTTPS 快（百毫秒级）先试；Windows 的 NCSI 判据慢且常被
	//    校园网 DNS 劫持，放后面兜底。
	n := CheckHTTPS(src)
	if !n {
		n = CheckNCSI(src)
	}

	if cfg.EthGW != "" && sameIPv4Subnet(src, cfg.EthGW) && Ping(cfg.EthGW, 1200, src) {
		if n {
			return Status{Online: true, Situation: SituationOK,
				Detail: "ping 不通，但 Windows 连通性探测通过", Source: src}
		}
		return Status{Online: false, Situation: SituationNeedAuth,
			Detail: fmt.Sprintf("网关 %s 通、外网不通（认证已失效）", cfg.EthGW), Source: src}
	}

	// 3) 网关也不通 —— 接入层面就没通
	if n {
		return Status{Online: true, Situation: SituationOK,
			Detail: "网关 ping 不通，但外网 HTTP 可达", Source: src}
	}
	return Status{Online: false, Situation: SituationUnreachable,
		Detail: "外网与网关均不通", Source: src}
}

// httpGetWithSource 发一个绑定源 IP 的 GET 请求。
func httpGetWithSource(rawURL, sourceIP string, timeout time.Duration) (int, string, error) {
	dialer := &net.Dialer{Timeout: timeout}
	if sourceIP != "" {
		if ip := net.ParseIP(sourceIP); ip != nil {
			dialer.LocalAddr = &net.TCPAddr{IP: ip}
		}
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			DisableKeepAlives:   true,
			MaxIdleConnsPerHost: 1,
		},
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 CampusAutoLogin/2.0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > 64*1024 {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return resp.StatusCode, string(buf), nil
}

var (
	jsonRe = regexp.MustCompile(`(?s)\{.*\}`)
	// 网关在"账号已在线"等情况下返回的不是 JSON，而是这种错误页：
	//   <html><body>Error code: 205 System Error1(-98)</body></html>
	// 括号里那个数才是真正的错误码。
	htmlErrRe = regexp.MustCompile(`Error\s*code://s*(//d+).*?//((-?//d+)//)`)
)

// GatewayReply 是从网关响应里解析出来的结果。
type GatewayReply struct {
	Result json.Number `json:"result"`
	Msg    json.Number `json:"msg"`
	Hidn   json.Number `json:"hidn"`
	V46IP  string      `json:"v46ip"`
	// OLMAC 是网关认定的本机 MAC（形如 c0515c8a7aa6，无分隔符）。
	// 它未必等于本机网卡 MAC（经路由器/多网卡时不同），
	// 但它是「在线信息」里显示的那一个，所以以它为准。
	OLMAC string `json:"olmac"`

	// 从 HTML 错误页里解析出来的码（JSON 形式时为 0）
	HTMLCode int
	Raw      string
}

// ParseReply 解析网关响应。两种格式都见过，必须都能认。
func ParseReply(body string) (GatewayReply, bool) {
	r := GatewayReply{Raw: strings.TrimSpace(body)}

	if m := jsonRe.FindString(body); m != "" {
		if err := json.Unmarshal([]byte(m), &r); err == nil {
			return r, true
		}
	}
	if m := htmlErrRe.FindStringSubmatch(body); m != nil {
		fmt.Sscanf(m[2], "%d", &r.HTMLCode)
		return r, true
	}
	// 纯文本（例如注销成功页）也算解析成功，交给调用方判断
	if r.Raw != "" {
		return r, true
	}
	return r, false
}

// AlreadyOnline 判断这次响应是不是"账号已有会话"。
//
// 这不是失败：用户可能刚在手机或浏览器上登过，或者本机的
// 会话还没被网关回收。旧版把它当错误报给用户，才会出现
// "手动能登、工具说失败"的怪现象。
func (r GatewayReply) AlreadyOnline() bool {
	return r.HTMLCode == -98 || r.Hidn.String() == "-98"
}

// Accepted 判断网关是否明确表示登录成功。
func (r GatewayReply) Accepted() bool { return r.Result.String() == "1" }

// Explain 把响应翻译成一句人话。
func (r GatewayReply) Explain() string {
	if r.Accepted() {
		if r.V46IP != "" {
			return fmt.Sprintf("登录成功（内网 IP %s）", r.V46IP)
		}
		return "登录成功"
	}
	if r.AlreadyOnline() {
		return "该账号当前已在线（无需重复登录）"
	}
	if r.HTMLCode != 0 {
		return fmt.Sprintf("网关返回错误码 %d（%s）", r.HTMLCode, r.Raw)
	}
	hint := ""
	switch r.Msg.String() {
	case "1":
		hint = "，密码错误或触发失败次数保护"
	case "2":
		hint = "，账号被占用"
	case "3":
		hint = "，账号已欠费或被停用"
	}
	if r.Result.String() == "0" && hint == "" {
		hint = "，账号或密码不对"
	}
	return fmt.Sprintf("服务器拒绝：result=%s msg=%s hidn=%s%s",
		r.Result.String(), r.Msg.String(), r.Hidn.String(), hint)
}

// formatMAC 把网关给的 12 位十六进制 MAC 加上分隔符。
func formatMAC(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	raw = strings.NewReplacer(":", "", "-", "", ".", "").Replace(raw)
	if len(raw) != 12 {
		return ""
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, raw[i:i+2])
	}
	return strings.Join(parts, "-")
}

// LoginURLFor 拼出登录请求 URL。
//
// 参数顺序必须与旧版一字不差：Dr.COM 网关按位置解析 QUERY_STRING，
// 用 url.Values.Encode() 会按字母序重排（0MKKey 跑到最前），
// 网关直接回 "404 URL no found"。
func LoginURLFor(cfg config.Config) string {
	return fmt.Sprintf(
		"http://%s/drcom/login?callback=dr1004&DDDDD=%s&upass=%s&0MKKey=%s"+
			"&R1=0&R3=1&R6=0&para=00&v6ip=&v=%s",
		cfg.Gateway, cfg.Account, cfg.Password, cfg.Password, CallbackVersion)
}

// Outcome 是一次登录尝试的结论。
type Outcome int

const (
	OutcomeRejected Outcome = iota // 网关明确拒绝
	OutcomeSuccess                 // 网关确认登录成功
	OutcomeAlready                 // 账号已有会话（等于已在线）
)

// LoginResult 是登录的完整结果。
type LoginResult struct {
	Outcome Outcome
	Detail  string
	Raw     string
	// MAC 来自网关响应的 olmac，格式化后形如 C0-51-5C-8A-7A-A6
	MAC string
}

// OK 表示「这次网络应该是通的」——成功和已在线都算。
func (r LoginResult) OK() bool {
	return r.Outcome == OutcomeSuccess || r.Outcome == OutcomeAlready
}

// Login 向网关发起登录。
func Login(cfg config.Config, sourceIP string) LoginResult {
	if !cfg.Complete() {
		return LoginResult{Outcome: OutcomeRejected, Detail: "配置不完整（缺少网关/账号/密码）"}
	}
	if sourceIP == "" {
		return LoginResult{Outcome: OutcomeRejected, Detail: "未找到可用的校园网地址（请先连接有线或 Wi-Fi）"}
	}

	code, body, err := httpGetWithSource(LoginURLFor(cfg), sourceIP, 10*time.Second)
	if err != nil {
		return LoginResult{Outcome: OutcomeRejected, Detail: "请求网关失败: " + err.Error()}
	}
	if code == 0 {
		return LoginResult{Outcome: OutcomeRejected, Detail: "网关不可达"}
	}

	reply, parsed := ParseReply(body)
	if !parsed {
		return LoginResult{Outcome: OutcomeRejected,
			Detail: fmt.Sprintf("网关无有效响应（HTTP %d）", code)}
	}

	switch {
	case reply.Accepted():
		return LoginResult{Outcome: OutcomeSuccess, Detail: reply.Explain(),
			Raw: reply.Raw, MAC: formatMAC(reply.OLMAC)}
	case reply.AlreadyOnline():
		// 关键：这不是失败。旧版把它当错误，用户看到的就是
		// "我自己能登上去，工具却说认证失败"。
		return LoginResult{Outcome: OutcomeAlready, Detail: reply.Explain(),
			Raw: reply.Raw, MAC: formatMAC(reply.OLMAC)}
	default:
		return LoginResult{Outcome: OutcomeRejected, Detail: reply.Explain(), Raw: reply.Raw}
	}
}

// LogoutURLFor 拼出注销请求 URL。
//
// 参数与旧版完全一致：Dr.COM 的注销也要带 DDDDD 和 v6ip，
// 少一个就只返回一个通用错误页、会话并不会被释放。
func LogoutURLFor(cfg config.Config) string {
	return fmt.Sprintf(
		"http://%s/drcom/logout?callback=dr1004&DDDDD=%s&para=00&v6ip=&v=%s",
		cfg.Gateway, cfg.Account, CallbackVersion)
}

// Logout 主动注销（会断网）。
//
// 返回 nil 只代表「网关没有明确拒绝」，是否真的断开由调用方
// 用 CheckOnline 复核 —— 各校网关的注销回执格式差异很大，
// 信回执不如信网络状态。
func Logout(cfg config.Config, sourceIP string) error {
	if sourceIP == "" {
		sourceIP = DiscoverSourceIP(cfg.Gateway, cfg.EthGW)
	}
	code, body, err := httpGetWithSource(LogoutURLFor(cfg), sourceIP, 10*time.Second)
	if err != nil {
		return fmt.Errorf("请求网关失败: %w", err)
	}
	if code == 0 {
		return fmt.Errorf("网关不可达")
	}
	reply, parsed := ParseReply(body)
	if !parsed {
		return fmt.Errorf("网关无有效响应（HTTP %d）", code)
	}
	if reply.Accepted() {
		return nil
	}
	if reply.AlreadyOnline() {
		// 已经不在线了，等价于注销完成
		return nil
	}
	return fmt.Errorf("注销被拒：%s", reply.Explain())
}

// Sanitize 去掉字符串里的空白，便于比对。
func Sanitize(s string) string { return strings.TrimSpace(s) }
