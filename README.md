# dsh-tray（Wails3 跨平台版）

`dsh web` 的托盘管理器，**同一套代码同时支持 Windows 与 Linux**。

这是原 `app/dsh-tray`（Go + `lxn/walk`，仅 Windows）的重写版：UI 层换成 Wails3
（原生托盘 + WebView 设置窗口），系统调用抽成 `internal/platform` 的两套实现，
业务逻辑（dsh 定位、启停、就绪判定、日志、更新）保持平台无关。

> 旧版仍在 `../dsh-tray`，未改动，可随时回退对照。

## 功能

- 托盘三态：未运行（灰）/ 启动中（橙，两帧动画）/ 已就绪（绿）
- 右键菜单：启动、重启、打开 web、查看日志、关闭、检查 dsh 更新、开机自启、
  启动后自动打开 web、设置、退出、完全退出、检查本程序更新
- 设置窗口（WebView）：dsh 路径（手填/自动探测/保存即验证）、代理（SOCKS5/HTTP 探测）、
  启动配置、版本与更新（进度条）、日志查看与复制
- 自更新：读 GitHub Release，按平台选择资产，Range 断点续传
- 单实例、僵尸凭证锁清理、就绪日志判定 + 端口 10 秒兜底

## 目录结构

```
dsh-tray-wails/
├── main.go                 唯一的装配入口：单实例、Wails 应用、隐藏窗口、启动托盘
├── internal/
│   ├── app/                界面与服务层：托盘菜单、Service（前端 API）、窗口管理
│   ├── core/               平台无关业务：配置、日志、dsh 定位/启停、版本、代理、自更新、图标
│   └── platform/           系统调用，按 build tag 分平台实现
│       ├── platform.go         通用工具（%VAR% 展开、路径规范化）
│       ├── sys_windows.go      注册表自启、Mutex、ShellExecute、netstat、隐藏窗口、exe 替换
│       └── sys_linux.go        XDG 自启、flock、xdg-open、ss/lsof、进程组、rename 替换
├── frontend/               React + TS + antd 设置界面
├── build/                  Wails 构建配置与图标（只保留 windows / linux）
└── tools/genicon/          由角色帧生成 build/appicon.png
```

## 构建依赖

公共：

- Go **1.26+**
- Node **18+**（前端构建）
- Wails3 CLI **v3.1.0**：`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`

### Windows

- **WebView2 Runtime**（Win10/11 通常已预装，缺失时程序起不来）
- MinGW-w64 gcc（本机实测 15.2.0；Wails 构建链路会用到）

### Linux（CachyOS / Arch）

```bash
sudo pacman -S --needed base-devel go nodejs npm pkgconf \
  gtk4 webkitgtk-6.0 libayatana-appindicator
```

- **Wails v3.1.0 的 Linux 后端默认是 GTK4 + WebKitGTK 6.0**（不是 GTK3 + WebKit 4.1）。
  装错版本时 `go install wails3` 就会报 `Package gtk4 was not found` / `webkitgtk-6.0 not found`
- `libayatana-appindicator` 提供托盘图标（GNOME 需装 AppIndicator 扩展才能看到）

Debian/Ubuntu 对应包名：`pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libayatana-appindicator3-dev`。

## 构建与运行

```bash
wails3 build          # 当前平台，产物 bin/dsh-tray（Windows 为 bin/dsh-tray.exe）
wails3 dev            # 开发模式（前端热更新）
go test ./...         # 纯逻辑测试
```

Windows 构建会自动生成 `.syso`（清单 + 版本 + 图标）并用 `-H windowsgui` 去掉控制台。
Linux 构建产物是动态链接的可执行文件，**运行时依赖上面那几个系统库**。

### 发布

三种方式，都会先跑 `go test ./internal/...` 再构建：

**1. 打标签（正式发布）**

```bash
git tag v2.0.0 && git push origin v2.0.0
```

CI 会：构建 Windows + Linux → 创建 Release `v2.0.0` → 上传
`dsh-tray.exe`、`dsh-tray-linux-amd64`、`checksums.txt`。
标签里的版本号会被写进 `internal/core/selfupdate.go` 与 `build/config.yml`，
所以包内 `--version` 与 Release 标签始终一致（自更新不会反复提示同一版本）。

**2. 手动触发（草稿，适合先验证）**

Actions → build → Run workflow → 填 `version`（如 `2.1.0`），`draft` 保持 `true`。
产物进草稿 Release，**不影响 latest**；网页上确认无误后再点 Publish。

**3. 只构建不发布**

推 `main` / `wails-v2`，或手动触发时把 `version` 留空。

> `workflow_dispatch` 只能从默认分支上存在的 workflow 触发；当前 workflow 位于
> `wails-v2` 分支，所以手动触发需要先用标签跑通（或把 workflow 合并到 main）。

### 版本号

当前 **2.0.0**，存在三处，改版本时必须同步：

| 位置 | 作用 |
|---|---|
| `internal/core/selfupdate.go` 的 `var AppVersion = "2.0.0"` | 运行时显示与更新比较（权威） |
| `build/config.yml` 的 `info.version` | exe 文件属性（FileVersion / ProductVersion） |
| `Taskfile.yml` 的 `vars.VERSION` | 构建脚本里的版本变量 |

快速确认：`./bin/dsh-tray.exe --version` 或 `task version`。

> `build/darwin/icon.icns` 是 Wails 图标工具链的固定输出路径，即使不构建 macOS 也要保留，
> 否则 `wails3 build` 会在 `generate:icons` 步骤失败。

## 运行期文件

| 内容 | Windows | Linux |
|---|---|---|
| 配置 | `%LOCALAPPDATA%\dsh-tray\config.json` | `~/.config/dsh-tray/config.json` |
| 日志 | `%LOCALAPPDATA%\dsh-tray\dsh-web.txt` | `~/.config/dsh-tray/dsh-web.txt` |
| 开机自启 | `HKCU\...\Run` → `DshTray` | `~/.config/autostart/dsh-tray.desktop` |
| 单实例 | 命名互斥体 | `instance.lock` + flock |

配置字段：`autoOpenWeb` / `proxy` / `proxyScheme` / `autoStart`（副本）/ `githubToken` / `dshPath`。

## 找到 dsh 的顺序

1. 设置里手填的路径（`dshPath`）
2. 环境变量 `DSH_TRAY_DSH`
3. 常见 node 安装前缀（npm 全局、nvm / fnm / mise / volta）
4. PATH 与版本管理器的 shim 目录（`mise/shims`、`volta/bin`、`~/.local/bin` …）
5. PATH 里的 `node` 所在目录

mise / volta 装出来的命令是转发器，冷启动时 PATH 里没有 shims 目录，
所以“手填一次路径”是最省事的解法；保存时会立刻执行 `dsh --version` 验证。

## 已知限制

- Linux 托盘依赖 AppIndicator，部分桌面环境（如默认 GNOME）需要扩展才有图标
- Windows 依赖 WebView2 Runtime
- 自更新需要 Release 里同时存在 `dsh-tray.exe` 与 `dsh-tray-linux-amd64` 两个资产
- 新版单实例互斥体名带 `-wails` 后缀，可与旧版并存；正式替换旧版后可改回
