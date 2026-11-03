/* ============================================================
   校园网认证 · 界面逻辑
   与守护进程通过本地 API 通信（token 由服务端注入）
   ============================================================ */

// 与内核通过 WebView2 消息通道通信，没有 HTTP、没有端口。
// 路径风格保留，便于阅读；内部映射到具体方法。
const H = (h) => `${String(Math.floor(h)).padStart(2, "0")}:${String(Math.round((h % 1) * 60)).padStart(2, "0")}`;

/* ---------------- API ---------------- */

const METHODS = {
  "GET /api/state": "state.get",
  "GET /api/config": "config.get",
  "GET /api/logs": "logs.get",
  "GET /api/autostart": "autostart.get",
  "POST /api/check": "check",
  "POST /api/login": "login",
  "POST /api/logout": "logout",
  "POST /api/config": "config.save",
  "POST /api/account": "account.save",
  "POST /api/test-account": "account.test",
  "POST /api/autostart": "autostart.set",
  "POST /api/open.url": "open.url",
};

async function api(path, opts = {}) {
  const method = opts.method || "GET";
  const bare = path.split("?")[0];
  const name = METHODS[`${method} ${bare}`];
  if (!name) throw new Error(`未知接口: ${method} ${bare}`);
  return window.__rpc(name, opts.body || {});
}

/* ---------------- 隐私脱敏 ---------------- */

// 前端保存一份本地偏好：轮询不会覆盖用户刚刚点下的选择，
// 保存成功后后端和本地会自然一致。
const privacy = {
  nameLevel: 0,       // 0=姓+同学，1=隐藏姓，2=连学生身份也替换
  netMasked: true,    // IP / MAC / 网关默认打码
  ready: false,
  nameAnimating: false,
  baseName: "",       // 后端返回的「姓+同学」
};
let demoNameActive = false;

const SHIELD_ON = '<path d="M12 3.5 19 6v5.5c0 4.6-3 7.6-7 9-4-1.4-7-4.4-7-9V6l7-2.5Z" stroke-linejoin="round"/><path d="m9 12 2.2 2.2L15.5 10" stroke-linecap="round" stroke-linejoin="round"/>';
const EYE_OFF = '<path d="M4 4l16 16" stroke-linecap="round"/><path d="M9.9 5.9A9.6 9.6 0 0 1 12 5.5c6.2 0 9.8 6.5 9.8 6.5a17.4 17.4 0 0 1-3.2 3.9M6.2 7.9A16.8 16.8 0 0 0 2.2 12s3.6 6.5 9.8 6.5c1.2 0 2.3-.2 3.3-.6" stroke-linecap="round"/><path d="M9.9 10.2a3.2 3.2 0 0 0 4.2 4.2" stroke-linecap="round"/>';

function protectedChipHTML() {
  return '<span class="secret-chip"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">' +
    SHIELD_ON + '</svg>已加敏</span>';
}

function renderSecret(el) {
  const value = el.dataset.secretValue || "";
  if (privacy.netMasked) {
    el.innerHTML = protectedChipHTML();
    el.title = "点击显示";
  } else {
    el.textContent = value || "—";
    el.title = value ? "点击隐藏" : "";
  }
}

function setSecret(el, kind, value) {
  if (!el) return;
  el.dataset.secretKind = kind;
  el.dataset.secretValue = value || "";
  renderSecret(el);
}

function applyPrivacyToSecrets() {
  document.querySelectorAll("[data-secret-kind]").forEach(renderSecret);
}

function syncPrivacyEyes() {
  document.querySelectorAll("[data-net-toggle]").forEach((btn) => {
    const revealed = !privacy.netMasked;
    btn.classList.toggle("revealed", revealed);
    btn.setAttribute("aria-pressed", revealed ? "true" : "false");
    btn.setAttribute("aria-label", revealed ? "隐藏网络信息" : "显示网络信息");
    btn.title = revealed ? "隐藏网络信息" : "显示网络信息";
    const svg = btn.querySelector("svg");
    if (svg) svg.innerHTML = revealed ? EYE_OFF : SHIELD_ON;
  });
}

function updatePrivacySwitches() {
  const g = document.getElementById("switchPrivacyGlobal");
  const n = document.getElementById("switchPrivacyName");
  const net = document.getElementById("switchPrivacyNet");
  if (g) g.setAttribute("aria-checked", (privacy.nameLevel > 0 && privacy.netMasked) ? "true" : "false");
  if (n) n.setAttribute("aria-checked", privacy.nameLevel > 0 ? "true" : "false");
  if (net) net.setAttribute("aria-checked", privacy.netMasked ? "true" : "false");
}

async function savePrivacy() {
  try {
    await api("/api/config", {
      method: "POST",
      body: {
        privacy_name_level: privacy.nameLevel,
        privacy_net_masked: privacy.netMasked,
      },
    });
    if (lastState && lastState.config) {
      lastState.config.privacy_name_level = privacy.nameLevel;
      lastState.config.privacy_net_masked = privacy.netMasked;
    }
  } catch (e) {
    toast(e.message, "err");
  }
}

document.querySelectorAll("[data-net-toggle]").forEach((btn) => {
  btn.addEventListener("click", () => {
    privacy.netMasked = !privacy.netMasked;
    syncPrivacyEyes();
    applyPrivacyToSecrets();
    updatePrivacySwitches();
    savePrivacy();
  });
});

// 加敏胶囊本身就是入口：点胶囊显示，再点真实值恢复加敏。
document.querySelectorAll("[data-secret-kind]").forEach((el) => {
  el.addEventListener("click", () => {
    if (!el.dataset.secretValue) return;
    privacy.netMasked = !privacy.netMasked;
    syncPrivacyEyes();
    applyPrivacyToSecrets();
    updatePrivacySwitches();
    savePrivacy();
  });
});

document.getElementById("switchPrivacyGlobal").addEventListener("click", () => {
  const on = document.getElementById("switchPrivacyGlobal").getAttribute("aria-checked") !== "true";
  privacy.nameLevel = on ? 2 : 0;
  privacy.netMasked = on;
  updatePrivacySwitches();
  syncPrivacyEyes();
  applyPrivacyToSecrets();
  renderName({ animate: true });
  savePrivacy();
  toast(on ? "已开启全局加敏" : "已关闭全局加敏", "ok");
});
document.getElementById("switchPrivacyName").addEventListener("click", () => {
  const on = document.getElementById("switchPrivacyName").getAttribute("aria-checked") !== "true";
  privacy.nameLevel = on ? 2 : 0;
  updatePrivacySwitches();
  renderName({ animate: true });
  savePrivacy();
});
document.getElementById("switchPrivacyNet").addEventListener("click", () => {
  privacy.netMasked = document.getElementById("switchPrivacyNet").getAttribute("aria-checked") !== "true";
  updatePrivacySwitches();
  syncPrivacyEyes();
  applyPrivacyToSecrets();
  savePrivacy();
});

