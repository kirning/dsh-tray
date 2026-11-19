# AGENTS.md — dsh-tray（dsh web 托盘管理器）

> **本文件面向在 `app/dsh-tray/` 下工作的 AI 代理与协作者**，只写「规范 + 硬约束 + 踩坑索引」。
>
> 分工（别写重复内容）：
> - `README.md` —— 面向使用者：菜单、设置窗口、怎么用
> - **本文件** —— 面向改代码的人：能改什么、不能改什么、改完怎么验、踩过的坑
> - `../../AGENTS.md` —— 上位项目地图、分类体系、**本机通用环境坑**（§4.6 Go 托盘、§4.3 Node/dsh、§5 工作纪律）
>
> 本文件是 dsh-tray 的**权威规范**。新踩的坑写这里；上位文件只留导航和跨项目通用坑。

---

## 0. 定位与技术边界

Windows 通知区常驻小工具，用右键菜单管 `dsh web` 的启停、重启、日志、版本检查。

| 项 | 值 |
|---|---|
| 语言 / GUI | Go 1.26.2 + `github.com/lxn/walk`（纯 Go，无 cgo），`-H windowsgui` 无控制台 |
| 产物 | 单文件 `bin/dsh-tray.exe`（约 8~11 MB，随依赖变化），**不依赖 .NET / Node 运行时** |
| 仓库 | https://github.com/kirning/dsh-tray ，`main` 分支，Apache-2.0 |
| 被管对象 | `dsh web` CLI，固定端口 **127.0.0.1:3080** |
| 运行时数据 | `%LOCALAPPDATA%\dsh-tray\` |
| 当前版本 | **1.7.0** |

⚠️ **推 GitHub 前先确认代理还在不在**。仓库 `.git/config` 写死了
`http.proxy` / `https.proxy = socks5://127.0.0.1:10808`（历史上本机直连 `github.com:443` 不通才加的）。
**2026-11-19 实测直连已可用**（curl 返回 200、`git push` 成功），且当天 10808 端口并未监听。
代理没开时 `git push` 会报 `Failed to connect to github.com:443 over proxy 127.0.0.1`，
绕过办法：`git -c http.proxy= -c https.proxy= push origin main`。
`github.com:22` 通但本机**没有 SSH key**，只能走 HTTPS。

---

## 1. 目录结构（已与磁盘核对）

```
app/dsh-tray/
├── AGENTS.md                  ← 本文件
├── README.md / LICENSE / .gitignore / go.mod / go.sum / build.ps1
├── cmd/dsh-tray/              main.go（唯一入口）+ versioninfo.syso（生成物，已提交）
├── internal/tray/             实现（package tray）
│   ├── assets/                角色序列帧 PNG + source-icon.webp（用户素材，勿删）
│   ├── main.go               托盘图标、菜单、三态状态机、1s 轮询、双击、单实例
│   ├── dsh.go                dsh 定位、启停、就绪判定、僵尸锁清理
│   ├── settings.go           非模态设置窗口
│   ├── selfupdate.go         本程序自更新（GitHub Release）
│   ├── update.go             semver 比较 + `npm view` 查 dsh 更新
│   ├── proxy.go / config.go / autostart.go / applog.go / icon.go / util.go
│   ├── selftest.go / uitest.go / settingstest.go / starttest.go / shot.go   隐藏自检模式
│   └── dsh_test.go / settings_test.go                                      单元测试
├── tools/genicon/             由角色帧生成 resources/app.ico
├── resources/                 app.manifest（Common Controls v6）、app.ico
├── configs/                   versioninfo.json（版本信息 + 清单输入）
├── .github/workflows/build.yml CI：test → build → artifact；推 `v*` 自动发 Release
└── bin/                       编译产物（被 .gitignore 忽略，仓库里看不到是正常的）
```

两个位置是硬性的，别"整理"：

- **`versioninfo.syso` 必须和 `main.go` 同目录**（`cmd/dsh-tray/`）—— Go 只把当前包目录下的 `.syso` 编进去，
  放 `bin/` 或根目录都不生效。
