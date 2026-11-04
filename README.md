# dsh-tray

`dsh web` 的 Windows 托盘管理器。常驻通知区，用右键菜单控制 dsh web 的启动、重启、关闭与更新检查。

- 单文件 exe，不依赖 .NET / Node 运行时
- 图标反映三种状态：未运行（灰）/ 启动中（橙，两帧动画）/ 已就绪（绿）
- 启动完成的判定以 dsh 日志为准（出现 `dsh web: http...`）

## 目录结构

```
dsh-tray/
├── cmd/dsh-tray/          入口（main.go）+ 生成的版本资源 versioninfo.syso
├── internal/tray/         实现（托盘、状态机、进程控制、图标、配置）
│   └── assets/            角色序列帧（go:embed 打包进 exe）
├── resources/             app.manifest（Common Controls v6，walk 必需）
├── configs/               versioninfo.json（构建期版本信息）
├── bin/                   编译产物 dsh-tray.exe
├── build.ps1              一键构建
├── go.mod
└── go.sum
```

`versioninfo.syso` 是 `go build` 自动纳入的资源文件，**必须和 main.go 同目录**，
不能放进 `bin/`。

## 构建

```powershell
.\build.ps1                    # 版本不变，重新打包
.\build.ps1 -Version 1.3.0     # 加功能：升中间位
.\build.ps1 -Version 1.2.2     # 修 bug：升末位
```

脚本会自动同步两处版本号（`internal/tray/main.go` 与 `configs/versioninfo.json`）、
重新生成资源、编译到 `bin/dsh-tray.exe`。

依赖 `goversioninfo`（一次性安装）：
```powershell
go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
```

⚠️ `build.ps1` 必须保持 **纯 ASCII** —— Windows PowerShell 5.1 按 ANSI 解码 `.ps1`，
中文会乱码甚至截断字符串。

## 版本规则

| 改动性质 | 升级位 | 示例 |
|---|---|---|
| 加需求 / 新功能 | 中间位 | 1.2.0 → 1.3.0 |
| 修 bug | 末位 | 1.2.0 → 1.2.1 |

每次产出新包都要升版本。

## 运行时文件

| 内容 | 位置 |
|---|---|
| dsh web 日志 | `%LOCALAPPDATA%\dsh-tray\dsh-web.txt` |
| 配置（`autoOpenWeb`） | `%LOCALAPPDATA%\dsh-tray\config.json` |
| 生成的图标 / 自检报告 | `%LOCALAPPDATA%\dsh-tray\` |
| 开机自启 | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` → `DshTray` |

## 自检模式

不点界面也能验证，结果写到 `%LOCALAPPDATA%\dsh-tray\`：

| 参数 | 作用 |
|---|---|
| `--selftest` | 路径解析、当前 dsh 版本、npm 查更新、运行状态 |
| `--uitest` | 真实创建托盘与菜单，走一遍三态切换与动画，3 秒后退出 |
| `--starttest` | 在 3999 端口起临时实例，验证启动→就绪→停止，不碰 3080 |

```powershell
go test ./...    # 纯逻辑单测：日志标志、僵尸锁、失败提取、semver、双击判定
```
