//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"campusautologin/internal/api"
	"campusautologin/internal/logx"
	"campusautologin/internal/state"
	"campusautologin/internal/watcher"
	"campusautologin/internal/winapp"
)

type uiOptions struct {
	Title       string
	HTML        string
	DataPath    string
	Debug       bool
	StartHidden bool
	BaseDir     string
	Watcher     *watcher.Watcher
	Store       *state.Store
	Service     *api.Service
	onQuit      func()
}

type uiApp struct {
	app *winapp.App
}

func (u *uiApp) notify(title, body string) {
	if u.app != nil {
		u.app.Notify(title, body)
	}
}

const instanceMutex = "Local\\CampusAutoLogin_SingleInstance"

// handledByExisting 在进程最早期做单实例判定。
//
// 必须早于初始化业务状态：否则第二个实例会先建好自己的资源再退出，
// 白白折腾一遍；早期版本还会覆盖 runtime.json 把在跑的实例弄坏。
func handledByExisting() bool {
	if _, already := winapp.SingleInstance(instanceMutex); !already {
		return false
	}
	for i := 0; i < 25; i++ {
		if winapp.NotifyExisting() {
			logx.Log(logx.Info, "检测到已有实例，已唤起其窗口")
			return true
		}
		time.Sleep(120 * time.Millisecond)
	}
	logx.Log(logx.Warn, "互斥量存在但找不到窗口，按新实例继续启动")
	return false
}

func runUI(o uiOptions) error {
	u := &uiApp{}

	// 界面里「打开自助服务」这类动作交给系统浏览器
	api.SetOpener(winapp.ShellOpen)

	var app *winapp.App

	app = winapp.New(winapp.Options{
		Title:       o.Title,
		HTML:        o.HTML,
		OnMessage:   handleRPC(o.Service),
		Width:       1040,
		Height:      720,
		MinWidth:    880,
		MinHeight:   580,
		DataPath:    o.DataPath,
		Debug:       o.Debug,
		StartHidden: o.StartHidden,
		Actions: winapp.Actions{
			OnCheck: func() {
				online, detail := o.Watcher.Check()
				msg := "当前离线：" + detail
				if online {
					msg = "当前在线：" + detail
				}
				app.Notify("检测结果", msg)
			},
			OnLogin: func() {
				res := o.Watcher.Login()
				if res.OK {
					app.Notify("认证成功", res.Detail)
				} else {
					app.Notify("认证失败", res.Detail)
				}
			},
			OnOpenLogs: func() {
				openWithDefault(filepath.Join(o.BaseDir, "autologin.log"))
			},
			OnOpenFolder: func() {
				openWithDefault(o.BaseDir)
			},
			OnToggleAutostart: func() bool {
				on, _, _ := api.AutostartGet()
				if err := api.AutostartSet(!on); err != nil {
					logx.Log(logx.Error, "切换开机自启失败: %v", err)
					return on
				}
				return !on
			},
			IsAutostartOn: func() bool {
				on, _, _ := api.AutostartGet()
				return on
			},
			OnQuit: o.onQuit,
		},
	})

	u.app = app

	go syncTray(app, o.Store)

	return app.Run()
}

// handleRPC 把页面发来的消息转成服务调用，并返回要执行的 JS。
//
// 这条链路全程在进程内，不经过网络栈 —— 没有端口、没有 token，
// 也没有任何能被其他程序访问的接口。
func handleRPC(svc *api.Service) func(string) string {
	return func(msg string) string {
		id, method, params, err := api.ParseRequest(msg)
		if err != nil {
			return ""
		}
		payload, callErr := svc.Call(method, params)
		return api.EncodeResponse(id, payload, callErr)
	}
}

// syncTray 把内部状态映射成托盘图标颜色与提示文字。
func syncTray(app *winapp.App, store *state.Store) {
	last := ""
	for {
		rt, nv := store.Snapshot()

		var st winapp.Status
		var label string
		switch rt.Phase {
		case state.PhaseOnline:
			st, label = winapp.StatusOnline, "已连接"
		case state.PhaseAuthing:
			st, label = winapp.StatusWorking, "认证中"
		case state.PhaseChecking:
			st, label = winapp.StatusWorking, "检测中"
		case state.PhaseOffline:
			st, label = winapp.StatusOffline, "未连接"
		case state.PhaseSuspended:
			st, label = winapp.StatusSuspend, "时段外 · 挂起"
		default:
			st, label = winapp.StatusSuspend, "准备中"
		}

		key := fmt.Sprintf("%s|%s|%v", rt.Phase, rt.Detail, nv.Watching)
		if key != last {
			tip := "校园网认证 · " + label
			if rt.Detail != "" {
				tip += " · " + rt.Detail
			}
			app.SetStatus(st, tip)
			last = key
		}
		time.Sleep(1500 * time.Millisecond)
	}
}

// openWithDefault 用系统默认程序打开文件或目录。
func openWithDefault(path string) {
	if _, err := os.Stat(path); err != nil {
		logx.Log(logx.Warn, "要打开的目标不存在: %s", path)
		return
	}
	if err := winapp.ShellOpen(path); err != nil {
		logx.Log(logx.Warn, "打开 %s 失败: %v", path, err)
	}
}