/* ---------------- 姓名脱敏交互 ---------------- */

function nameDisplayParts(level, base) {
  const surname = String(base || "").replace(/同学$/, "") || "某";
  if (level === 0) {
    return [{ text: surname, kind: "name" }, { text: "同学", kind: "role" }];
  }
  if (level === 1) {
    return [{ text: "某", kind: "name" }, { text: "同学", kind: "role" }];
  }
  return [{ text: "朋友", kind: "alias" }];
}

function renderName({ animate = false } = {}) {
  const greet = document.getElementById("heroGreet");
  if (!greet || privacy.nameAnimating) return;
  const base = demoNameActive ? "青同学" : privacy.baseName;
  if (!base) {
    greet.dataset.line = "";
    greet.classList.remove("enter");
    greet.innerHTML = "";
    return;
  }
  const line = `${greetingWord()}|${base}|${privacy.nameLevel}|${demoNameActive ? "demo" : "real"}`;
  greet.dataset.line = line;

  const parts = nameDisplayParts(privacy.nameLevel, base);
  const chars = parts.flatMap((part) =>
    Array.from(part.text).map((ch) =>
      `<span class="name-char" data-part="${part.kind}">${escapeHtml(ch)}</span>`
    )
  ).join("");
  greet.innerHTML =
    `${escapeHtml(greetingWord())}，<button type="button" class="name-privacy" ` +
    `aria-label="切换姓名脱敏">${chars}` +
    `<svg class="name-shield" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">` +
    `<path d="M12 3.5 19 6v5.5c0 4.6-3 7.6-7 9-4-1.4-7-4.4-7-9V6l7-2.5Z" stroke-linejoin="round"/>` +
    `<path d="m9 12 2.2 2.2L15.5 10" stroke-linecap="round" stroke-linejoin="round"/></svg></button>`;

  greet.querySelector(".name-privacy").addEventListener("click", cycleNamePrivacy);
  if (animate) {
    greet.querySelectorAll(".name-char").forEach((ch, i) => {
      ch.style.animationDelay = `${i * 34}ms`;
      ch.classList.add("entering");
      setTimeout(() => ch.classList.remove("entering"), 420 + i * 34);
    });
  }
}

function cycleNamePrivacy() {
  if (privacy.nameAnimating) return;
  const next = (privacy.nameLevel + 1) % 3;
  privacy.nameLevel = next;
  animateNameChange();
  updatePrivacySwitches();
  savePrivacy();
}

function animateNameChange() {
  const greet = document.getElementById("heroGreet");
  const oldChars = greet ? Array.from(greet.querySelectorAll(".name-char")) : [];
  if (!oldChars.length) {
    renderName({ animate: true });
    return;
  }

  privacy.nameAnimating = true;
  oldChars.forEach((ch, i) => {
    const rect = ch.getBoundingClientRect();
    const center = rect.left + rect.width / 2;
    ch.style.setProperty("--scatter-x", `${(center % 2 === 0 ? -1 : 1) * (7 + i * 6)}px`);
    ch.style.setProperty("--scatter-rot", `${(i % 2 ? -1 : 1) * (9 + i * 5)}deg`);
    ch.style.animationDelay = `${i * 28}ms`;
    ch.classList.add("leaving");
  });

  setTimeout(() => {
    privacy.nameAnimating = false;
    renderName({ animate: true });
  }, 360 + oldChars.length * 28);
}

// 引导里没有登录会话时，放一个演示称呼；结束后把它散掉。
function showDemoName() {
  if (privacy.baseName) return;
  demoNameActive = true;
  renderName();
}

function hideDemoName() {
  if (!demoNameActive) return;
  const greet = document.getElementById("heroGreet");
  const oldChars = greet ? Array.from(greet.querySelectorAll(".name-char")) : [];
  demoNameActive = false;
  if (!oldChars.length) {
    renderName();
    return;
  }
  privacy.nameAnimating = true;
  oldChars.forEach((ch, i) => {
    ch.style.setProperty("--scatter-x", `${(i % 2 ? -1 : 1) * (10 + i * 7)}px`);
    ch.style.setProperty("--scatter-rot", `${(i % 2 ? -1 : 1) * (10 + i * 6)}deg`);
    ch.style.animationDelay = `${i * 24}ms`;
    ch.classList.add("leaving");
  });
  setTimeout(() => {
    privacy.nameAnimating = false;
    renderName();
  }, 340 + oldChars.length * 24);
}

/* ---------------- 确认框 ---------------- */

// 自绘确认框。不用 window.confirm：它在 WebView 里会带出
// "127.0.0.1:xxxxx 显示" 这种来源行，既不美观也不专业。
function askConfirm({ title, body, okText = "确定", cancelText = "取消", danger = false }) {
  return new Promise((resolve) => {
    const mask = document.getElementById("modalMask");
    const modal = mask.querySelector(".modal");
    const icon = document.getElementById("modalIcon");
    const btnOk = document.getElementById("modalOk");
    const btnCancel = document.getElementById("modalCancel");

    document.getElementById("modalTitle").textContent = title;
    document.getElementById("modalBody").textContent = body;
    btnOk.textContent = okText;
    btnCancel.textContent = cancelText;

    modal.classList.toggle("danger", !!danger);
    btnOk.className = "btn " + (danger ? "btn-danger" : "btn-primary");
    icon.innerHTML = danger
      ? '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"><path d="M12 8v5" stroke-linecap="round"/><circle cx="12" cy="16.4" r="1.1" fill="currentColor" stroke="none"/><path d="M10.3 3.9 2.6 17.4A1.9 1.9 0 0 0 4.3 20.3h15.4a1.9 1.9 0 0 0 1.7-2.9L13.7 3.9a1.9 1.9 0 0 0-3.4 0Z" stroke-linejoin="round"/></svg>'
      : '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"><circle cx="12" cy="12" r="9"/><path d="M12 7.6v5.2" stroke-linecap="round"/><circle cx="12" cy="16.3" r="1.05" fill="currentColor" stroke="none"/></svg>';

    mask.hidden = false;
    btnCancel.focus();

    const done = (val) => {
      mask.hidden = true;
      btnOk.removeEventListener("click", onOk);
      btnCancel.removeEventListener("click", onCancel);
      mask.removeEventListener("click", onMask);
      document.removeEventListener("keydown", onKey);
      resolve(val);
    };
    const onOk = () => done(true);
    const onCancel = () => done(false);
    // 点遮罩关闭，点对话框内部不关
    const onMask = (e) => { if (e.target === mask) done(false); };
    const onKey = (e) => {
      if (e.key === "Escape") done(false);
      if (e.key === "Enter") done(true);
    };

    btnOk.addEventListener("click", onOk);
    btnCancel.addEventListener("click", onCancel);
    mask.addEventListener("click", onMask);
    document.addEventListener("keydown", onKey);
  });
}

