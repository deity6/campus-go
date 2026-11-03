// Package api 是界面与业务之间的唯一通道。
//
// 早期版本走本地 HTTP（Python 时代为了把 GUI 拆成独立进程所需），
// 但界面现在已经和守护在同一个进程里，再开一个端口纯属多余：
// 多一个监听、多一份 token、多一个 runtime.json，还让页面拿到
// http://127.0.0.1:xxxxx 这种来源 —— 浏览器原生弹窗会把它显示出来。
//
// 现在改成 WebView2 原生的消息通道：
//
//	JS:  window.chrome.webview.postMessage(JSON.stringify({id, method, params}))
//	Go:  chromium.MessageCallback 收到后同步处理
//	Go:  chromium.Eval("__rpcResolve(id, ok, payload)")
//
// 不监听任何端口，不经过网络栈，比 HTTP 更封闭。
package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"runtime"
	"strconv"
	"strings"

	"campusautologin/internal/config"
	"campusautologin/internal/logx"
	"campusautologin/internal/state"
	"campusautologin/internal/watcher"
)

//go:embed all:ui
var uiFS embed.FS

// Version 显示在界面上。
const Version = "4.3"

// Debug 返回平台信息，便于排查「在我这能跑」类问题。
func Debug() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Service 承载所有可被界面调用的操作。
type Service struct {
	w     *watcher.Watcher
	store *state.Store
}

// New 创建服务。
func New(w *watcher.Watcher, store *state.Store) *Service {
	return &Service{w: w, store: store}
}

// Call 执行一次调用。返回的对象会被序列化后回给界面。
func (s *Service) Call(method string, params map[string]any) (any, error) {
	switch method {
	case "state.get":
		return s.statePayload(), nil
	case "config.get":
		return map[string]any{"ok": true, "config": s.configSnapshot()}, nil
	case "config.save":
		return s.saveConfig(params), nil
	case "account.save":
		return s.saveAccount(params), nil
	case "account.test":
		cfg := s.w.Config()
		gw := strings.TrimSpace(str(params["gateway"]))
		acc := strings.TrimSpace(str(params["account"]))
		pwd := str(params["password"])
		if gw == "" || acc == "" || pwd == "" {
			return map[string]any{"ok": false, "error": "三个字段都要填"}, nil
		}
		ok, detail := s.w.TestAccount(gw, acc, pwd, cfg.EthGW)
		if ok {
			return map[string]any{"ok": true, "detail": detail}, nil
		}
		return map[string]any{"ok": false, "error": detail}, nil
	case "check":
		online, detail := s.w.Check()
		return map[string]any{"ok": true, "online": online, "detail": detail}, nil
	case "login":
		// 没填账号密码时直接引导去填，不要把"配置不完整"当成认证失败
		if cfg := s.w.Config(); !cfg.Complete() {
			return map[string]any{
				"ok":          false,
				"need_config": true,
				"error":       "还没填账号和密码",
			}, nil
		}
		return s.w.Login(), nil
	case "logout":
		ok, detail := s.w.Logout()
		if !ok {
			return map[string]any{"ok": false, "error": detail}, nil
		}
		return map[string]any{"ok": true, "detail": detail}, nil
	case "logs.get":
		n := 120
		if v, ok := toInt(params["n"]); ok && v > 0 && v <= 500 {
			n = v
		}
		return map[string]any{"ok": true, "lines": logx.Tail(n)}, nil
	case "autostart.get":
		enabled, target, err := AutostartGet()
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true, "enabled": enabled, "target": target}, nil
	case "autostart.set":
		enable, _ := params["enable"].(bool)
		if err := AutostartSet(enable); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true, "enabled": enable}, nil
	case "open.url":
		if opener == nil {
			return map[string]any{"ok": false, "error": "当前平台不支持打开链接"}, nil
		}
		if err := opener(str(params["url"])); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, fmt.Errorf("未知方法: %s", method)
}

// Opener 由宿主注入：用系统默认浏览器打开链接。
var opener func(url string) error

// SetOpener 注册打开链接的实现。
func SetOpener(f func(url string) error) { opener = f }

