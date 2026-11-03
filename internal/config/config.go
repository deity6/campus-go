// Package config 负责配置文件的读写与默认值。
//
// 设计要点：
//   - 配置文件与旧版 Python 完全兼容（同一份 config.json、同样的键名），
//     这样两个版本可以共用配置，迁移无需手工改。
//   - 读取时用「零值即默认」的策略：文件里缺的键不会覆盖默认值，
//     避免用户手工删掉某一行就导致程序行为异常。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 对应 config.json。
type Config struct {
	// 认证
	Gateway  string `json:"gateway"`
	Account  string `json:"account"`
	Password string `json:"password"`
	EthGW    string `json:"eth_gateway"`

	// 自动认证时段（整点）
	WatchStartHour int `json:"watch_start_hour"`
	WatchEndHour   int `json:"watch_end_hour"`

	// 检测节奏（秒）
	CheckInterval   int `json:"check_interval"`   // 稳定在线
	IdleInterval    int `json:"idle_interval"`    // 时段外
	SuspectInterval int `json:"suspect_interval"` // 有异常迹象

	// 重点时段
	IntensiveEnabled   bool   `json:"intensive_enabled"`
	IntensiveStart     string `json:"intensive_start"` // "03:50"
	IntensiveEnd       string `json:"intensive_end"`   // "04:15"
	IntensiveInterval  int    `json:"intensive_interval"`
	IntensiveThreshold int    `json:"intensive_threshold"`

	// 其他
	ReloginOutsideWindow bool   `json:"relogin_outside_window"`
	PingTarget           string `json:"ping_target"`

	// 隐私脱敏。这些偏好会被记住（冷处理），重启后仍然生效。
	// NameLevel: 0=保留姓+同学，1=隐藏姓，2=连“同学”身份也替换掉。
	// NetMasked: IP / MAC / 网关等信息默认打码，用户可临时查看。
	// CustomName 是称呼区的小彩蛋：长按后可改成任意文案，空值恢复默认。
	PrivacyNameLevel int    `json:"privacy_name_level"`
	PrivacyNetMasked bool   `json:"privacy_net_masked"`
	CustomName       string `json:"custom_name"`

	// 界面：使用引导是否已看过。
	// 首次为 false，播完置 true；用户在设置里取消勾选会重新变 false，
	// 下次启动再播一遍（播完又会被置回 true）。
	TourDone bool `json:"tour_done"`
}

// Default 返回一份合理的默认配置。
func Default() Config {
	return Config{
		Gateway:              "10.0.253.14",
		EthGW:                "192.168.5.1",
		WatchStartHour:       0,
		WatchEndHour:         8,
		CheckInterval:        180,
		IdleInterval:         1800,
		SuspectInterval:      15,
		IntensiveEnabled:     true,
		IntensiveStart:       "03:50",
		IntensiveEnd:         "04:15",
		IntensiveInterval:    5,
		IntensiveThreshold:   1,
		ReloginOutsideWindow: false,
		// 用公共 DNS 的 IP 而不是域名：域名要先解析，而 DNS 恰好是本工具
		// 最需要鉴别的那一环 —— 用域名会在 DNS 故障时把"DNS 坏了"
		// 误判成"断网了"，反过来又去重连，毫无意义。
		// 两个目标互为备份，任一可达即视为外网通。
		PingTarget: "223.5.5.5,119.29.29.29",

		// 网络标识（IP/MAC/网关）天然属于隐私，默认打码；
		// 姓名已经是“姓+同学”，默认保留这一级。
		PrivacyNameLevel: 0,
		PrivacyNetMasked: true,
	}
}

// Path 返回配置文件路径。
//
// 便携式的正确语义是「配置跟着 exe 走」，所以首选 exe 所在目录。
// 但开发调试时 exe 在临时目录（go run），因此加两条回退：
//  1. 环境变量 CAMPUS_CONFIG 显式指定
//  2. 当前工作目录
func Path() string {
	if p := strings.TrimSpace(os.Getenv("CAMPUS_CONFIG")); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "config.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		// exe 目录没有配置时，若当前目录有就先用它（开发场景）
		if wd, err2 := os.Getwd(); err2 == nil {
			if _, err3 := os.Stat(filepath.Join(wd, "config.json")); err3 == nil {
				return filepath.Join(wd, "config.json")
			}
		}
		return candidate
	}
	return "config.json"
}