- **`assets/` 必须与 `internal/tray` 同目录** —— `icon.go` 里的 `//go:embed assets/*.png` 是相对包目录解析的。

---

## 2. 硬约束清单（改代码前先扫一遍）

| # | 约束 | 违反后的症状 |
|---|---|---|
| 1 | **只用 `build.ps1` 打包**，不要裸 `go build` | 跳过资源生成 → exe 没有清单 → walk 在 `NewMainWindow` 报 `TTM_ADDTOOL failed` 直接崩 |
| 2 | `build.ps1` 与其他 `.ps1` **保持纯 ASCII** | Windows PowerShell 5.1 按 ANSI(GBK) 解码，中文会弄断字符串引号，脚本解析失败**且零输出** |
| 3 | Go 链接器**只接受一个 `.rsrc` 段** | 同时用 `rsrc` 生成 manifest.syso 和 `versioninfo.syso` → `too many .rsrc sections`。只用 `goversioninfo -manifest -icon` 一次带上清单+版本+图标（`rsrc` 路线已废弃） |
| 4 | `//go:embed` 的 `//` 后**不能有空格** | `// go:embed` 指令静默失效、编译不报错，运行时 `embed.FS.ReadFile` 才炸 `file does not exist` |
| 5 | 裸 `walk.NewMainWindow()` 必须 `SetLayout(walk.NewHBoxLayout())` | 弹设置窗口时主窗口收到 `WM_WINDOWPOSCHANGED + SWP_SHOWWINDOW`，walk 去布局 → `ContainerBase.CreateLayoutItem` 空指针崩溃（**不是设置窗口自己的问题**） |
| 6 | 关掉设置窗口后**必须把宿主窗口再藏回去** | walk 的 `FormBase.WndProc` 处理 `WM_CLOSE` 会给 owner 发 `SetWindowPos(..., SWP_SHOWWINDOW)` → 多出一个空白窗体；用户关掉它 → `mw.Run()` 返回 → **整个托盘连图标一起消失** |
| 7 | 日志**禁止 `O_TRUNC\|O_APPEND` 组合** | Windows 下 Go 的 `syscall.Open` 会保留 `GENERIC_WRITE`，写按文件指针走，dsh 子进程与 `trayLogf` 两个句柄互相覆盖。必须「先单独截断，再以纯 `O_APPEND` 重开」 |
| 8 | 自更新**不给 `http.Client` 设整体 `Timeout`** | 它把响应体读取也算进去，大 exe 在慢网络下必断（实测 59s 断在 2.9 MB）。只限制拨号/TLS/响应头 |
| 9 | 下载 Release 用 **API 资产端点** `/releases/assets/<id>` | `browser_download_url` 在 `github.com` 域名下，本机直连不通；API 端点 302 到 `release-assets.githubusercontent.com`，可直连 |
| 10 | 重启前**先释放单实例互斥体** `CloseHandle(singleMutex)` | 新进程启动即判定"已有实例"自行退出 → 用户托盘没了 |
| 11 | `Run()` 第一行就要 `loadConfig()` | 隐藏自检模式原来不读配置，自检结果和用户实际行为不一致，排查时被骗过一次 |
| 12 | 启动 dsh 时 **cwd 不能是 dsh 安装目录** | `...\@deepseek-ai\dsh\lib` 是只读的，dsh 把 cwd 当工作区 → 卡在版本提示后永不监听端口、进程却活着。用 `%USERPROFILE%`（详见上位 §4.3） |
| 13 | 别把「退出」和「完全退出」做成一样 | 见 §4 |
| 14 | `assets/source-icon.webp` 是**用户提供的素材** | 曾按"测试残留"删过一次。删文件前按内容判断，别按文件名/时间戳 |

---

## 3. 构建与版本

### 版本规则（必须遵守）

**每次产出新包都要升版本号**：

| 改动性质 | 升级位 | 示例 |
|---|---|---|
| 加需求 / 新功能 | 中间位 minor | 1.2.0 → 1.3.0 |
| 修 bug | 末位 patch | 1.2.0 → 1.2.1 |
| 仅改文档/注释、不影响程序 | 不必升 | — |

