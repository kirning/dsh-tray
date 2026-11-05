# dsh-tray

`dsh web` 的 Windows 托盘管理器。常驻通知区，用右键菜单控制 dsh web 的启动、重启、关闭与更新检查。

- 单文件 exe，不依赖 .NET / Node 运行时
- 图标反映三种状态：未运行（灰）/ 启动中（橙，两帧动画）/ 已就绪（绿）
- 启动完成的判定以 dsh 日志为准（出现 `dsh web: http...`）
- 菜单最末「版本 x.y.z」可点击，检查并自动更新本程序
- 「设置」窗口可配代理、查看/复制日志、带进度条地升级本程序
- 本程序产生的日志统一带 `[dsh-tray]` 前缀，每次启动 dsh 只保留本次内容

## 菜单

| 项 | 说明 |
|---|---|
| 启动 / 重启 / 关闭 | 控制 dsh web |
| 打开web | 默认浏览器打开 `http://127.0.0.1:<port>`，仅"已就绪"可点 |
| 查看日志 | 用默认文本程序打开 dsh web 日志 |
| 检查dsh更新 | 查 npm 上有没有更高的 dsh 版本（只查不升） |
| 开机自启 / 启动后自动打开web | 可勾选配置（与「设置」窗口双向同步） |
| 设置… | 代理配置、启动配置同步、版本+检查更新、只读日志查看 |
| 退出 | 只结束托盘程序，dsh 继续运行 |
| 完全退出 | 先关 dsh 服务再退出 |
| 版本 x.y.z | 点击检查本程序更新 |

双击托盘图标：未运行则启动，已就绪则打开 Web UI。

## 设置窗口

右键 →「设置…」（非模态，不阻塞托盘）：

| 分区 | 内容 |
|---|---|
| 代理服务器 | 填 `IP:端口`（或 `IP 端口`、中文冒号、`socks5://` / `http://` 前缀）。点「检测并保存」后**优先做 SOCKS5 握手**，不通则自动回退 HTTP；留空或点「清空」= 直连 |
| 启动配置 | 「开机自启」「启动后自动打开web」两份勾选，与右键菜单实时同步；`config.json` 里另存一份 `autoStart` 副本 |
| 版本与更新 | 显示当前版本，旁边「检查更新」按钮；检查→下载→替换→重启的进度走进度条，错误直接显示在状态框（只读、可复制）。下面一行是**可选的 GitHub Token**（见下） |
| 日志 | 只读 TextEdit 显示 `dsh-web.txt`，可选中复制 / 一键复制全部 / 刷新 / 用默认程序打开 |

代理同时用于：GitHub Release 检查与下载（Go 的 `http.Transport`）、`npm view` 查 dsh 版本（注入 `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY` 给子进程）。
Go 标准库原生支持 `socks5://` 代理 URL，无需额外依赖。运行时若 SOCKS5 突然不可用，会自动改记为 HTTP 并重试。

### GitHub Token（可选）

`api.github.com` 的**匿名**配额只有 60 次/小时，而且**按出口 IP 算** —— 公司网、共享代理的出口
经常被别人用光，表现就是"代理握手成功，但检查更新报 HTTP 403 rate limit exceeded"。
填一个令牌可以提到 **5000 次/小时**。取值顺序：

1. 「设置」里手填（只存在 `config.json`，界面上以密码方式显示）
2. 环境变量 `GITHUB_TOKEN` / `GH_TOKEN`
3. **`gh auth token`** —— 装了 GitHub CLI 并登录过就自动复用，无需手填

填完点「保存」，程序会打一次 `/rate_limit`（该端点不占配额）验证令牌并回显当前限额。
令牌只用于查/下载本程序的 Release，不需要任何 scope（公开仓库只读即可）。

## 日志规则

- 只有**本程序**写的日志带 `[dsh-tray] 时间戳 ...` 前缀；dsh 自己的 stdout/stderr 原样保留
- 每次点「启动 / 重启」dsh 时**清空日志**，文件里只有本次启动的内容（托盘程序自身启动/退出不清空）
- 启动错误（找不到 dsh、僵尸锁清理、进程创建失败、`startup failed` 摘录）、检查 dsh 更新、应用自更新的每一步都写进同一个 `dsh-web.txt`

> ⚠️ 实现坑：Windows 下 Go 的 `O_TRUNC|O_APPEND` 组合会保留 `GENERIC_WRITE`，
> 写操作按文件指针走而非追加，两个句柄会互相覆盖。所以截断用单独句柄做，
> 再以**纯 `O_APPEND`** 重新打开给 dsh 子进程继承。

## 目录结构

```
dsh-tray/
├── cmd/dsh-tray/          入口（main.go）+ 生成的版本/图标资源 versioninfo.syso
├── internal/tray/         实现（托盘、状态机、进程控制、图标、配置、设置窗口、代理、日志）
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
  改为只限制拨号/TLS/响应头，下载整体上限 30 分钟；断开后用 `Range` **断点续传**，最多 8 轮。
- 「设置」窗口里的「检查更新」会把下载百分比画到进度条，出错直接显示在状态框并写入日志。

> 若所在网络把 `api.github.com` 也墙了，在「设置」里填代理地址（SOCKS5 优先，不通自动回退 HTTP），
> 或设 `HTTPS_PROXY` 环境变量后重试。
>
> **反过来也成立**：代理能连上不代表能查。GitHub 匿名 API 配额是 60 次/小时、**按出口 IP 算**，
> 代理出口常被别人用光，会回 `HTTP 403 rate limit exceeded`（日志里能看到是哪个 IP）。
> 本程序会按「配置的代理 → 同端口 HTTP 代理 → 直连」依次尝试，谁通用谁，
> 并在日志里写明最终走了哪条（例如 `查询 release 成功（直连，版本 v1.5.1）`）。
> 三条全失败时，报错会把每条链路的原因和配额恢复时间一起列出来。
> 若三条都在限流（公司出口和代理出口都是共享 IP 时很常见），根治办法是带上 GitHub Token
> 把配额提到 5000 次/小时 —— 见下面的「GitHub Token」小节，装了 gh 并登录过会自动复用。

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
| dsh web 日志（含本程序日志） | `%LOCALAPPDATA%\dsh-tray\dsh-web.txt` |
| 配置 | `%LOCALAPPDATA%\dsh-tray\config.json`：`autoOpenWeb` / `proxy` / `proxyScheme` / `autoStart`（副本） |
| 生成的图标 / 自检报告 | `%LOCALAPPDATA%\dsh-tray\` |
| 开机自启（权威） | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` → `DshTray` |

## 自检模式

不点界面也能验证，结果写到 `%LOCALAPPDATA%\dsh-tray\`：

| 参数 | 作用 |
|---|---|
| `--selftest` | 路径解析、当前 dsh 版本、npm 查更新、运行状态 |
| `--uitest` | 真实创建托盘与菜单，走一遍三态切换与动画，3 秒后退出 |
| `--settingstest` | 真实创建「设置」窗口，核对控件、配置联动、进度与状态刷新，3 秒后退出 |
| `--shot` | 设置窗口两个尺寸的截图 + 每个控件的实际矩形（核对分区对齐），并验证关闭后宿主窗口没被显出来 |
| `--starttest` | 在 3999 端口起临时实例，验证启动→就绪→停止，不碰 3080 |
| `--checkupdate` | 查 GitHub release 并**真实下载一次** exe 核对字节数（验证更新链路） |

```powershell
go test ./...    # 纯逻辑单测：日志标志、僵尸锁、失败提取、semver、双击判定
```
