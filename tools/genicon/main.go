// Command genicon 用内置角色帧生成构建期应用图标。
// 用法：go run ./tools/genicon [输出路径，默认 build/appicon.png]
package main

import (
	"fmt"
	"os"

	"github.com/kirning/dsh-tray/internal/core"
)

func main() {
	path := "build/appicon.png"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	if err := core.WriteAppIcon(path, 512); err != nil {
		fmt.Fprintln(os.Stderr, "生成图标失败:", err)
		os.Exit(1)
	}
	fmt.Println("已生成", path)
}
