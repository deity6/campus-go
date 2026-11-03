// Package portal 与认证网关的「自助页面」打交道。
//
// 网关在 http://<gateway>/ 直接返回一小段 JS 配置，里面有：
//
//	uid='<账号>'; NID='<姓名>'; time='9'; flow='7031'; fee='0';
//	v4ip='172.30.x.x'; stime='2026-11-03 21:58:06'; etime='...';
//	authlogoutpath='/eportal/?c=ACSetting&a=Logout&ver=1.0';
//
// 这份数据不需要登录任何系统就能读，正好用来显示「欢迎 X 同学」
// 以及在线时长/流量。同页面还给出了另一种注销接口 a=Logout，
// 在 /drcom/logout 不生效时可以兜底。
//
// 注意：该页面是 GB2312 编码，中文必须转码，否则姓名会乱。
package portal

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"campusautologin/internal/config"
)

// Info 是网关页面上读到的会话信息。
type Info struct {
	Valid   bool
	Name    string
	UID     string
	IP      string
	StartAt string
	Minutes int
	FlowKB  int
	Fee     string

	// LogoutPath 是网关自己给出的注销路径，可能为空
	LogoutPath string
}

// varRe 匹配页面里的 `name='value';` 赋值。
var varRe = regexp.MustCompile(`(\w+)\s*=\s*'([^']*)'\s*;`)

// httpGet 发一个绑定源 IP 的 GET，返回解码成 UTF-8 的正文。
func httpGet(rawURL, sourceIP string, timeout time.Duration) (int, string, error) {
	dialer := &net.Dialer{Timeout: timeout}
	if sourceIP != "" {
		if ip := net.ParseIP(sourceIP); ip != nil {
			dialer.LocalAddr = &net.TCPAddr{IP: ip}
		}
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:       dialer.DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 CampusAutoLogin/4.0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, decodeGBK(raw), nil
}

// decodeGBK 尝试把 GBK/GB2312 转成 UTF-8；失败时按原样返回。
func decodeGBK(b []byte) string {
	// 先试 UTF-8：有些固件其实是 UTF-8，强行转码会变乱码
	if isValidUTF8(b) {
		return string(b)
	}
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), b)
	if err != nil {
		return string(b)
	}
	return string(out)
}

func isValidUTF8(b []byte) bool {
	// 简单判定：含高位字节且能被 utf8 正常解码
	if bytes.IndexFunc(b, func(r rune) bool { return r > 127 }) < 0 {
		return true
	}
	return bytes.Equal(bytes.ToValidUTF8(b, nil), b)
}

// Fetch 读取网关页面的会话信息。
func Fetch(cfg config.Config, sourceIP string) (Info, error) {
	if sourceIP == "" {
		return Info{}, fmt.Errorf("没有可用的校园网源地址")
	}
	code, body, err := httpGet(fmt.Sprintf("http://%s/", cfg.Gateway), sourceIP, 6*time.Second)
	if err != nil {
		return Info{}, err
	}
	if code != http.StatusOK {
		return Info{}, fmt.Errorf("网关返回 HTTP %d", code)
	}
	return parse(body), nil
}

func parse(body string) Info {
	kv := map[string]string{}
	for _, m := range varRe.FindAllStringSubmatch(body, -1) {
		// 同名变量取最后一次出现（页面里 v4ip 被赋值两次，后者才是真实内网 IP）
		kv[m[1]] = strings.TrimSpace(m[2])
	}

	info := Info{
		Name:       kv["NID"],
		UID:        kv["uid"],
		IP:         kv["lip"],
		StartAt:    kv["stime"],
		Fee:        kv["fee"],
		LogoutPath: kv["authlogoutpath"],
	}
	if info.IP == "" {
		info.IP = kv["v4ip"]
	}
	info.Minutes, _ = strconv.Atoi(kv["time"])
	info.FlowKB, _ = strconv.Atoi(kv["flow"])

	// 有姓名或账号才算读到了有效会话
	info.Valid = info.UID != "" || info.Name != ""
	return info
}

// MaskName 把姓名脱敏成「X同学」这种形式。
//
// 名字只用于问候，界面不需要全名；统一脱敏后，截图、日志、
// 演示都不会带出真实姓名。中文取姓，其他取首字符。
func MaskName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	r := []rune(name)
	first := r[0]
	if !(first >= 0x4E00 && first <= 0x9FFF) && first >= 'a' && first <= 'z' {
		first -= 32 // 英文名首字母大写
	}
	return string(first) + "同学"
}

// Logout 通过网关页面给出的 a=Logout 接口注销。
//
// 这是 /drcom/logout 不生效时的第二条路：两者走的是不同的服务端
// 处理分支，实测有一种能成、另一种不灵的情况。
func Logout(cfg config.Config, sourceIP string, info Info) error {
	if sourceIP == "" {
		return fmt.Errorf("没有可用的校园网源地址")
	}
	path := info.LogoutPath
	if path == "" {
		path = "/eportal/?c=ACSetting&a=Logout&ver=1.0"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	code, _, err := httpGet(fmt.Sprintf("http://%s%s", cfg.Gateway, path),
		sourceIP, 8*time.Second)
	if err != nil {
		return err
	}
	if code == 0 {
		return fmt.Errorf("网关不可达")
	}
	return nil
}
