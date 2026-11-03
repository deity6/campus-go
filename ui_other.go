//go:build !windows

package main

import (
	"fmt"

	"campusautologin/internal/api"
	"campusautologin/internal/state"
	"campusautologin/internal/watcher"
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

type uiApp struct{}

func (u *uiApp) notify(title, body string) {}

// handledByExisting 在非 Windows 平台不做单实例限制。
func handledByExisting() bool { return false }

// 非 Windows 平台先只跑守护进程。
func runUI(o uiOptions) error {
	fmt.Printf("校园网认证守护已启动（当前平台无原生界面）\n")
	select {}
}
