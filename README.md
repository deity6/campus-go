# Campus Auto Login

[简体中文](README.zh-CN.md)

A Windows desktop tool for Dr.COM / web-portal campus network auto login. Single portable executable, no runtime installation, tray-resident, with automatic reconnection. Supports wired access and passwordless campus Wi-Fi.

> **Compatibility note:** this project is built and tested against the **WZUT** campus network authentication flow. Other schools or gateway variants are not guaranteed to work without adaptation.

| Overview | Privacy controls |
| --- | --- |
| ![Overview](docs/overview.png) | ![Privacy settings](docs/privacy-settings.png) |

## Features

- **Auto login / auto reconnect**: adaptive watch loop with an optional intensive window for known outage periods.
- **Manual controls**: login now, disconnect (dual logout path), and check connection.
- **Schedule planning**: daily auto-login hours plus an intensive window up to 1 hour, including cross-midnight.
- **Privacy masking**: name, campus IP, MAC, and gateway are masked by default; preferences persist locally.
- **Wired / Wi-Fi**: detects the source IP that can reach the auth gateway; no wired-only assumption.
- **Portable single exe**: Go + WebView2, no local HTTP port and no extra runtime.

## Privacy masking

- The name is reduced to “X同学” before it reaches the UI.
- Click the greeting name to cycle through `X同学 → 某同学 → 朋友 → X同学` with a character-scatter animation.
- Campus IP, MAC, and gateway values are replaced by a shielded “已加敏” chip. Click the chip or shield icon to temporarily reveal them.
- `Settings → 隐私脱敏` has global, name, and network-info switches; choices persist in `config.json`.

## Architecture

```
main.go ─┬─ internal/config   configuration
         ├─ internal/auth     online check / login / logout / connectivity probes
         ├─ internal/watcher  watch loop (adaptive pacing, intensive window, backoff)
         ├─ internal/state    runtime state (written by watcher, read by API)
         ├─ internal/api      in-process RPC + embedded frontend (go:embed)
         ├─ internal/logx     file log (rolling + throttled)
         └─ internal/winapp   tray + WebView2 window + message loop
```

The UI and daemon run in one process and communicate through the WebView2 message channel; the process listens on no port.

## Build

Requires Go 1.23+, MinGW gcc (CGO is used by the WebView2 binding), and windres.

```bash
# resources (icon / version info / manifest)
windres -i assets/app.rc -o rsrc_windows_amd64.syso -O coff

# build
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -H windowsgui" -o CampusAutoLogin.exe .
```

Put the generated `.syso` in the package directory; `go build` links it automatically.

## Portable layout

```
CampusAutoLogin.exe   application
config.json           account and settings (created on first run; contains plaintext password)
autologin.log         runtime log
```

Configuration is written next to the exe when possible; if that directory is read-only, it falls back to `%LOCALAPPDATA%\CampusAutoLogin`. WebView2 cache uses the same fallback location.

## Security notes

- Account credentials are only stored in the local `config.json`; login requests are only sent to the configured gateway.
- `config.json` contains a plaintext password. Do not commit or share it; the repository ignores it by default.
- Before first use, confirm the gateway IP matches the address shown in your campus authentication page.

## Known limitations

- Tray and window code is Windows-only; other platforms can build the background watcher only.
- The daily auto-login window does not cross midnight; the intensive window does and is capped at 1 hour.
- Moving the portable folder invalidates an existing autostart entry; toggle it again.
- The current implementation is WZUT-specific. Other Dr.COM-like gateways may need changes.

## License

MIT
