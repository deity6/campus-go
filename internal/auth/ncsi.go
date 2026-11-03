package auth

import (
	"crypto/tls"
	"net"
	"strings"
	"time"

	"campusautologin/internal/config"
)

// Windows 判断"这台机器有没有 Internet"，靠的是 NCSI（Network Connectivity
// Status Indicator）对微软两个固定地址的探测：
//
//	http://www.msftconnecttest.com/connecttest.txt  → 必须是 "Microsoft Connect Test"
//	http://www.msftconnecttest.com/redirect         → 能拿到结果说明没有强制门户
//
// 校园网刚认证通过时，Windows 往往还停在"无 Internet"：
// 它上一次探测被网关劫持到了认证页，而 NCSI 有自己的重试周期，
// 要等几十秒到几分钟才会重来。期间任务栏一直挂着地球图标。
//
// 本程序在这里主动请求同一个地址，起两个作用：
//  1. 用 Windows 自己的判据确认"应用层真的通了"——比 ping 更接近
//     用户实际打开的网页，也补上"网络禁 ICMP 但 HTTP 正常"的情况；
//  2. 让网关尽早放行这个域名，Windows 下一次自检就能拿到正确内容，
//     任务栏图标随之恢复。
//
// 全程静默，不弹窗、不写日志噪音，失败也不影响主流程。
const (
	ncsiProbeURL  = "http://www.msftconnecttest.com/connecttest.txt"
	ncsiProbeBody = "Microsoft Connect Test"
	ncsiTimeout   = 3 * time.Second

	// HTTPS 判据用的站点。选国内大站：解析稳定、握手快、不会被
	// 校园网关当成"认证探测"特别处理。校验证书，所以即使被 DNS
	// 劫持到认证页，TLS 握手也会失败，不会误判成"能上网"。
	httpsTargets = "www.baidu.com,www.qq.com"
	httpsTimeout = 4 * time.Second
)

// CheckHTTPS 用一次真实的 TLS 握手确认"应用层能出外网"。
//
// 比 ping 更接近用户实际打开的网页，也补上"网络禁 ICMP 但 HTTP 正常"
// 的情况。证书校验能识破 DNS 劫持到认证页的情形 —— 那种连接拿不到
// 合法证书，握手直接失败。
func CheckHTTPS(sourceIP string) bool {
	if sourceIP == "" {
		return false
	}
	for _, host := range strings.Split(httpsTargets, ",") {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		dialer := &net.Dialer{Timeout: httpsTimeout}
		if ip := net.ParseIP(sourceIP); ip != nil {
			dialer.LocalAddr = &net.TCPAddr{IP: ip}
		}
		conn, err := tls.DialWithDialer(dialer, "tcp", host+":443",
			&tls.Config{ServerName: host})
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// CheckNCSI 用 Windows 的判据确认外网是否可用。
//
// 必须绑定校园网源 IP：机器上同时有其他网络时，不绑定的请求可能
// 走另一张网卡，把别的网络误判成校园网。
func CheckNCSI(sourceIP string) bool {
	if sourceIP == "" {
		return false
	}
	code, body, err := httpGetWithSource(ncsiProbeURL, sourceIP, ncsiTimeout)
	if err != nil || code != 200 {
		return false
	}
	// 被网关劫持时拿到的是认证页 HTML，不含这句文本
	return strings.Contains(body, ncsiProbeBody)
}

// WarmNCSI 在认证成功后调用，让 Windows 尽快把网络图标恢复正常。
//
// 认证刚通过的那几秒，网关对本机流量的放行可能还没完全生效，
// 所以重试几次；一旦成功就停。整个函数在独立 goroutine 里跑，
// 不阻塞守护循环，也不产生任何用户可见输出。
func WarmNCSI(sourceIP string) {
	if sourceIP == "" {
		return
	}
	// 只试两次：校园网的 DNS 常把这个域名劫持到不可达地址，
	// 试多了纯属在后台空转（每次 3 秒超时）。
	for i := 0; i < 2; i++ {
		if i > 0 {
			time.Sleep(time.Second)
		}
		if CheckNCSI(sourceIP) {
			return
		}
	}
}

// 状态归类。前端按它选主文案，不直接展示技术细节。
const (
	SituationOK          = "ok"          // 可以正常上网
	SituationNeedAuth    = "need_auth"   // 连上了校园网，但还没认证
	SituationNoCable     = "no_cable"    // 没有可用的校园网接入（有线或 Wi-Fi）
	SituationUnreachable = "unreachable" // 外网与网关都不通
	SituationSuspended   = "suspended"   // 时段外，只检测不重连
)

// Texts 返回给人看的两行文案：标题 + 说明。
//
// 之前写的是「已联网（ping 223.5.5.5 通）」，对普通用户毫无意义 ——
// 他们只关心"能不能上网""要不要我做点什么"。
func Texts(situation string) (headline, hint string) {
	switch situation {
	case SituationOK:
		return "网络正常", "可以正常上网"
	case SituationNeedAuth:
		return "需要认证", "已连上校园网，点「立即认证」就能上网"
	case SituationNoCable:
		return "未连接校园网", "没有找到能访问认证网关的连接，先连上有线或校园 Wi-Fi"
	case SituationSuspended:
		return "等待认证", "当前不在自动认证时段，需要的话点「立即认证」"
	default:
		return "网络不通", "外网和网关都连不上，检查网络接入或网卡"
	}
}

// CheckStatus 是带文案的在线判定，watcher 直接用它的结果。
func CheckStatus(cfg config.Config) (online bool, situation, headline, hint, detail, source string) {
	st := CheckOnline(cfg)
	headline, hint = Texts(st.Situation)
	return st.Online, st.Situation, headline, hint, st.Detail, st.Source
}