版本存在**两处**，必须同步，否则菜单显示与文件属性不一致：
- `internal/tray/main.go`：`var appVersion = "1.7.0"`（用 `var`，便于 `-ldflags -X` 覆盖）
- `configs/versioninfo.json`：`FixedFileInfo` 的 Major/Minor/Patch + `FileVersion`/`ProductVersion`（四段，如 `1.7.0.0`）

**统一用脚本，别手改**：
```powershell
.\build.ps1 -Version 1.8.0 -Note "add xxx"   # 加功能
.\build.ps1 -Version 1.7.1 -Note "fix xxx"   # 修 bug
.\build.ps1                                  # 版本不变，仅重新打包
go test ./...                                # 纯逻辑单测，不启动任何进程
```

`build.ps1` 依次做：同步两处版本号 → `go run ./tools/genicon` 生成 `resources/app.ico` →
`goversioninfo -manifest -icon` 重生成 `cmd/dsh-tray/versioninfo.syso` →
`go build -ldflags '-H windowsgui -s -w' -o bin/dsh-tray.exe ./cmd/dsh-tray` → 回显版本/大小。
依赖一次性安装：`go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest`。

改 `resources/app.manifest`、`configs/versioninfo.json`、版本号、角色帧后都要重跑 `build.ps1`（syso 是生成物但要提交）。
换角色图只需覆盖 `assets/running.png` / `stopped.png` / `starting-a.png` / `starting-b.png`。

版本沿革：1.0.0 首发 → 1.1.0 双击+自动打开 web → 1.2.0 退出拆分 → 1.2.1 目录重排 →
1.3.0 exe 图标 → 1.4.0 PATH 兜底+自更新+CI → 1.4.1 下载端点修复 → 1.5.0 设置窗口+代理+统一日志 →
1.5.1 残留窗体/布局对齐/403 回退直连 → 1.6.0 GitHub Token →
**1.7.0 dsh 路径可手填/浏览 + 自动识别 mise/volta/fnm/scoop 的 shim 启动器**。

---

## 4. 状态机与菜单语义

**三态**（姿态 + 角标颜色双通道，16px 缩小时靠颜色仍可辨）：

| 状态 | 判定 | 图标 | 角标 |
|---|---|---|---|
| `stStopped` | 进程不在（无 PID 且端口无监听） | `stopped.png` | 灰 `#8C8C8C` |
| `stStarting` | 进程在但未就绪 | `starting-a/b.png` 每 420ms 交替 | 橙 `#F09D2E` |
| `stRunning` | 日志出现就绪标志，或端口兜底 | `running.png` | 绿 `#2EA043` |

**"启动完成"以日志为准**：`logHasReadyMarker` 匹配 `dsh web: http` 或 `opening the default browser`，
先剥 ANSI 颜色码，**只扫描本次启动时刻之后的日志**（`readyOffset`），否则历史标志会让"启动中"瞬间消失。
⚠️ 加了 `--no-open` 时 dsh **不打印这两行**，所以保留端口兜底：端口监听满 `readyPortGrace`（10 秒）即认为就绪。
`portSince` 只记**首次**探测到监听的时刻，不要在探测抖动时重置，否则端口闪断会把兜底无限推后。

| 菜单项 | 可用条件 |
|---|---|
| 启动 | 仅 `stStopped` |
| 重启 | 始终（未运行时等同启动） |
| 打开web | **仅 `stRunning`** —— 启动中不可点，避免打开空页面 |
| 查看日志 / 检查dsh更新 / 设置 / 开机自启 / 启动后自动打开web | 始终 |
| 关闭 | 非 `stStopped`（含启动中，卡住时能中断） |
| **退出** | 始终 —— `quitApp()` 收图标 + `os.Exit(0)`，**不碰 dsh** |
| **完全退出** | 始终 —— `doExitAll()` 先 `dsh.Shutdown()` 再 `quitApp()` |
| 版本 x.y.z（最末项） | 始终可点 —— 检查本程序更新 |

⚠️ 「退出后 dsh 还能活」是因为子进程 stdout/stderr 直接接到**日志文件句柄**（不是管道），
父进程退出后句柄仍有效。**若哪天改成管道转发日志，这个语义会崩**，必须重新设计退出逻辑。