/* ---------------- Toast ---------------- */

function toast(msg, kind = "") {
  const wrap = document.getElementById("toastWrap");
  const el = document.createElement("div");
  el.className = "toast " + kind;
  el.textContent = msg;
  wrap.appendChild(el);
  setTimeout(() => {
    el.classList.add("out");
    setTimeout(() => el.remove(), 220);
  }, 2600);
}

/* ---------------- 视图切换 ---------------- */

const views = ["overview", "schedule", "account", "settings", "logs"];
let currentView = "overview";

function switchView(name) {
  if (!views.includes(name)) return;
  currentView = name;
  document.querySelectorAll(".nav-item").forEach((b) => {
    b.classList.toggle("active", b.dataset.view === name);
  });
  views.forEach((v) => {
    const el = document.getElementById("view-" + v);
    if (el) el.classList.toggle("active", v === name);
  });
  if (name === "logs") refreshLogs();
  if (name === "schedule") requestAnimationFrame(layoutTimeline);
}

document.querySelectorAll(".nav-item").forEach((btn) => {
  btn.addEventListener("click", () => {
    switchView(btn.dataset.view);
    try { history.replaceState(null, "", "#" + btn.dataset.view); } catch (e) {}
  });
});

// 支持 #schedule 之类的深链，刷新后仍停在原页
window.addEventListener("hashchange", () => {
  switchView(location.hash.replace("#", "") || "overview");
});

/* ---------------- 状态渲染 ---------------- */

const PHASE_TEXT = {
  online: "已连接",
  offline: "未连接",
  authenticating: "认证中",
  checking: "检测中",
  unknown: "未知",
};

let lastState = null;

function fmtUptime(sec) {
  if (!sec && sec !== 0) return "—";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  if (m > 0) return `${m} 分`;
  return `${sec} 秒`;
}

// 分钟数转成人话：38 分钟 / 2 小时 5 分
function fmtDuration(min) {
  min = Math.max(0, Math.round(min || 0));
  if (min < 60) return min + " 分钟";
  const h = Math.floor(min / 60);
  const m = min % 60;
  return m ? `${h} 小时 ${m} 分` : `${h} 小时`;
}

// KB 转成合适单位
function fmtFlow(kb) {
  kb = Math.max(0, kb || 0);
  if (kb < 1024) return kb + " KB";
  const mb = kb / 1024;
  if (mb < 1024) return mb.toFixed(1) + " MB";
  return (mb / 1024).toFixed(2) + " GB";
}

function greetingWord() {
  const h = new Date().getHours();
  if (h < 6) return "夜深了";
  if (h < 11) return "早上好";
  if (h < 14) return "中午好";
  if (h < 18) return "下午好";
  return "晚上好";
}

