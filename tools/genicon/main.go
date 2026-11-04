// Command genicon 生成 exe 自带的应用程序图标（resources/app.ico）。
//
// 由 build.ps1 在编译前调用；也可以单独跑：
//
//	go run ./tools/genicon resources/app.ico
//
// 图标取自 internal/tray/assets 里的角色帧，与托盘图标同源但**不带状态角标**
// （exe 图标是静态的，状态点只在托盘运行时才有意义）。
package main

import (
	"fmt"
	"os"

	"dshtray/internal/tray"
)

func main() {
	out := "resources/app.ico"
	if len(os.Args) > 1 && os.Args[1] != "" {
		out = os.Args[1]
	}
	if err := tray.WriteAppICO(out); err != nil {
		fmt.Fprintf(os.Stderr, "genicon: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("wrote " + out)
}
