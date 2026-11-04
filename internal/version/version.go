// Package version 是整个应用唯一的版本号来源。
//
// 版本号不要散落在 api.go、app.rc、使用说明里手写；
// 日常改版用 scripts/bump_version.py 递增 patch，发布用 scripts/release.py。
package version

import (
	_ "embed"
	"strings"
)

//go:embed version.txt
var raw string

// Number 返回语义化版本号，例如 "4.3.2"。
func Number() string {
	return strings.TrimSpace(raw)
}
