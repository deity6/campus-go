// genicon 生成 exe 资源用的多尺寸图标（assets/app.ico）。
//
// 图标本身由 internal/winapp 用代码绘制（作者没有美术素材，
// 用代码画能保证各尺寸都清晰、且换配色只需改一处）。
//
// 用法：go run ./cmd/genicon
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"campusautologin/internal/winapp"
)

func main() {
	out := "assets/app.ico"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	data := winapp.IconsForApp()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s（%d 字节）\n", out, len(data))
}