**双击图标**：`stStopped` → 启动；`stRunning` → 打开 web；`stStarting` → 不响应。
walk 的 `NotifyIcon` **只暴露 `MouseUp`**（单击/双击都触发，拿不到 `WM_LBUTTONDBLCLK`），
所以按两次点击的时间间隔自行判定（`doubleClickWindow = 450ms`）；单击本来无动作，无需延迟等待。

**「启动后自动打开web」= 是否给 dsh 传 `--no-open`**（`config.json` 的 `autoOpenWeb`，默认 `true`）。
取消勾选后就绪要走 10 秒端口兜底，比勾选时慢约 10 秒 —— **已知且可接受，不是 bug**。

**启动失败必须报出来**：`updateState` 检测 `stStarting → stStopped` 跳变，从日志最后一段
`===== ... 启动 dsh ... =====` 里找 `startup failed` 弹窗（`lastStartupFailure`）。
只认最后一段，避免把上次的旧错误当本次。

---

## 5. dsh 定位与进程控制

`resolveDshInstall()` 返回 `launch{exe, prefix, how}`，顺序（**已按 `dsh.go` 代码核对**）：

1. 设置里手填的 `dshPath`（`config.json`）—— **填了就只认它**，否则会读到另一份安装的 package.json（实测过）
2. 环境变量 `DSH_TRAY_DSH`（不改配置临时排查）
3. 常见 node 根目录 `candidateRoots()` → `node.exe + .../@deepseek-ai/dsh/lib/bin.js`
4. `findLauncher("dsh")`：PATH + `launcherDirs()`（`%LOCALAPPDATA%\mise\shims`、
   `~/.local/share/mise/shims`、`Volta\bin`、`pnpm`、`%APPDATA%\npm`、`scoop\shims`、`chocolatey\bin`）——
   同目录有完整安装 → node 模式；`.cmd` 能抠出 bin.js 且 node 可用 → node 模式；否则**直接执行启动器**
5. PATH 里 `node` 的安装目录

两种启动模式只差"命令行怎么拼"，`Start()` 统一用 `launch.argv(args...)`:

- **node 模式** `node.exe <bin.js> web [--no-open]` —— 窗口干净，不带批处理壳
- **shim 模式** `dsh.exe web [--no-open]` —— 交给 mise/volta 自己解析 node

⚠️ shim 模式下 `DshDir()` 拿不到包目录，`CurrentVersion()` 退化为执行 `dsh --version` 再 `firstSemver` 抠版本号。

**为什么需要手填路径**（1.7.0 的真实故障）：mise（旧名 rtx）/ volta / scoop 装出来的是**转发器 shim**，
既不是 `node.exe + node_modules` 结构（`probeRoot` 探不到），也不是可解析文本的 `.cmd`（`binJSFromShim` 抠不出路径），
而 shims 目录通常只在交互 shell 里被塞进 PATH —— 托盘从注册表 `Run` 键**冷启动时 PATH 里没有它**。
都找不到时 `errDshNotFound()` 弹窗引导用户执行 `where dsh` → 粘贴进设置窗口，或设 `DSH_TRAY_DSH`。

**僵尸凭证锁**（踩过的真实事故）：dsh 写 `~/.dsh/.credentials.yaml` 用 `.credentials.yaml.lock`（内容是 PID）。
进程被 `taskkill /F` 强杀时来不及删锁，下次启动直接
`startup failed: ... atomic-write: timed out waiting for the writer lock`。
两头都堵：启动前 `clearStaleCredLock()` **按 PID 存活判断**、已死才删（活着说明确有写入，绝不碰）；
非 PID 内容只在文件超过 60 秒才当残留；`Stop`/`StopTracked` 等进程真正消失后再清一次。
**判据必须是 PID 存活，不是文件年龄** —— 年龄阈值容易误删正在写入的锁。

