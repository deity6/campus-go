// 校园网自动认证工具（Go 版）
//
// 便携式布局：所有文件都在 exe 同级目录，拷走文件夹即可带走全部状态。
//
//	校园网认证.exe
//	config.json      账号与时段配置（首次运行自动生成）
//	autologin.log    运行日志
//
// 界面与守护在同一进程内，通过 WebView2 的消息通道通信，
// 不监听任何端口 —— 任务管理器里看不到它占用端口。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"campusautologin/internal/api"
	"campusautologin/internal/config"
	"campusautologin/internal/logx"
	"campusautologin/internal/state"
	"campusautologin/internal/watcher"
)

var (
	flagAutostart = flag.Bool("autostart", false, "以开机自启方式运行（不显示窗口）")
	flagDebug     = flag.Bool("debug", false, "开启开发者工具与右键菜单")
	flagVersion   = flag.Bool("version", false, "显示版本号后退出")
	flagHeadless  = flag.Bool("headless", false, "不创建窗口，仅后台守护")
)

// baseDir 返回「配置与日志应该放在哪」。
//
// 便携式优先用 exe 所在目录；若该目录不可写（U 盘只读、放在 Program Files），
// 退回 %LOCALAPPDATA%\CampusAutoLogin，避免程序无法保存配置。
func baseDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	dir := filepath.Dir(exe)

	test := filepath.Join(dir, ".write-test")
	if f, err := os.OpenFile(test, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Close()
		os.Remove(test)
		return dir
	}

	fallback := filepath.Join(os.Getenv("LOCALAPPDATA"), "CampusAutoLogin")
	_ = os.MkdirAll(fallback, 0o755)
	return fallback
}

func main() {
	flag.Parse()

	if *flagVersion {
		fmt.Printf("校园网自动认证 v%s (%s)\n", api.Version, api.Debug())
		return
	}

	dir := baseDir()

	logx.Init(filepath.Join(dir, "autologin.log"))
	logx.SetThrottleWindow("srcip", 30*time.Second)
	logx.Log(logx.Info, "===== 启动 v%s，目录 %s =====", api.Version, dir)

	// 单实例判定要尽早（放在建立任何状态之前）
	if handledByExisting() {
		return
	}

	os.Setenv("CAMPUS_CONFIG", filepath.Join(dir, "config.json"))

	cfg := config.Load()
	if _, err := os.Stat(filepath.Join(dir, "config.json")); os.IsNotExist(err) {
		if err := config.Save(cfg); err == nil {
			logx.Log(logx.Info, "已生成默认配置文件 config.json")
		}
	}

	store := state.New()

	var app *uiApp
	w := watcher.New(cfg, store, notifierFunc(func(title, body string) {
		if app != nil {
			app.notify(title, body)
		}
	}))

	svc := api.New(w, store)

	w.Start()
	store.SetNetwork("", cfg.InWatchWindow(time.Now().Hour()), cfg.Complete())

	if *flagHeadless {
		logx.Log(logx.Info, "无界面模式运行中")
		select {}
	}

	html, err := svc.RenderHTML()
	if err != nil {
		logx.Log(logx.Error, "生成界面失败: %v", err)
		fmt.Fprintf(os.Stderr, "生成界面失败: %v\n", err)
		os.Exit(1)
	}

	err = runUI(uiOptions{
		Title:       "校园网认证",
		HTML:        html,
		DataPath:    filepath.Join(os.Getenv("LOCALAPPDATA"), "CampusAutoLogin", "WebView2"),
		Debug:       *flagDebug,
		StartHidden: *flagAutostart,
		BaseDir:     dir,
		Watcher:     w,
		Store:       store,
		Service:     svc,
		onQuit: func() {
			w.Stop()
			logx.Log(logx.Info, "===== 已退出 =====")
			logx.Close()
		},
	})
	if err != nil {
		logx.Log(logx.Error, "界面启动失败: %v", err)
		fmt.Fprintf(os.Stderr, "界面启动失败: %v\n", err)
		os.Exit(1)
	}
}

// notifierFunc 让普通函数满足 watcher.Notifier 接口。
type notifierFunc func(title, body string)

func (f notifierFunc) Notify(title, body string) { f(title, body) }