// Load 读取配置；文件不存在或解析失败时返回默认值。
// 缺失的字段保持默认（JSON 反序列化对零值不覆盖，需先填默认再解）。
func Load() Config {
	cfg := Default()

	data, err := os.ReadFile(Path())
	if err != nil {
		return cfg
	}

	// 用一个「指针版」结构判断哪些键真实存在，避免 0 值覆盖默认
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default()
	}

	// 数值型字段若在文件里是 0（未设置），恢复为默认
	d := Default()
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = d.CheckInterval
	}
	if cfg.IdleInterval <= 0 {
		cfg.IdleInterval = d.IdleInterval
	}
	if cfg.SuspectInterval <= 0 {
		cfg.SuspectInterval = d.SuspectInterval
	}
	if cfg.IntensiveInterval <= 0 {
		cfg.IntensiveInterval = d.IntensiveInterval
	}
	if cfg.IntensiveThreshold <= 0 {
		cfg.IntensiveThreshold = d.IntensiveThreshold
	}
	if cfg.WatchStartHour == 0 && cfg.WatchEndHour == 0 {
		cfg.WatchStartHour, cfg.WatchEndHour = d.WatchStartHour, d.WatchEndHour
	}
	if strings.TrimSpace(cfg.IntensiveStart) == "" {
		cfg.IntensiveStart = d.IntensiveStart
	}
	if strings.TrimSpace(cfg.IntensiveEnd) == "" {
		cfg.IntensiveEnd = d.IntensiveEnd
	}
	if strings.TrimSpace(cfg.PingTarget) == "" {
		cfg.PingTarget = d.PingTarget
	}
	// 旧默认值（单个联通 DNS）迁移到新的多目标
	if strings.TrimSpace(cfg.PingTarget) == "223.109.82.212" {
		cfg.PingTarget = d.PingTarget
	}
	if cfg.PrivacyNameLevel < 0 || cfg.PrivacyNameLevel > 2 {
		cfg.PrivacyNameLevel = d.PrivacyNameLevel
	}
	return cfg
}

// Save 写回配置文件（0600 权限，含明文密码）。
func Save(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再改名，避免写一半断电导致配置损坏
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	return os.Rename(tmp, Path())
}

// PingTargets 把配置里的检测目标拆成列表（逗号分隔，允许空格）。
func (c Config) PingTargets() []string {
	raw := strings.Split(c.PingTarget, ",")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = append(out, "223.5.5.5")
	}
	return out
}

// PingTargetText 供界面显示。
func (c Config) PingTargetText() string {
	return strings.Join(c.PingTargets(), " / ")
}

// Complete 判断认证所需字段是否齐备。
func (c Config) Complete() bool {
	return strings.TrimSpace(c.Gateway) != "" &&
		strings.TrimSpace(c.Account) != "" &&
		c.Password != ""
}

// --- 时间窗口判断 ---

// InWatchWindow 判断当前是否处于自动认证时段。
func (c Config) InWatchWindow(hour int) bool {
	if c.WatchStartHour == c.WatchEndHour {
		return true // 全天
	}
	if c.WatchStartHour < c.WatchEndHour {
		return hour >= c.WatchStartHour && hour < c.WatchEndHour
	}
	return hour >= c.WatchStartHour || hour < c.WatchEndHour
}

// parseHHMM 把 "03:50" 解析成当天第几分钟；失败返回 -1。
func parseHHMM(s string) int {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return -1
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return -1
	}
	return h*60 + m
}

// IntensiveSpan 返回重点时段（分钟），跨度超过 60 分钟会被截断。
func (c Config) IntensiveSpan() (start, end int, ok bool) {
	if !c.IntensiveEnabled {
		return 0, 0, false
	}
	s := parseHHMM(c.IntensiveStart)
	e := parseHHMM(c.IntensiveEnd)
	if s < 0 || e < 0 || s == e {
		return 0, 0, false
	}
	span := (e - s + 24*60) % (24 * 60)
	if span > 60 {
		e = (s + 60) % (24 * 60)
	}
	return s, e, true
}

// InIntensiveWindow 判断当前是否处于重点时段（且必须在自动认证时段内）。
func (c Config) InIntensiveWindow(minuteOfDay, hour int) bool {
	if !c.InWatchWindow(hour) {
		return false
	}
	s, e, ok := c.IntensiveSpan()
	if !ok {
		return false
	}
	if s < e {
		return minuteOfDay >= s && minuteOfDay < e
	}
	return minuteOfDay >= s || minuteOfDay < e
}

// IntensiveText 供界面显示，如 "03:50-04:15"。
func (c Config) IntensiveText() string {
	if !c.IntensiveEnabled {
		return "未启用"
	}
	s, e, ok := c.IntensiveSpan()
	if !ok {
		return "未配置"
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", s/60, s%60, e/60, e%60)
}

// WatchText 供界面显示，如 "00:00-08:00"。
func (c Config) WatchText() string {
	if c.WatchStartHour == c.WatchEndHour {
		return "全天"
	}
	return fmt.Sprintf("%02d:00-%02d:00", c.WatchStartHour, c.WatchEndHour)
}