function renderState(s) {
  lastState = s;
  const rt = s.runtime || {};
  const cfg = s.config || {};
  const net = s.network || {};

  const phase = rt.phase || "unknown";
  const online = phase === "online";
  const pending = phase === "checking" || phase === "authenticating";
  // 主文案优先用后端给的 headline（"网络正常""需要认证"），
  // 没有才退回按 phase 的通用词 —— 技术细节只留在日志里。
  const text = rt.headline || PHASE_TEXT[phase] || "未知";

  // --- 侧栏 ---
  const dot = document.getElementById("sideDot");
  dot.className = "dot " + (online ? "online" : pending ? "pending" : "offline");
  document.getElementById("sideState").textContent = rt.headline || text;
  document.getElementById("sideSub").textContent =
    net.watching ? "自动认证时段内" : "时段外 · 可手动认证";

  // --- 隐私偏好（只在首次拿到配置时初始化，之后以本地为准）---
  if (!privacy.ready && cfg.privacy_name_level !== undefined) {
    privacy.nameLevel = cfg.privacy_name_level || 0;
    privacy.netMasked = cfg.privacy_net_masked !== false;
    privacy.ready = true;
    updatePrivacySwitches();
    syncPrivacyEyes();
  }

  // --- Hero ---
  const hero = document.getElementById("heroCard");
  hero.classList.toggle("is-online", online);
  hero.classList.toggle("is-offline", phase === "offline");
  // 在线时显示「晚上好，X同学」。
  // sess.name 已在后端脱敏（只留姓），界面不出现全名。
  const sess = s.session || {};
  const greet = document.getElementById("heroGreet");
  if (greet) {
    if (online && sess.valid && sess.name) {
      if (privacy.baseName !== sess.name) {
        privacy.baseName = sess.name;
        renderName();
        greet.classList.remove("enter");
        void greet.offsetWidth; // 强制回流，动画才会重新播放
        greet.classList.add("enter");
      }
    } else if (!demoNameActive && privacy.baseName) {
      privacy.baseName = "";
      renderName();
    }
  }

  // --- 使用引导 ---
  const swTour = document.getElementById("switchTour");
  if (swTour) swTour.setAttribute("aria-checked", cfg.tour_done ? "true" : "false");
  if (!tourChecked) {
    tourChecked = true;
    // 等首帧数据渲染完再弹，避免遮罩后面还是骨架屏
    if (!cfg.tour_done) setTimeout(startTour, 900);
  }

  // --- 本次连接（网关首页读到的会话信息）---
  const sp = document.getElementById("sessionPanel");
  if (sp) {
    const show = online && sess.valid;
    sp.hidden = !show;
    if (show) {
      setSecret(document.getElementById("svIp"), "ip", sess.ip);
      setSecret(document.getElementById("svMac"), "mac", sess.mac);
      // 网关登录响应里的 MAC 才是「在线信息」里那个；
      // 没登录过时退回本机网卡，标签要说清楚来源，否则对不上号。
      const macDt = document.querySelector("#sessionPanel dt:nth-of-type(2)");
      if (macDt) macDt.textContent = sess.mac_from_gateway ? "MAC 地址" : "本机网卡";
      document.getElementById("svStart").textContent = sess.start_at || "—";
      document.getElementById("svDuration").textContent = fmtDuration(sess.minutes);
      document.getElementById("svFlow").textContent = fmtFlow(sess.flow_kb);
    }
  }

  document.getElementById("heroState").textContent = text;
  document.getElementById("heroDetail").textContent = rt.hint || rt.detail || "—";

  // 环形：在线满圈，离线空圈，进行中转圈
  const ring = document.getElementById("ringFg");
  const C = 326.7;
  // 首次渲染关闭过渡，避免从默认值滑向目标值产生"半圈绿"的错觉
  if (!ring.dataset.init) {
    ring.style.transition = "none";
    ring.dataset.init = "1";
    requestAnimationFrame(() => { ring.style.transition = ""; });
  }
  if (online) {
    ring.style.stroke = "var(--ok)";
    ring.style.strokeDashoffset = "0";
  } else if (pending) {
    ring.style.stroke = "var(--warn)";
    ring.style.strokeDashoffset = String(C * 0.72);
  } else {
    ring.style.stroke = "var(--danger)";
    ring.style.strokeDashoffset = String(C * 0.94);
  }
  document.getElementById("ringLabel").textContent =
    online ? "ONLINE" : pending ? "…" : "OFFLINE";

  // --- 指标 ---
  setSecret(document.getElementById("mSrcIp"), "ip", net.src_ip);
  document.getElementById("mLoginCount").textContent = `${rt.login_count || 0} 次`;
  document.getElementById("mLastLogin").textContent = rt.last_login_at || "—";
  document.getElementById("mWatching").textContent =
    net.watching ? "时段内 · 自动" : "时段外 · 手动";
  document.getElementById("mWatching").style.color =
    net.watching ? "var(--ok)" : "var(--tx-2)";

  // --- 事件 ---
  renderEvents(rt.events || []);

  // --- 页脚 ---
  document.getElementById("footUptime").textContent = "已运行 " + fmtUptime(rt.uptime_sec);
  document.getElementById("kvStartedAt").textContent = rt.started_at || "—";
  document.getElementById("kvUptime").textContent = fmtUptime(rt.uptime_sec);

  // --- 账号表单预填 ---
  // 只在用户没动过表单时填充，否则每 2 秒轮询会把正在输入的内容冲掉
  const accTouched = document.getElementById("inpAccount").dataset.touched === "1";
  if (!accTouched) {
    const gi = document.getElementById("inpGateway");
    const ai = document.getElementById("inpAccount");
    const pi = document.getElementById("inpPassword");
    if (!gi.value && cfg.gateway) gi.value = cfg.gateway;
    if (!ai.value && cfg.account) ai.value = cfg.account;
    // 密码也回填：用户要求「保存后能看到、能改」。
    // 只在没被编辑过时填一次，避免 2 秒轮询把正在输入的内容冲掉。
    if (!pi.value && cfg.password) pi.value = cfg.password;
  }

  // --- 账号面板当前生效 ---
  setSecret(document.getElementById("kvGateway"), "ip", cfg.gateway);
  document.getElementById("kvAccount").textContent = cfg.account || "未设置";
  document.getElementById("kvPassword").textContent = cfg.password_set ? "已保存" : "未设置";

  // --- 时段卡片 ---
  if (cfg.watch_start_hour !== undefined && !scheduleDirty) {
    setSchedule(cfg.watch_start_hour, cfg.watch_end_hour, false);
  }
  document.getElementById("inpCheckInterval").value = cfg.check_interval ?? 60;
  document.getElementById("inpIdleInterval").value = cfg.idle_interval ?? 1800;

  // --- 重点时段 ---
  const iw = document.getElementById("intensiveWindow");
  if (iw) {
    iw.textContent = cfg.intensive_enabled ? (cfg.intensive_window || "—") : "已关闭";
    document.getElementById("intensiveInterval").textContent =
      cfg.intensive_enabled ? `${cfg.intensive_interval || 5} 秒` : "—";
    document.getElementById("intensiveThreshold").textContent =
      cfg.intensive_enabled ? `${cfg.intensive_threshold || 1} 次即重连` : "—";
    const badge = document.getElementById("intensiveBadge");
    badge.textContent = !cfg.intensive_enabled ? "未启用"
                      : cfg.intensive_active ? "进行中" : "待命中";
    badge.className = "badge" + (cfg.intensive_active ? " active" : "");
  }

  const fv = document.getElementById("footVersion");
  if (fv) fv.textContent = "v" + (s.version || "3.0");
  const kvv = document.getElementById("kvVersion");
  if (kvv) kvv.textContent = s.version || "3.0";
  const kvp = document.getElementById("kvPing");
  if (kvp) kvp.textContent = cfg.ping_target || "—";

  // 第一份数据到手，撤掉骨架屏
  const boot = document.getElementById("boot");
  if (boot) {
    boot.classList.add("gone");
    setTimeout(() => boot.remove(), 260);
  }

  const sn = document.getElementById("scheduleNow");
  sn.textContent = net.watching ? "当前在时段内" : "当前在时段外";
  sn.style.color = net.watching ? "var(--ok)" : "var(--tx-3)";
}