// ---------------------------------------------------------------- 载荷

func (s *Service) configSnapshot() map[string]any {
	cfg := s.w.Config()
	now := s.w.Now()
	return map[string]any{
		"gateway":      cfg.Gateway,
		"account":      cfg.Account,
		"password_set": cfg.Password != "",
		// 密码回到界面：用户要求「保存后也要能看到、能改」。
		// 这是纯进程内通道，与 config.json 明文存储一致。
		"password":            cfg.Password,
		"configured":          cfg.Complete(),
		"watch_start_hour":    cfg.WatchStartHour,
		"watch_end_hour":      cfg.WatchEndHour,
		"watch_window":        cfg.WatchText(),
		"check_interval":      cfg.CheckInterval,
		"idle_interval":       cfg.IdleInterval,
		"suspect_interval":    cfg.SuspectInterval,
		"relogin_outside":     cfg.ReloginOutsideWindow,
		"ping_target":         cfg.PingTargetText(),
		"eth_gateway":         cfg.EthGW,
		"intensive_enabled":   cfg.IntensiveEnabled,
		"intensive_window":    cfg.IntensiveText(),
		"intensive_interval":  cfg.IntensiveInterval,
		"intensive_threshold": cfg.IntensiveThreshold,
		"intensive_active":    cfg.InIntensiveWindow(now.Hour()*60+now.Minute(), now.Hour()),
		"failure_threshold":   2,
		"privacy_name_level":  cfg.PrivacyNameLevel,
		"privacy_net_masked":  cfg.PrivacyNetMasked,
		"tour_done":           cfg.TourDone,
	}
}

func (s *Service) statePayload() map[string]any {
	rt, nv := s.store.Snapshot()
	return map[string]any{
		"ok":      true,
		"runtime": rt,
		"config":  s.configSnapshot(),
		"network": nv,
		"session": s.store.Session(),
		"version": Version,
	}
}

func (s *Service) saveConfig(params map[string]any) map[string]any {
	cfg := s.w.Config()

	if v, ok := toInt(params["watch_start_hour"]); ok {
		cfg.WatchStartHour = clampInt(v, 0, 24)
	}
	if v, ok := toInt(params["watch_end_hour"]); ok {
		cfg.WatchEndHour = clampInt(v, 0, 24)
	}
	if v, ok := toInt(params["check_interval"]); ok {
		cfg.CheckInterval = clampInt(v, 5, 3600)
	}
	if v, ok := toInt(params["idle_interval"]); ok {
		cfg.IdleInterval = clampInt(v, 30, 7200)
	}
	if v, ok := toInt(params["suspect_interval"]); ok {
		cfg.SuspectInterval = clampInt(v, 3, 300)
	}
	if v, ok := params["relogin_outside_window"].(bool); ok {
		cfg.ReloginOutsideWindow = v
	}
	if v, ok := params["ping_target"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.PingTarget = strings.TrimSpace(v)
	}
	if v, ok := params["tour_done"].(bool); ok {
		cfg.TourDone = v
	}
	if v, ok := params["intensive_enabled"].(bool); ok {
		cfg.IntensiveEnabled = v
	}
	if v, ok := params["intensive_start"].(string); ok && validHHMM(v) {
		cfg.IntensiveStart = v
	}
	if v, ok := params["intensive_end"].(string); ok && validHHMM(v) {
		cfg.IntensiveEnd = v
	}
	if v, ok := toInt(params["intensive_interval"]); ok {
		cfg.IntensiveInterval = clampInt(v, 3, 300)
	}
	if v, ok := toInt(params["intensive_threshold"]); ok {
		cfg.IntensiveThreshold = clampInt(v, 1, 5)
	}
	if v, ok := toInt(params["privacy_name_level"]); ok {
		cfg.PrivacyNameLevel = clampInt(v, 0, 2)
	}
	if v, ok := params["privacy_net_masked"].(bool); ok {
		cfg.PrivacyNetMasked = v
	}

	if err := config.Save(cfg); err != nil {
		return map[string]any{"ok": false, "error": "写入 config.json 失败: " + err.Error()}
	}
	s.w.UpdateConfig(cfg)
	s.store.SetNetwork("", cfg.InWatchWindow(s.w.Now().Hour()), cfg.Complete())
	return map[string]any{"ok": true, "config": s.configSnapshot()}
}

