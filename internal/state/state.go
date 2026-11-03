// Package state 保存「界面需要看到、但核心逻辑不关心」的运行时状态。
//
// 单独成包的原因：守护循环（watcher）写、HTTP 接口（api）读，
// 两边都不该 import 对方。放在这里作为双方共同的最小依赖。
package state

import (
	"sync"
	"time"
)

// Event 是事件流里的一条记录。
type Event struct {
	Time string `json:"time"`
	Kind string `json:"kind"` // info / ok / warn / error
	Text string `json:"text"`
}

// Phase 取值
const (
	PhaseUnknown   = "unknown"
	PhaseChecking  = "checking"
	PhaseOnline    = "online"
	PhaseOffline   = "offline"
	PhaseAuthing   = "authenticating"
	PhaseSuspended = "suspended"
)

const maxEvents = 60

// Store 是并发安全的运行时状态容器。
type Store struct {
	mu        sync.RWMutex
	startedAt time.Time

	phase     string
	detail    string
	headline  string
	hint      string
	situation string
	lastChk   time.Time
	chkOK     bool
	lastAuth  time.Time
	authOK    bool
	authCnt   int

	events []Event

	// 网络侧只读信息，由 watcher 更新，api 直读
	srcIP        string
	watching     bool
	backoffUntil time.Time
	configured   bool
	session      SessionInfo
	medium       string
}

// New 创建一个 Store。
func New() *Store {
	return &Store{
		startedAt: time.Now(),
		phase:     PhaseUnknown,
		events:    make([]Event, 0, maxEvents),
	}
}

// SetPhase 记录一次检测结果。
// SetStatus 一次写入状态、情境与两行文案，保证三者不会出现
// "状态已变、文案还是上一条"的错位。
func (s *Store) SetStatus(phase, situation, headline, hint, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
	if situation != "" {
		s.situation = situation
	}
	if headline != "" {
		s.headline = headline
	}
	if hint != "" {
		s.hint = hint
	}
	if detail != "" {
		s.detail = detail
	}
	if phase != PhaseSuspended {
		s.lastChk = time.Now()
		s.chkOK = phase == PhaseOnline
	}
}

func (s *Store) SetPhase(phase, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
	if detail != "" {
		s.detail = detail
	}
	// 上一条文案是针对旧状态的，留着会和新状态打架；
	// 清空后前端回退到按 phase 显示的通用词。
	s.headline, s.hint = "", ""
	if phase != PhaseSuspended {
		s.lastChk = time.Now()
		s.chkOK = phase == PhaseOnline
	}
}

// NoteLogin 记录一次认证结果。
func (s *Store) NoteLogin(ok bool, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAuth = time.Now()
	s.authOK = ok
	if ok {
		s.authCnt++
	}
	if detail != "" {
		s.detail = detail
	}
}

// AddEvent 往事件流头部插一条。
func (s *Store) AddEvent(kind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := Event{Time: time.Now().Format("15:04:05"), Kind: kind, Text: text}
	s.events = append([]Event{ev}, s.events...)
	if len(s.events) > maxEvents {
		s.events = s.events[:maxEvents]
	}
}

// SetNetwork 更新网络侧信息。
func (s *Store) SetNetwork(srcIP string, watching bool, configured bool, medium string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if srcIP != "" {
		s.srcIP = srcIP
	}
	if medium != "" {
		s.medium = medium
	}
	s.watching = watching
	s.configured = configured
}

// SetBackoff 由 watcher 在进入退避时调用。
func (s *Store) SetBackoff(until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backoffUntil = until
}

// SessionInfo 是从认证网关页面读到的在线会话信息。
//
// 网关会在 http://<gw>/ 返回一小段 JS 配置，里面直接带着
// 姓名(NID)、账号(uid)、内网 IP、登录时间、已用时长与流量 ——
// 不需要登录任何系统就能读到，用来做「欢迎 xx」最合适。
type SessionInfo struct {
	Valid bool   `json:"valid"`
	Name  string `json:"name"`
	UID   string `json:"uid"`
	IP    string `json:"ip"`
	MAC   string `json:"mac"`
	// MACFromGateway 表示 MAC 是网关认定的（登录响应 olmac），
	// 否则是本机网卡地址。两者在经路由器/多网卡时可能不同。
	MACFromGateway bool   `json:"mac_from_gateway"`
	StartAt        string `json:"start_at"`
	Minutes        int    `json:"minutes"`
	FlowKB         int    `json:"flow_kb"`
	Fee            string `json:"fee"`
}

// SetSession 更新会话信息。
func (s *Store) SetSession(info SessionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = info
}

// Session 返回最近一次读到的会话信息。
func (s *Store) Session() SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.session
}

// ClearSrcIP 在探测失败时清空缓存，避免界面显示过期地址。
func (s *Store) ClearSrcIP() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.srcIP = ""
}

// SrcIP 返回缓存的源 IP（只读，不做任何探测）。
func (s *Store) SrcIP() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.srcIP
}

// RuntimeView 是 runtime 段。
type RuntimeView struct {
	Phase string `json:"phase"`
	// Headline/Hint 是给普通用户看的两行文案（"网络正常 / 可以正常上网"）。
	// Detail 保留技术描述，只在日志与排查时用。
	Headline    string  `json:"headline"`
	Hint        string  `json:"hint"`
	Situation   string  `json:"situation"`
	Detail      string  `json:"detail"`
	LastCheckAt *string `json:"last_check_at"`
	LastCheckOK *bool   `json:"last_check_ok"`
	LastLoginAt *string `json:"last_login_at"`
	LastLoginOK *bool   `json:"last_login_ok"`
	LoginCount  int     `json:"login_count"`
	UptimeSec   int     `json:"uptime_sec"`
	StartedAt   string  `json:"started_at"`
	Events      []Event `json:"events"`
}

// NetworkView 是 network 段。
type NetworkView struct {
	SrcIP         string `json:"src_ip"`
	Watching      bool   `json:"watching"`
	BackoffRemain int    `json:"backoff_remain"`
	Medium        string `json:"medium"`
}

// Snapshot 返回 runtime 与 network 两段（供 /api/state 使用）。
func (s *Store) Snapshot() (RuntimeView, NetworkView) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rt := RuntimeView{
		Phase:      s.phase,
		Detail:     s.detail,
		Headline:   s.headline,
		Hint:       s.hint,
		Situation:  s.situation,
		LoginCount: s.authCnt,
		UptimeSec:  int(time.Since(s.startedAt).Seconds()),
		StartedAt:  s.startedAt.Format("2006-01-02 15:04:05"),
		Events:     make([]Event, len(s.events)),
	}
	copy(rt.Events, s.events)

	if !s.lastChk.IsZero() {
		v := s.lastChk.Format("15:04:05")
		ok := s.chkOK
		rt.LastCheckAt, rt.LastCheckOK = &v, &ok
	}
	if !s.lastAuth.IsZero() {
		v := s.lastAuth.Format("15:04:05")
		ok := s.authOK
		rt.LastLoginAt, rt.LastLoginOK = &v, &ok
	}

	remain := 0
	if !s.backoffUntil.IsZero() {
		if d := int(time.Until(s.backoffUntil).Seconds()); d > 0 {
			remain = d
		}
	}
	nv := NetworkView{
		SrcIP:         s.srcIP,
		Watching:      s.watching,
		BackoffRemain: remain,
		Medium:        s.medium,
	}
	return rt, nv
}

// Uptime 返回运行秒数。
func (s *Store) Uptime() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int(time.Since(s.startedAt).Seconds())
}
