# AGENTS.md — dsh-tray-wails（Wails3 跨平台托盘）

> 旧版在 `../dsh-tray/`（Go + walk，仅 Windows，1.7.0），本目录是 2.x 重写版，Windows + Linux 同一套代码。
> **在本目录改代码先看本文件**；面向使用者的说明见 `README.md`；项目总导航见 `../../AGENTS.md` §2.6。

---

## 1. 分层（新增代码按这个归位）

| 目录/文件 | 职责 | 平台相关 |
|---|---|---|
| `main.go` | 唯一装配入口：单实例 → Wails 应用 → 隐藏设置窗口 → 启动托盘 | 否 |
| `internal/app/` | 托盘菜单（`tray.go`）、前端 API（`service.go`）、窗口（`window.go`）、退出（`quit.go`） | 否 |
| `internal/core/` | 业务：配置、日志、dsh 定位/启停/就绪、版本、代理、自更新、升级、图标合成 | 否 |
| `internal/platform/` | 系统调用，`sys_windows.go` / `sys_linux.go` 各一套（build tag 分平台） | 是 |
| `frontend/` | React + TS + antd 设置页 | 否 |

**根目录只放 `main.go` 一个 Go 文件**，不要再把服务/托盘摊到根目录。

---

## 2. 托盘图标颜色（硬约束，别忘）

颜色唯一来源：`internal/core/icon.go` 的 `var ( badge* )`。**改颜色只改这里**，
禁止在 `tray.go`、前端或别处另写色值。

| 状态 | 常量 | 角标色 | 素材 |
|---|---|---|---|
| 未运行 | `StateStopped` | **灰 `#8C8C8C`** | `stopped.png` |
| 启动中 | `StateStarting` | **橙 `#F09D2E`** | `starting-a.png` / `starting-b.png` 两帧 |
| 已就绪 | `StateRunning` | **绿 `#2EA043`** | `running.png` |
| 升级中 | `StateUpgrading` | **紫 `#7F77DD`** | `starting-a.png` / `starting-b.png` 两帧 |

画法（`compose()`）：

- 角标在**右下角**，半径 = 画布尺寸 × 0.15（最小 2px），外圈套一层白色描边（尺寸 × 0.025），
  保证在深色角色/浅色任务栏上都看得清
- 托盘图标输出 **64×64 PNG**（`TrayIcon`）；exe 图标是 512 PNG（`WriteAppIcon` → `build/appicon.png`
  → `wails3 generate icons` 生成 `build/windows/icon.ico`）
- 两帧动画间隔 **420ms**，只对 `StateStarting` / `StateUpgrading` 生效（`tray.go` 的 `animate()`）

**新增/修改状态时必须同步四处**，漏一处就会出现"图标不对但编译通过"：

1. `internal/core/icon.go`：状态常量 + `badge` 色值 + `TrayIcon` 的 switch
2. `internal/app/tray.go`：`stateLabel()` 文案、`applyIcon()` 的升级覆盖逻辑
3. `README.md`：状态表
4. 本文件第 2 节的颜色表

语义约定（别改错）：

- 绿 = dsh 已就绪（端口在监听且可用）
- 橙 = 本次启动还没就绪
- 紫 = 正在执行 `npm/mise/volta` 升级，**不要**复用橙色（用户明确要求与启动区分）
- 灰 = 没有 dsh 在跑

---

## 3. 已踩过的 Wails 坑（改 UI/退出前务必看）

| 现象 | 根因 | 正确做法 |
|---|---|---|
| 点「退出」无效，dsh 停了进程还在 | `windowsApp.destroy()` **首行** `if !shouldQuit() { return }`，而菜单直接调 `wailsApp.Quit()` 没置位 | 退出统一走 `app.Quit()`（先 `quitting.Store(true)` 再调注入的 Quit） |
| 点设置窗口 X，整个程序退出 | `unregisterWindow` 发现窗口数为 0 就 `PostQuitMessage`，条件是 `!Windows.DisableQuitOnLastWindowClosed`，**绕过 ShouldQuit** | 置 `DisableQuitOnLastWindowClosed: true`，且 `ShowSettings` 要能重建被销毁的窗口 |
| 带自定义按钮的弹框只剩一个「确定」 | `Info()` 走 MessageBox，不支持自定义按钮文案；只有 `Question` + 按钮才走 TaskDialog | 用 `app.Dialog.Question()`，再 `AddButton(...).OnClick(...)` |
| dsh 已在跑，托盘一直橙色 | `SetReadyBaseline` 把扫描起点设在日志末尾，外部实例的就绪行在更早位置，永远扫不到，只能等端口兜底 | 端口已监听时直接判定就绪；兜底 3s |

---

## 4. 构建与自检

```bash
wails3 build                  # 产物 bin/dsh-tray（Windows 为 bin/dsh-tray.exe）
wails3 dev                    # 开发模式
go test ./internal/...
```

托盘菜单无法自动化点击，所以内置了三个自检参数（结果都写 `dsh-web.txt`）：

| 参数 | 作用 | 期望日志 |
|---|---|---|
| `--version` | 打印版本 | `2.x.y` |
| `--quittest` | 3 秒后主动退出 | `--quittest：主动退出` → `程序退出`，进程消失 |
| `--statustest` | 每 2 秒打印三态内部状态（含 `readyOffset`/`logSize`/`portSince`） | 状态应尽快变 `running` |
| `--windowtest` | 关闭设置窗口 → 验证进程存活 → 重建窗口 | `关闭 3 秒后进程仍存活` → `窗口重建成功` |

Linux 构建依赖 **GTK4 + WebKitGTK 6.0**（不是 GTK3 + WebKit 4.1）：

```bash
sudo pacman -S --needed base-devel go nodejs npm pkgconf gtk4 webkitgtk-6.0 libayatana-appindicator
```

---

## 5. 版本号

当前 **2.2.1**。三处必须同步，否则界面显示与文件属性不一致：

| 位置 | 作用 |
|---|---|
| `internal/core/selfupdate.go` `var AppVersion` | 运行时显示与自更新比较（权威） |
| `build/config.yml` `info.version` | exe 文件属性 |
| `Taskfile.yml` `vars.VERSION` | 构建脚本变量 |

升位规则：新功能升 minor，修 bug 升 patch，只改文档不升。
推 `v*` 标签时 CI 会用标签值覆盖前两处并发布 Release。
