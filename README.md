# dsh-tray

`dsh web` 的 Windows 托盘管理器。常驻通知区，用右键菜单控制 dsh web 的启动、重启、关闭与更新检查。

- 单文件 exe，不依赖 .NET / Node 运行时
- 图标反映三种状态：未运行（灰）/ 启动中（橙，两帧动画）/ 已就绪（绿）
- 启动完成的判定以 dsh 日志为准（出现 `dsh web: http...`）
- 菜单最末「版本 x.y.z」可点击，检查并自动更新本程序

## 菜单

| 项 | 说明 |
|---|---|
| 启动 / 重启 / 关闭 | 控制 dsh web |
| 打开web | 默认浏览器打开 `http://127.0.0.1:<port>`，仅"已就绪"可点 |
| 查看日志 | 用默认文本程序打开 dsh web 日志 |
| 检查dsh更新 | 查 npm 上有没有更高的 dsh 版本（只查不升） |
| 开机自启 / 启动后自动打开web | 可勾选配置 |
| 退出 | 只结束托盘程序，dsh 继续运行 |
| 完全退出 | 先关 dsh 服务再退出 |
| 版本 x.y.z | 点击检查本程序更新 |

双击托盘图标：未运行则启动，已就绪则打开 Web UI。

## 目录结构

```
dsh-tray/
├── cmd/dsh-tray/          入口（main.go）+ 生成的版本/图标资源 versioninfo.syso
├── internal/tray/         实现（托盘、状态机、进程控制、图标、配置）
│   └── assets/            角色序列帧（go:embed 打包进 exe）
├── tools/genicon/         由角色帧生成 resources/app.ico
├── resources/             app.manifest、app.ico（exe 图标）
├── configs/               versioninfo.json（构建期版本信息）
├── bin/                   编译产物 dsh-tray.exe
├── build.ps1              一键构建
├── go.mod
└── go.sum
```

`versioninfo.syso` 是 `go build` 自动纳入的资源文件，**必须和 main.go 同目录**，
不能放进 `bin/`。它把 **清单 + 版本信息 + 应用图标**打进同一个 `.rsrc` 段
（Go 链接器只允许一个），因此已在仓库中提交；改动清单/版本/图标后由 `build.ps1` 重新生成。

## 图标

- **exe 图标**：取 `assets/running.png` 那一帧（张嘴抬手的角色），不带状态角标 ——
  由 `tools/genicon` 合成多尺寸 `resources/app.ico`，再由 `goversioninfo -icon` 嵌入 exe。
- **托盘图标**：运行时按状态合成，右下角带颜色圆点（绿=运行中 / 橙=启动中 / 灰=未运行），
  启动中两帧交替形成动画。

## 构建

```powershell
.\build.ps1                    # 版本不变，重新打包
.\build.ps1 -Version 1.3.0     # 加功能：升中间位
.\build.ps1 -Version 1.2.2     # 修 bug：升末位
```

脚本会自动同步两处版本号（`internal/tray/main.go` 与 `configs/versioninfo.json`）、
生成 `resources/app.ico`、重新生成资源、编译到 `bin/dsh-tray.exe`。

⚠️ **不要只用 `go build`**：跳过资源生成会得到一个**没有清单**的 exe，
启动时 walk 直接崩在 `TTM_ADDTOOL failed`。务必用 `build.ps1`。

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

## 自动更新

点击菜单最后的「版本 x.y.z」：

1. 查 `https://api.github.com/repos/kirning/dsh-tray/releases/latest`
2. 与本地版本比较，有新版则询问是否更新
3. 下载（约 8~9 MB）→ 重命名自身 → 新文件就位 → 自动重启

dsh 服务是独立进程，更新托盘不会中断它。旧文件留作 `dsh-tray.old.exe`，下次启动自动清理。

两个实现要点：
- **下载走 API 资产端点**（`/releases/assets/<id>`），不用 `browser_download_url` ——
  后者在 `github.com` 域名下，部分网络（如国内直连）根本不通；前者会 302 到
  `release-assets.githubusercontent.com`，可直连。
- **不设 `http.Client.Timeout`** —— 它把整个响应体读取算进去，慢网络下必然断在几 MB。
  改为只限制拨号/TLS/响应头，下载整体上限 15 分钟。

> 若所在网络把 `api.github.com` 也墙了，可设置 `HTTPS_PROXY` 环境变量后重试。

## 找不到 dsh 时

程序按以下顺序查找 `dsh`：

1. `%ProgramFiles%\nodejs`、`C:\Program Files\nodejs`
2. `%APPDATA%\npm`（npm 按用户安装前缀）
3. `PATH` 里的 `dsh` 启动器 → 解析 npm shim 里的 bin.js 路径反推
4. `PATH` 里 `node` 的安装目录

都找不到会弹窗提示把 dsh 加入 `Path`（含具体操作步骤）。

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
| `--checkupdate` | 查 GitHub release 并**真实下载一次** exe 核对字节数（验证更新链路） |

```powershell
go test ./...    # 纯逻辑单测：日志标志、僵尸锁、失败提取、semver、双击判定
```