func (s *Service) saveAccount(params map[string]any) map[string]any {
	cfg := s.w.Config()

	gw := strings.TrimSpace(str(params["gateway"]))
	acc := strings.TrimSpace(str(params["account"]))
	pwd := str(params["password"])

	if gw == "" || acc == "" {
		return map[string]any{"ok": false, "error": "网关与账号不能为空"}
	}
	cfg.Gateway = gw
	cfg.Account = acc
	if pwd != "" {
		cfg.Password = pwd
	} else if cfg.Password == "" {
		return map[string]any{"ok": false, "error": "尚未设置密码，请填写"}
	}

	if err := config.Save(cfg); err != nil {
		return map[string]any{"ok": false, "error": "写入 config.json 失败: " + err.Error()}
	}
	s.w.UpdateConfig(cfg)
	s.store.SetNetwork("", cfg.InWatchWindow(s.w.Now().Hour()), cfg.Complete())
	return map[string]any{"ok": true, "config": s.configSnapshot()}
}

// ---------------------------------------------------------------- 页面

// RenderHTML 把 index.html / app.css / app.js 合成一个自包含文档。
//
// 目的是让 WebView 用 NavigateToString 直接载入，不需要任何本地服务：
// 没有资源请求，也就没有端口、没有 token、没有跨域。
func (s *Service) RenderHTML() (string, error) {
	read := func(name string) (string, error) {
		b, err := fs.ReadFile(uiFS, "ui/"+name)
		return string(b), err
	}

	html, err := read("index.html")
	if err != nil {
		return "", err
	}
	css, err := read("app.css")
	if err != nil {
		return "", err
	}
	js, err := read("app.js")
	if err != nil {
		return "", err
	}

	html = strings.Replace(html,
		`<link rel="stylesheet" href="/ui/app.css">`,
		"<style>\n"+css+"\n</style>", 1)

	shim := `<script>
(function () {
  var seq = 0, pending = new Map();
  window.__rpcResolve = function (id, ok, payload) {
    var p = pending.get(id);
    if (!p) return;
    pending.delete(id);
    ok ? p.resolve(payload) : p.reject(new Error(payload || "调用失败"));
  };
  window.__rpc = function (method, params) {
    return new Promise(function (resolve, reject) {
      var id = ++seq;
      pending.set(id, { resolve: resolve, reject: reject });
      try {
        window.chrome.webview.postMessage(
          JSON.stringify({ id: id, method: method, params: params || {} }));
      } catch (e) {
        pending.delete(id);
        reject(new Error("与程序内核的通道不可用"));
      }
      setTimeout(function () {
        if (pending.has(id)) { pending.delete(id); reject(new Error("调用超时")); }
      }, 40000);
    });
  };
})();
</script>`

	html = strings.Replace(html,
		`<script src="/ui/app.js"></script>`,
		shim+"\n<script>\n"+js+"\n</script>", 1)

	return html, nil
}

// ---------------------------------------------------------------- RPC 编解码

// ParseRequest 解析界面发来的 RPC 报文。
func ParseRequest(msg string) (id int, method string, params map[string]any, err error) {
	var req struct {
		ID     int            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(msg), &req); err != nil {
		return 0, "", nil, err
	}
	return req.ID, req.Method, req.Params, nil
}

// EncodeResponse 生成回传给界面的 JS 调用。
func EncodeResponse(id int, payload any, callErr error) string {
	if callErr != nil {
		b, _ := json.Marshal(callErr.Error())
		return fmt.Sprintf(`window.__rpcResolve(%d,false,%s)`, id, b)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		b, _ = json.Marshal("响应序列化失败")
		return fmt.Sprintf(`window.__rpcResolve(%d,false,%s)`, id, b)
	}
	return fmt.Sprintf(`window.__rpcResolve(%d,true,%s)`, id, b)
}

// ---------------------------------------------------------------- 工具

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func validHHMM(s string) bool {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
}