function renderEvents(events) {
  const ul = document.getElementById("eventList");
  if (!events.length) {
    ul.innerHTML = '<li class="event-empty">暂无事件</li>';
    return;
  }
  ul.innerHTML = events
    .map(
      (e) =>
        `<li><span class="event-time">${e.time}</span><span class="event-text ${e.kind || ""}">${escapeHtml(e.text)}</span></li>`
    )
    .join("");
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

/* ---------------- 轮询 ---------------- */

let pollTimer = null;

async function poll() {
  try {
    const s = await api("/api/state");
    renderState(s);
    document.body.classList.remove("loading");
  } catch (e) {
    document.getElementById("sideState").textContent = "连接断开";
    document.getElementById("sideSub").textContent = e.message;
    document.getElementById("sideDot").className = "dot offline";
  }
}

function startPolling() {
  poll();
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(poll, 2000);
}

/* ---------------- 动作 ---------------- */

function busy(btn, on, label) {
  if (on) {
    btn.dataset._old = btn.innerHTML;
    btn.disabled = true;
    btn.innerHTML = label || "处理中…";
  } else {
    btn.disabled = false;
    if (btn.dataset._old) btn.innerHTML = btn.dataset._old;
  }
}

document.getElementById("btnCheck").addEventListener("click", async (e) => {
  const b = e.currentTarget;
  busy(b, true, "检测中…");
  try {
    const r = await api("/api/check", { method: "POST" });
    toast(r.online ? "当前在线" : "当前离线：" + r.detail, r.online ? "ok" : "");
    poll();
  } catch (err) {
    toast(err.message, "err");
  } finally {
    busy(b, false);
  }
});

// 去账号页并聚焦到第一个空字段
function gotoAccountForm(reason) {
  switchView("account");
  if (reason) toast(reason, "err");
  // 等视图切完再聚焦，否则元素还没显示出来
  setTimeout(() => {
    const acc = document.getElementById("inpAccount");
    const gw = document.getElementById("inpGateway");
    const target = (gw && !gw.value) ? gw : acc;
    if (target) { target.focus(); target.select && target.select(); }
  }, 260);
}

document.getElementById("btnLogin").addEventListener("click", async (e) => {
  const b = e.currentTarget;
  const cfg0 = lastState && lastState.config;
  if (!cfg0 || !cfg0.configured) {
    // 没账号密码就点认证，十有八九是新装 —— 直接带去填，别让它"失败"
    gotoAccountForm("先填好账号和密码，回来再点认证");
    return;
  }
  busy(b, true, "认证中…");
  try {
    const r = await api("/api/login", { method: "POST" });
    if (r.need_config) {
      gotoAccountForm(r.error || "还没填账号和密码");
      return;
    }
    if (r.already_online) toast(r.detail, "ok");
    else toast(r.ok ? "认证成功，校园网已连接" : "认证失败：" + r.detail, r.ok ? "ok" : "err");
    poll();
  } catch (err) {
    toast(err.message, "err");
  } finally {
    busy(b, false);
  }
});

document.getElementById("btnLogout").addEventListener("click", async () => {
  const yes = await askConfirm({
    title: "断开认证？",
    body: "这会让本机失去校园网认证，需要重新连接才能上网。\n" +
          "如果正在下载或开会，请先确认。",
    okText: "确认断开",
    cancelText: "不断开",
    danger: true,
  });
  if (!yes) return;

  const btn = document.getElementById("btnLogout");
  busy(btn, true);
  try {
    const r = await api("/api/logout", { method: "POST" });
    if (r.ok) {
      toast(r.detail || "已注销认证", "ok");
    } else {
      // 网关两条注销接口都没生效时，把用户引到自助服务
      const go = await askConfirm({
        title: "没能断开",
        body: (r.error || "注销未生效") + "\n\n要现在打开自助服务页面手动注销吗？",
        okText: "打开自助服务",
        cancelText: "算了",
      });
      if (go) {
        await api("/api/open.url", {
          method: "POST",
          body: { url: `http://${lastState?.config?.gateway || ""}/` },
        });
      }
    }
    poll();
  } catch (err) {
    toast(err.message, "err");
  } finally {
    busy(btn, false);
  }
});

/* ---------------- 时间轴 ---------------- */

const tl = {
  el: document.getElementById("timeline"),
  range: document.getElementById("tlRange"),
  start: 0,
  end: 8,
  drag: null,
  dirty: false,
};

let scheduleDirty = false;

function hourFromX(clientX) {
  const r = tl.el.getBoundingClientRect();
  const p = Math.min(1, Math.max(0, (clientX - r.left) / r.width));
  // 后端按整点执行，滑杆也按整点吸附，不让界面暗示能保存 15 分钟。
  return Math.round(p * 24);
}

(function buildScheduleSelects() {
  const start = document.getElementById("selScheduleStart");
  const end = document.getElementById("selScheduleEnd");
  for (let h = 0; h < 24; h++) {
    start.add(new Option(H(h), String(h)));
  }
  for (let h = 1; h <= 24; h++) {
    end.add(new Option(H(h), String(h)));
  }
  start.addEventListener("change", () => {
    const v = Number(start.value);
    setSchedule(v, v >= tl.end ? Math.min(24, v + 1) : tl.end);
  });
  end.addEventListener("change", () => {
    const v = Number(end.value);
    setSchedule(v <= tl.start ? Math.max(0, v - 1) : tl.start, v);
  });
})();

function setSchedule(start, end, dirty = true) {
  if (start === end) { start = 0; end = 24; }
  let a = Math.min(23, Math.max(0, Math.round(start)));
  let b = Math.min(24, Math.max(1, Math.round(end)));
  // 保证 a < b；相等表示"全天"。拖过头时交换而不是允许跨零点，
  // 跨零点会让时间轴渲染与交互复杂度翻倍，收益很小。
  if (a > b) [a, b] = [b, a];
  if (a === b) { a = 0; b = 24; }
  tl.start = a;
  tl.end = b;
  if (dirty) scheduleDirty = true;
  renderTimeline();
}

function layoutTimeline() {
  renderTimeline();
}

function renderTimeline() {
  const s = tl.start, e = tl.end;
  const pct = (h) => (h / 24) * 100;

  document.getElementById("selScheduleStart").value = String(s);
  document.getElementById("selScheduleEnd").value = String(e);
  document.getElementById("roDuration").textContent = fmtDuration((e - s) * 60);
  const handleL = document.getElementById("handleL");
  const handleR = document.getElementById("handleR");
  handleL.setAttribute("aria-valuemin", "0");
  handleL.setAttribute("aria-valuemax", "23");
  handleL.setAttribute("aria-valuenow", String(s));
  handleL.setAttribute("aria-valuetext", H(s));
  handleR.setAttribute("aria-valuemin", "1");
  handleR.setAttribute("aria-valuemax", "24");
  handleR.setAttribute("aria-valuenow", String(e));
  handleR.setAttribute("aria-valuetext", H(e));

  const seg = document.getElementById("tlRange");
  seg.style.left = pct(s) + "%";
  seg.style.width = Math.max(0.5, pct(e) - pct(s)) + "%";

  // 隐藏可能残留的第二段
  const seg2 = document.getElementById("tlRange2");
  if (seg2) seg2.style.display = "none";

  const now = new Date();
  const nowH = now.getHours() + now.getMinutes() / 60;
  document.getElementById("tlNow").style.left = pct(nowH) + "%";

  const allDay = (e - s) >= 23.99;
  const inWin = allDay || (nowH >= s && nowH < e);
  const hint = document.getElementById("roHint");
  hint.className = "readout-hint " + (inWin ? "in" : "out");
  hint.textContent = allDay ? "全天自动认证" : inWin ? "现在：时段内" : "现在：时段外";
}

function bindRange(el, mode) {
  el.addEventListener("pointerdown", (ev) => {
    if (ev.target.classList.contains("tl-handle")) return;
    ev.preventDefault();
    el.setPointerCapture(ev.pointerId);
    tl.drag = {
      type: "move",
      x0: ev.clientX,
      s0: tl.start,
      e0: tl.end,
    };
  });

  el.addEventListener("pointermove", (ev) => {
    if (!tl.drag || tl.drag.type !== "move") return;
    const r = tl.el.getBoundingClientRect();
    const dh = Math.round(((ev.clientX - tl.drag.x0) / r.width) * 24);
    const span = tl.drag.e0 - tl.drag.s0;
    let ns = tl.drag.s0 + dh;
    let ne = tl.drag.e0 + dh;
    // 整体平移：碰到边界时钳住，不压缩跨度
    if (ns < 0) { ne -= ns; ns = 0; }
    if (ne > 24) { ns -= (ne - 24); ne = 24; }
    setSchedule(ns, ne);
  });

  el.addEventListener("pointerup", () => {
    if (tl.drag && tl.drag.type === "move") tl.drag = null;
  });
}

function bindHandle(handle, which) {
  handle.addEventListener("pointerdown", (ev) => {
    ev.preventDefault();
    ev.stopPropagation();
    handle.setPointerCapture(ev.pointerId);
    tl.drag = { type: which };
  });
  handle.addEventListener("pointermove", (ev) => {
    if (!tl.drag || tl.drag.type !== which) return;
    const h = hourFromX(ev.clientX);
    if (which === "start") setSchedule(h, tl.end);
    else setSchedule(tl.start, h);
  });
  handle.addEventListener("pointerup", () => {
    if (tl.drag && tl.drag.type === which) tl.drag = null;
  });
  handle.addEventListener("keydown", (ev) => {
    let next = which === "start" ? tl.start : tl.end;
    if (["ArrowLeft", "ArrowDown"].includes(ev.key)) next--;
    else if (["ArrowRight", "ArrowUp"].includes(ev.key)) next++;
    else if (ev.key === "Home") next = which === "start" ? 0 : 1;
    else if (ev.key === "End") next = which === "start" ? 23 : 24;
    else return;
    ev.preventDefault();
    if (which === "start") setSchedule(next, next >= tl.end ? Math.min(24, next + 1) : tl.end);
    else setSchedule(next <= tl.start ? Math.max(0, next - 1) : tl.start, next);
  });
}

// 初始化刻度
(function buildTicks() {
  const wrap = document.getElementById("tlTicks");
  [0, 3, 6, 9, 12, 15, 18, 21, 24].forEach((h) => {
    const d = document.createElement("div");
    d.className = "tl-tick";
    d.style.left = (h / 24) * 100 + "%";
    d.textContent = String(h).padStart(2, "0");
    wrap.appendChild(d);
  });
})();

bindRange(tl.range, "main");
bindHandle(document.getElementById("handleL"), "start");
bindHandle(document.getElementById("handleR"), "end");

async function saveSchedule() {
  const ok = await askConfirm({
    title: "保存自动认证时段？",
    body: `将设为 ${H(tl.start)} → ${H(tl.end)}。\n` +
          "时段内掉线会自动重新认证；时段外只检测，需要手动点「立即认证」。",
    okText: "保存",
  });
  if (!ok) return;
  try {
    await api("/api/config", {
      method: "POST",
      body: {
        watch_start_hour: tl.start,
        watch_end_hour: tl.end,
        check_interval: clamp(parseInt(document.getElementById("inpCheckInterval").value, 10), 10, 3600, 60),
        idle_interval: clamp(parseInt(document.getElementById("inpIdleInterval").value, 10), 60, 86400, 1800),
      },
    });
    scheduleDirty = false;
    toast("已保存，立即生效", "ok");
    poll();
  } catch (e) {
    toast(e.message, "err");
  }
}

document.getElementById("btnSaveSchedule").addEventListener("click", saveSchedule);
document.getElementById("btnSaveDaily").addEventListener("click", saveSchedule);

/* 重点时段使用应用内弹窗，不再调用系统 prompt。 */
const intensiveMask = document.getElementById("intensiveMask");
const intensiveStart = document.getElementById("inpIntensiveStart");
const intensiveEnd = document.getElementById("inpIntensiveEnd");
const intensiveSwitch = document.getElementById("switchIntensive");
let intensivePreviousFocus = null;

function hhmmToMinutes(value) {
  const m = String(value).match(/^(\d{2}):(\d{2})$/);
  if (!m) return -1;
  const h = Number(m[1]), min = Number(m[2]);
  return h <= 23 && min <= 59 ? h * 60 + min : -1;
}

function minutesToHHMM(value) {
  const v = ((value % 1440) + 1440) % 1440;
  return `${String(Math.floor(v / 60)).padStart(2, "0")}:${String(v % 60).padStart(2, "0")}`;
}

function intensiveSpan() {
  const start = hhmmToMinutes(intensiveStart.value);
  const end = hhmmToMinutes(intensiveEnd.value);
  if (start < 0 || end < 0) return -1;
  return (end - start + 1440) % 1440;
}

function updateIntensiveEditor() {
  const enabled = intensiveSwitch.getAttribute("aria-checked") === "true";
  const row = document.getElementById("intensiveTimeRow");
  const presets = document.getElementById("durationPresets");
  row.classList.toggle("disabled", !enabled);
  presets.classList.toggle("disabled", !enabled);
  intensiveStart.disabled = !enabled;
  intensiveEnd.disabled = !enabled;

  const duration = document.getElementById("intensiveDuration");
  const error = document.getElementById("intensiveError");
  const span = intensiveSpan();
  duration.textContent = "";
  error.textContent = "";
  if (!enabled) {
    duration.textContent = "已关闭，保存后按常规频率检测";
    duration.style.color = "var(--tx-3)";
    return true;
  }
  if (span <= 0) {
    error.textContent = span === 0 ? "开始和结束时间不能相同" : "请选择有效的开始和结束时间";
    return false;
  }
  if (span > 60) {
    error.textContent = "重点时段最长 1 小时，可跨零点设置";
    return false;
  }
  const crosses = hhmmToMinutes(intensiveEnd.value) <= hhmmToMinutes(intensiveStart.value);
  duration.textContent = `持续 ${span} 分钟${crosses ? " · 跨零点" : ""}`;
  duration.style.color = "var(--ok)";
  return true;
}

function openIntensiveEditor() {
  const cfg = lastState && lastState.config;
  if (!cfg) return;
  const current = (cfg.intensive_window || "03:50-04:15").split("-");
  intensiveStart.value = /^\d{2}:\d{2}$/.test(current[0]) ? current[0] : "03:50";
  intensiveEnd.value = /^\d{2}:\d{2}$/.test(current[1]) ? current[1] : "04:15";
  intensiveSwitch.setAttribute("aria-checked", cfg.intensive_enabled ? "true" : "false");
  intensivePreviousFocus = document.activeElement;
  intensiveMask.hidden = false;
  updateIntensiveEditor();
  (cfg.intensive_enabled ? intensiveStart : intensiveSwitch).focus();
}

function closeIntensiveEditor() {
  intensiveMask.hidden = true;
  if (intensivePreviousFocus && intensivePreviousFocus.focus) intensivePreviousFocus.focus();
}

document.getElementById("btnEditIntensive").addEventListener("click", openIntensiveEditor);
document.getElementById("intensiveCancel").addEventListener("click", closeIntensiveEditor);
intensiveMask.addEventListener("click", (ev) => {
  if (ev.target === intensiveMask) closeIntensiveEditor();
});
intensiveSwitch.addEventListener("click", () => {
  const on = intensiveSwitch.getAttribute("aria-checked") === "true";
  intensiveSwitch.setAttribute("aria-checked", on ? "false" : "true");
  updateIntensiveEditor();
});
intensiveStart.addEventListener("input", updateIntensiveEditor);
intensiveEnd.addEventListener("input", updateIntensiveEditor);
document.querySelectorAll("#durationPresets button").forEach((btn) => {
  btn.addEventListener("click", () => {
    const start = hhmmToMinutes(intensiveStart.value);
    if (start < 0) return;
    intensiveEnd.value = minutesToHHMM(start + Number(btn.dataset.minutes));
    updateIntensiveEditor();
  });
});
document.addEventListener("keydown", (ev) => {
  if (intensiveMask.hidden) return;
  if (ev.key === "Escape") closeIntensiveEditor();
  if (ev.key === "Enter" && ev.target.tagName !== "BUTTON") {
    ev.preventDefault();
    document.getElementById("intensiveSave").click();
  }
});
document.getElementById("intensiveSave").addEventListener("click", async (ev) => {
  if (!updateIntensiveEditor()) return;
  const btn = ev.currentTarget;
  busy(btn, true, "保存中…");
  try {
    await api("/api/config", {
      method: "POST",
      body: {
        intensive_enabled: intensiveSwitch.getAttribute("aria-checked") === "true",
        intensive_start: intensiveStart.value,
        intensive_end: intensiveEnd.value,
      },
    });
    closeIntensiveEditor();
    toast("重点时段已更新，立即生效", "ok");
    poll();
  } catch (e) {
    document.getElementById("intensiveError").textContent = e.message;
  } finally {
    busy(btn, false);
  }
});

document.getElementById("btnResetSchedule").addEventListener("click", () => {
  setSchedule(0, 8);
  document.getElementById("inpCheckInterval").value = 60;
  document.getElementById("inpIdleInterval").value = 1800;
  toast("已恢复默认值，记得点保存");
});

function clamp(v, min, max, dflt) {
  if (!Number.isFinite(v)) return dflt;
  return Math.min(max, Math.max(min, v));
}

/* ---------------- 账号 ---------------- */

// 标记用户是否编辑过（避免轮询覆盖输入）
document.getElementById("inpAccount").addEventListener("input", (e) => {
  e.target.dataset.touched = "1";
});
document.getElementById("inpGateway").addEventListener("input", (e) => {
  e.target.dataset.touched = "1";
});
document.getElementById("inpPassword").addEventListener("input", (e) => {
  e.target.dataset.touched = "1";
});

document.getElementById("btnTogglePwd").addEventListener("click", (e) => {
  const btn = e.currentTarget;
  const inp = document.getElementById("inpPassword");
  const show = inp.type === "password";
  inp.type = show ? "text" : "password";
  btn.classList.toggle("on", show);
  btn.setAttribute("aria-label", show ? "隐藏密码" : "显示密码");
  btn.title = show ? "隐藏密码" : "显示密码";
  inp.focus();
});

document.getElementById("btnSaveAccount").addEventListener("click", async (e) => {
  const b = e.currentTarget;
  const body = {
    gateway: document.getElementById("inpGateway").value.trim(),
    account: document.getElementById("inpAccount").value.trim(),
    password: document.getElementById("inpPassword").value,
  };
  const msg = document.getElementById("accountMsg");
  if (!body.gateway || !body.account) {
    msg.className = "inline-msg err";
    msg.textContent = "网关和账号要填";
    return;
  }
  if (!body.password) {
    msg.className = "inline-msg err";
    msg.textContent = "请填写密码";
    return;
  }
  if (!body.password) delete body.password;   // 留空 = 保持原密码
  busy(b, true, "保存中…");
  try {
    const r = await api("/api/account", { method: "POST", body });
    msg.className = "inline-msg ok";
    msg.textContent = "已保存并生效";
    toast("账号已保存", "ok");
    poll();
  } catch (err) {
    msg.className = "inline-msg err";
    msg.textContent = err.message;
  } finally {
    busy(b, false);
  }
});

document.getElementById("btnTestAccount").addEventListener("click", async (e) => {
  const b = e.currentTarget;
  const body = {
    gateway: document.getElementById("inpGateway").value.trim(),
    account: document.getElementById("inpAccount").value.trim(),
    password: document.getElementById("inpPassword").value,
  };
  const msg = document.getElementById("accountMsg");
  busy(b, true, "测试中…");
  msg.className = "inline-msg";
  msg.textContent = "正在向网关发送登录请求…";
  try {
    const r = await api("/api/test-account", { method: "POST", body });
    if (r.ok) {
      msg.className = "inline-msg ok";
      msg.textContent = r.detail || "登录成功";
      toast("测试登录成功", "ok");
    } else {
      msg.className = "inline-msg err";
      msg.textContent = r.error || "登录失败";
    }
  } catch (err) {
    msg.className = "inline-msg err";
    msg.textContent = err.message;
  } finally {
    busy(b, false);
  }
});

/* ---------------- 设置 ---------------- */

let autostartOn = false;

async function loadAutostart() {
  try {
    const r = await api("/api/autostart");
    autostartOn = !!r.enabled;
    const sw = document.getElementById("switchAutostart");
    sw.setAttribute("aria-checked", autostartOn ? "true" : "false");
    document.getElementById("autostartTarget").textContent =
      autostartOn ? "已启用 · 登录 Windows 时自动运行"
                  : "当前未启用，开机后需手动启动";
  } catch (e) {
    /* 忽略 */
  }
}

document.getElementById("switchAutostart").addEventListener("click", async () => {
  const next = !autostartOn;
  try {
    const r = await api("/api/autostart", { method: "POST", body: { enable: next } });
    if (!r.ok) throw new Error(r.error || "操作失败");
    autostartOn = !!r.enabled;
    document.getElementById("switchAutostart").setAttribute("aria-checked", autostartOn ? "true" : "false");
    document.getElementById("autostartTarget").textContent =
      autostartOn ? "登录 Windows 时自动运行" : "当前未启用，开机后需手动启动";
    toast(autostartOn ? "已开启开机自启" : "已关闭开机自启", "ok");
  } catch (e) {
    toast(e.message, "err");
  }
});

/* ---------------- 日志 ---------------- */

async function refreshLogs() {
  try {
    const r = await api("/api/logs?n=200");
    const box = document.getElementById("logBox");
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 40;
    box.textContent = (r.lines || []).join("\n") || "（暂无日志）";
    if (atBottom) box.scrollTop = box.scrollHeight;
  } catch (e) {
    document.getElementById("logBox").textContent = "读取失败：" + e.message;
  }
}

document.getElementById("btnRefreshLog").addEventListener("click", refreshLogs);

setInterval(() => {
  if (currentView === "logs" && document.getElementById("chkAutoLog").checked) {
    refreshLogs();
  }
}, 3000);

/* ---------------- 启动 ---------------- */

window.addEventListener("resize", () => {
  if (currentView === "schedule") renderTimeline();
});

(async function init() {
  const initial = location.hash.replace("#", "");
  if (views.includes(initial)) switchView(initial);
  await loadAutostart();
  startPolling();
  setTimeout(() => {
    if (currentView === "schedule") renderTimeline();
  }, 300);
})();

/* ---------------- 使用引导 ---------------- */

// 五步，对应新用户第一次打开真正会迷惑的地方。
// 元素用选择器描述，切换视图不影响定位（侧栏一直可见）。
const TOUR_STEPS = [
  {
    sel: "#heroCard",
    title: "这里是当前状态",
    text: "网络通不通、这次连了多久，都显示在这一块。掉线时会变成「需要认证」。",
  },
  {
    sel: ".name-privacy",
    title: "截图前可以加敏",
    text: "点一下称呼，姓氏会散开；再点一下连「同学」也会收起来。",
  },
  {
    sel: ".actions",
    title: "需要上网时点这里",
    text: "「立即认证」马上连一次，几秒就好；「断开认证」是主动断开，会先问你一句。",
  },
  {
    sel: '[data-view="schedule"]',
    title: "让它自己连",
    text: "设一段时间，期间掉线会自动帮你认证。已知会断网的那段可以单独设得更灵敏。",
  },
  {
    sel: '[data-view="account"]',
    title: "账号只填一次",
    text: "网关、账号、密码都存在本机，之后不用再管。密码框右侧的眼睛能看明文。",
  },
  {
    sel: '[data-view="settings"]',
    title: "开机自动运行",
    text: "在这里可以打开开机自启。以后想再看一遍引导，把「使用引导」关掉即可。",
  },
];

let tourIdx = 0;
let tourChecked = false;

function placeTour() {
  const st = TOUR_STEPS[tourIdx];
  if (!st) return;
  const el = document.querySelector(st.sel);
  const hole = document.getElementById("tourHole");
  const tip = document.getElementById("tourTip");
  if (!el || !hole || !tip) { endTour(true); return; }

  const r = el.getBoundingClientRect();
  const pad = 6;
  const hx = Math.max(4, r.left - pad);
  const hy = Math.max(4, r.top - pad);
  const hw = Math.min(window.innerWidth - hx - 4, r.width + pad * 2);
  const hh = r.height + pad * 2;

  hole.style.left = hx + "px";
  hole.style.top = hy + "px";
  hole.style.width = hw + "px";
  hole.style.height = hh + "px";

  // 气泡宽度与 HTML 里一致；先量高度再决定放上面还是下面
  const tw = tip.offsetWidth || 302;
  const th = tip.offsetHeight || 160;
  let tx = Math.min(Math.max(12, r.left), window.innerWidth - tw - 12);
  let ty = hy + hh + 14;
  let below = true;
  if (ty + th > window.innerHeight - 10) {
    ty = Math.max(10, hy - th - 14);
    below = false;
  }
  tip.classList.toggle("below", below);
  tip.classList.toggle("above", !below);
  tip.style.left = tx + "px";
  tip.style.top = ty + "px";
  // 箭头跟着目标走，气泡被挤到边上时也不会指错
  const arrow = Math.min(Math.max(18, r.left - tx + 16), tw - 30);
  tip.style.setProperty("--arrow", arrow + "px");
}

function showStep() {
  const st = TOUR_STEPS[tourIdx];
  if (!st) return;
  document.getElementById("tourStep").textContent =
    `第 ${tourIdx + 1} / ${TOUR_STEPS.length} 步`;
  document.getElementById("tourTitle").textContent = st.title;
  document.getElementById("tourText").textContent = st.text;
  document.getElementById("tourNext").textContent =
    tourIdx === TOUR_STEPS.length - 1 ? "完成" : "下一步";
  // 内容变了高度就变了，等一帧再定位
  requestAnimationFrame(placeTour);
}

function startTour() {
  tourIdx = 0;
  const t = document.getElementById("tour");
  if (!t) return;
  showDemoName();
  t.hidden = false;
  showStep();
}

// markDone：跳过也算"看过"，否则每次打开都会再弹，很烦
function endTour(markDone) {
  const t = document.getElementById("tour");
  if (!t || t.hidden) return;
  t.hidden = true;
  hideDemoName();
  if (!markDone) return;
  api("/api/config", { method: "POST", body: { tour_done: true } })
    .then(() => {
      if (lastState && lastState.config) lastState.config.tour_done = true;
      const sw = document.getElementById("switchTour");
      if (sw) sw.setAttribute("aria-checked", "true");
    })
    .catch(() => {});
}

document.getElementById("tourNext").addEventListener("click", () => {
  if (tourIdx >= TOUR_STEPS.length - 1) { endTour(true); return; }
  tourIdx++;
  showStep();
});
document.getElementById("tourSkip").addEventListener("click", () => endTour(true));
window.addEventListener("resize", () => {
  const t = document.getElementById("tour");
  if (t && !t.hidden) placeTour();
});
document.addEventListener("keydown", (e) => {
  const t = document.getElementById("tour");
  if (!t || t.hidden) return;
  if (e.key === "Escape") endTour(true);
  if (e.key === "Enter") document.getElementById("tourNext").click();
});

// 设置页的「使用引导」开关：关掉表示下次启动再放一遍
document.getElementById("switchTour").addEventListener("click", async (e) => {
  const btn = e.currentTarget;
  const next = btn.getAttribute("aria-checked") !== "true";
  btn.setAttribute("aria-checked", next ? "true" : "false");
  try {
    await api("/api/config", { method: "POST", body: { tour_done: next } });
    if (lastState && lastState.config) lastState.config.tour_done = next;
    toast(next ? "下次启动不再播放引导" : "下次启动会再播放一遍引导", "ok");
  } catch (err) {
    btn.setAttribute("aria-checked", next ? "false" : "true");
    toast(err.message, "err");
  }
});