**开机自启路径自愈**：已启用时每次启动都重写 Run 值（取 `os.Executable()`），移动目录/重命名 exe 不用手改注册表。
首启用靠 `installed.flag` 判断，之后只看 Run 键是否存在；`config.json` 的 `autoStart` 只是**副本，不反向覆盖注册表**。

---

## 6. 自更新与网络链路

点菜单最末「版本 x.y.z」→ `checkAppUpdate()` → `applyUpdate()` → `restartSelf()`。

| 环节 | 结论 |
|---|---|
| 下载端点 | API 资产端点，302 到 `release-assets.githubusercontent.com`（见 §2 #9） |
| 超时 | 元数据 `metaTimeout = 30s`；下载 `downloadTimeout = 30 * time.Minute`，用 context 控制；**中断后用 `Range: bytes=<have>-` 断点续传，最多 `attempts = 8` 轮**，服务端忽略 Range 返回 200 则从头重下 |
| 链路回退 | `proxyRoutes()`：配置的代理 → 同端口 HTTP 代理 → **直连**，谁通用谁，日志写明走哪条 |
| 配额 | `api.github.com` 匿名 **60 次/小时且按出口 IP 算**，共享代理/公司出口常被用光 → HTTP 403。别假定"配了代理就只能走代理" |
| Token | `githubToken()`：设置手填 → `GITHUB_TOKEN`/`GH_TOKEN` → `gh auth token`（`ghCandidates()` 兜底 `%LOCALAPPDATA%\Programs\gh\gh.exe`、`%ProgramFiles%\GitHub CLI\gh.exe`，不要求 gh 在 PATH）。带上后配额 5000 次/小时 |
| 报错 | `apiProblem()` 把 `X-RateLimit-Remaining/Reset` 翻成"匿名配额已用尽 0/60，约 7m0s 后恢复"，并一次列出所有链路失败原因，别只回 `HTTP 403` |

替换手法：把运行中的自身改名成 `dsh-tray.old.exe`（Windows 允许重命名运行中的镜像）→ 新文件挪到原位
→ 重启 → 下次启动 `cleanupOldBinary()` 清旧文件。

⚠️ **上位 `../../AGENTS.md` §2.5 早期写的「下载整体上限 15 分钟」已过时**，代码现为 30 分钟 + Range 续传；
涉及此项目的事实以**本文件与代码**为准。

---

## 7. 日志与排障

日志：`%LOCALAPPDATA%\dsh-tray\dsh-web.txt`。

- 本程序写的行统一带 `[dsh-tray] 时间戳` 前缀；dsh 自身 stdout/stderr 原样保留
- **每次启动/重启 dsh 时清空**（`openFreshLog`），只保留本次内容；托盘自身启动/退出不清空
- 启动错误、僵尸锁清理、检查 dsh 更新、应用自更新每一步都写进同一个文件
- `util.go` 的 `runHidden*` 负责隐藏子进程窗口 + **GBK → UTF-8 解码**（`decodeConsole`），
  判断逻辑别依赖中文文案的字节形态

设置窗口布局类问题（1.5.1 的两个坑）：
- **walk 分组框不会自动撑满宽度**：`BoxLayout` 只把交叉轴可拉伸（`GrowableHorz`/`ShrinkableHorz`）的子项铺满，
  只放两个 `CheckBox`（`LayoutFlags=0`）的分组框会缩成窄带并居中。判定依据
  `groupBoxLayoutItem.LayoutFlags()` 返回的是**它内部 composite 的标志**。
  修法：每行尾部放 `decl.HSpacer{}`（吃掉多余空间但控件保持自然宽度），按钮定宽配 `MaxSize`。
- 宿主窗口两道防护：`guardMainWindow()`（给 `mw.Closing()` 挂 `canceled=true` + 隐藏）+
  `hideMainWindow()`（在设置窗口 `Closing` 里**用 `mw.Synchronize` 延后**隐藏；同步隐藏会被 walk 之后的
  `ShowWindow` 立刻覆盖）+ `monitor()` 里 1 秒兜底。
- 验证别只看截图：**透明控件（Label、GroupBox 标题）的文字在 `Screenshot()` 里会被父窗口背景盖掉，看起来像空白**。
  用 `--shot` 输出的每个控件实际矩形判断（四个 `GroupBox` 同 `x` 且同宽、内部控件同 `x`）。

其他高频误判（详见上位 §4.3）：
- `dsh web` 冷启动 **8~55 秒**才监听，10 秒就断言"没起来"是错的
- 报 `startup failed` 可能是 `EADDRINUSE`（已有实例在跑），不是装坏了；访问 3080 返回 **HTTP 401 属正常**
- 进程活着 + 无输出 + 端口不监听 = 优先怀疑 **cwd 是只读目录**

---

## 8. 验证方式（自检优先，别靠点托盘）

windowsgui 程序没有 stdout，所以内置隐藏模式，结果写到 `%LOCALAPPDATA%\dsh-tray\`：

| 参数 | 作用 | 输出 |
|---|---|---|
| `--selftest` | dsh 定位（打印实际启动方式）、当前版本、npm 查版本、运行状态 | `selftest.txt` |
| `--uitest` | 真实创建托盘+菜单，走三态切换与动画两帧，3 秒后退出 | `uitest.txt` |
| `--settingstest` | 真实创建设置窗口，核对控件/配置联动/进度与状态刷新 | `settingstest.txt` |
| `--shot` | 设置窗口两个尺寸截图 + **每个控件实际矩形**；末尾验证「关闭后宿主窗口可见 = false」 | `settingsshot.txt` + `settingsshot-default.png` / `settingsshot-wide.png` |
| `--starttest` | 在 **3999 端口**起临时实例，记录端口监听/就绪判定/停止复位，**不碰 3080** | `starttest-report.txt` |
| `--checkupdate` | 查 release 并**真实下载一次** exe 核对字节数（慢网络下要几分钟） | `updatecheck.txt` |
| `go test ./...` | 纯逻辑单测：日志标志、僵尸锁、失败提取、semver、双击、启动参数、代理解析、日志前缀与截断、配置往返 | — |

改状态机/布局/进程控制后，**至少跑相关的自检 + `go test ./...`**，别只靠"编译过了"。

---

## 9. 发布流程

`.github/workflows/build.yml`：推 `main` 或 PR → `go test ./...` + `build.ps1` + 上传 artifact；
推 `v*` 标签 → `softprops/action-gh-release` 自动创建 Release 并附 `bin/dsh-tray.exe`。
应用内更新读的就是 `releases/latest`，所以发版必须打标签：

```powershell
.\build.ps1 -Version 1.8.0 -Note "..."      # 改版本 + 本地验证（含自检）
git add -A && git commit -m "..."
git tag -a v1.8.0 -m "v1.8.0"
git push origin main v1.8.0                 # CI 自动出 Release；走仓库里配好的 SOCKS 代理
```

本机 `gh` CLI 在 `%LOCALAPPDATA%\Programs\gh\gh.exe`（2.103.3，**不在 Git Bash 的 PATH 里，用绝对路径调**），
已登录 `kirning`（scopes: gist/read:org/repo/workflow）。查 Release / CI 结果直接用 `gh`，别手搓 REST。

---

## 10. 维护纪律

沿用上位 `../../AGENTS.md` §5，落到本目录：

1. **区分「验证过」和「看起来对」**：写进本文件的结论要真跑过自检/测试；未验证的显式标注。
2. **踩坑留痕三件套：症状 → 根因 → 解法**。只写"这里有个坑"等于没写。
3. **改根因，不掩症状**：被安全策略/网络问题拦住时先问"为什么被拦"。
4. **新增/修改代码后**：更新本文件对应章节（目录结构 §1、硬约束 §2、验证 §8），
   并回上位 `../../AGENTS.md` §1/§2 补导航 —— 但**细节只写本文件**，避免两份文档各自漂移。
5. **临时产物**进系统助手根目录 `temp/`，不要落在 `app/dsh-tray/` 里；`bin/` 已被 `.gitignore` 忽略。
6. 做完实质性工作，append 一条到 `.workbuddy/memory/YYYY-MM-DD.md`。
7. **凭证不硬编码**：Token 走 `config.json` / 环境变量 / `gh auth token`，不要写进仓库源码。
