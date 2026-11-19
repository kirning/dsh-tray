import { useCallback, useEffect, useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Input,
  Progress,
  Space,
  Spin,
  Switch,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  ApiOutlined,
  CloudDownloadOutlined,
  FolderOpenOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  SaveOutlined,
  StopOutlined,
  SyncOutlined,
} from "@ant-design/icons";
import { Events } from "@wailsio/runtime";
import * as svc from "../bindings/github.com/kirning/dsh-tray/internal/app/service";
import type { Status, UpdateInfo } from "../bindings/github.com/kirning/dsh-tray/internal/app/models";

const { Text } = Typography;

type Busy = "" | "start" | "stop" | "restart" | "open" | "check" | "update" | "save" | "log";

function errText(e: unknown): string {
  if (typeof e === "string") return e;
  if (e && typeof e === "object" && "message" in e) return String((e as { message: unknown }).message);
  return JSON.stringify(e);
}

function stateTag(state: string) {
  if (state === "running") return <Tag color="success">已就绪</Tag>;
  if (state === "starting") return <Tag color="warning">启动中</Tag>;
  return <Tag>未运行</Tag>;
}

export default function App() {
  const { message } = AntApp.useApp();
  const [status, setStatus] = useState<Status | null>(null);
  const [busy, setBusy] = useState<Busy>("");

  const [dshPath, setDshPath] = useState("");
  const [proxy, setProxy] = useState("");
  const [token, setToken] = useState("");
  const [log, setLog] = useState("");
  const [dshUpdate, setDshUpdate] = useState<UpdateInfo | null>(null);
  const [appUpdate, setAppUpdate] = useState<UpdateInfo | null>(null);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);

  const refresh = useCallback(async () => {
    try {
      const s = await svc.GetStatus();
      setStatus(s);
      setDshPath((prev) => (prev === "" ? s.config?.dshPath ?? "" : prev));
      setProxy((prev) =>
        prev === "" && s.config?.proxy ? `${s.config.proxyScheme || "socks5"}://${s.config.proxy}` : prev,
      );
      setToken((prev) => (prev === "" ? s.config?.githubToken ?? "" : prev));
    } catch {
      // 启动瞬间绑定可能还没就绪，下一次轮询会补上
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), 2000);
    const offStatus = Events.On("status", (e: { data: Status }) => setStatus(e.data));
    const offProgress = Events.On("update-progress", (e: { data: { done: number; total: number } }) =>
      setProgress(e.data),
    );
    return () => {
      clearInterval(timer);
      offStatus();
      offProgress();
    };
  }, [refresh]);

  const run = async (key: Busy, fn: () => Promise<unknown>, okText?: string) => {
    setBusy(key);
    try {
      const result = await fn();
      if (okText) message.success(okText);
      if (typeof result === "string" && result !== "") message.success(result);
      return result;
    } catch (e) {
      message.error(errText(e));
      return undefined;
    } finally {
      setBusy("");
      void refresh();
    }
  };

  const loadLog = async () => {
    setBusy("log");
    try {
      setLog(await svc.ReadLog());
    } catch (e) {
      message.error(errText(e));
    } finally {
      setBusy("");
    }
  };

  if (!status) {
    return (
      <div style={{ display: "grid", placeItems: "center", height: "60vh" }}>
        <Spin tip="正在读取状态…" size="large">
          <div style={{ width: 200, height: 60 }} />
        </Spin>
      </div>
    );
  }

  const running = status.state === "running";
  const stopped = status.state === "stopped";
  const pct = progress && progress.total > 0 ? Math.round((progress.done / progress.total) * 100) : 0;

  return (
    <Space direction="vertical" size={14} style={{ width: "100%" }}>
      <Card size="small">
        <div className="section-head">
          <Space size={10} align="center">
            {stateTag(status.state)}
            <Text strong>dsh web</Text>
            <Text type="secondary">
              端口 {status.port} · dsh {status.dshVersion || "未知"} · 托盘 {status.appVersion} · {status.os}
            </Text>
          </Space>
          <Space size={8} wrap>
            <Button
              type="primary"
              icon={<PlayCircleOutlined />}
              disabled={!stopped}
              loading={busy === "start"}
              onClick={() => void run("start", () => svc.StartDsh(), "已启动")}
            >
              启动
            </Button>
            <Button
              icon={<SyncOutlined />}
              loading={busy === "restart"}
              onClick={() => void run("restart", () => svc.RestartDsh(), "已重启")}
            >
              重启
            </Button>
            <Button
              danger
              icon={<StopOutlined />}
              disabled={stopped}
              loading={busy === "stop"}
              onClick={() => void run("stop", () => svc.StopDsh(), "已关闭")}
            >
              关闭
            </Button>
            <Tooltip title={running ? "在浏览器打开 127.0.0.1 上的 dsh web" : "启动完成后才能打开"}>
              <Button
                icon={<ApiOutlined />}
                disabled={!running}
                loading={busy === "open"}
                onClick={() => void run("open", () => svc.OpenWeb())}
              >
                打开 web
              </Button>
            </Tooltip>
          </Space>
        </div>
      </Card>

      <Card size="small" title="dsh 程序路径">
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Space.Compact style={{ width: "100%" }}>
            <Input
              value={dshPath}
              placeholder="留空 = 自动探测。可直接粘贴路径，如 %LOCALAPPDATA%\mise\shims\dsh.exe 或 /home/me/.local/share/mise/shims/dsh"
              onChange={(e) => setDshPath(e.target.value)}
              allowClear
            />
            <Button
              icon={<SaveOutlined />}
              loading={busy === "save"}
              onClick={() => void run("save", () => svc.SetDshPath(dshPath))}
            >
              保存并验证
            </Button>
            <Button onClick={() => void run("save", () => svc.SetDshPath(""), "已恢复自动探测")}>自动探测</Button>
          </Space.Compact>
          <Text className="hint">
            用 mise / volta / scoop 等版本管理器时，托盘冷启动的 PATH 里可能没有 shims 目录，
            手填一次最省事。保存后会用 dsh --version 实际验证。
          </Text>
        </Space>
      </Card>

      <Card size="small" title="代理服务器">
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Space.Compact style={{ width: "100%" }}>
            <Input
              value={proxy}
              placeholder="IP:端口（留空 = 直连），可写 socks5:// 或 http:// 前缀"
              onChange={(e) => setProxy(e.target.value)}
              allowClear
            />
            <Button
              loading={busy === "save"}
              onClick={() => void run("save", () => svc.ProbeProxy(proxy), "探测通过")}
            >
              测试
            </Button>
            <Button
              type="primary"
              icon={<SaveOutlined />}
              loading={busy === "save"}
              onClick={() => void run("save", () => svc.SaveProxy(proxy))}
            >
              保存
            </Button>
            <Button
              onClick={() => {
                setProxy("");
                void run("save", () => svc.SaveProxy(""), "已改为直连");
              }}
            >
              清空
            </Button>
          </Space.Compact>
          <Text className="hint">
            优先按 SOCKS5 握手探测，不通自动回退 HTTP。代理同时用于下载本程序更新和 npm 查版本。
          </Text>
        </Space>
      </Card>

      <Card size="small" title="启动配置">
        <Space direction="vertical" size={10} style={{ width: "100%" }}>
          <div className="section-head">
            <Text>开机自启</Text>
            <Switch
              checked={status.autostart}
              onChange={(v) => void run("save", () => svc.SetAutostart(v), v ? "已开启" : "已关闭")}
            />
          </div>
          <div className="section-head">
            <Text>启动后自动打开 web</Text>
            <Switch
              checked={status.config?.autoOpenWeb ?? true}
              onChange={(v) =>
                void run("save", () =>
                  svc.SaveConfig({ ...(status.config as object), autoOpenWeb: v } as never),
                )
              }
            />
          </div>
          <Text className="hint">
            关闭“自动打开 web”时启动参数会加 --no-open；此时 dsh 不打印就绪日志，
            状态会晚约 10 秒（端口兜底）才转为已就绪，属正常现象。
          </Text>
        </Space>
      </Card>

      <Card size="small" title="版本与更新">
        <Space direction="vertical" size={10} style={{ width: "100%" }}>
          <div className="section-head">
            <Text>
              托盘程序 <Text strong>{status.appVersion}</Text>
            </Text>
            <Space size={8}>
              <Button
                size="small"
                loading={busy === "check"}
                onClick={async () => {
                  const info = (await run("check", () => svc.CheckAppUpdate())) as UpdateInfo | undefined;
                  if (info) {
                    setAppUpdate(info);
                    message.info(info.hasUpdate ? `发现新版本 ${info.latest}` : info.note || "已是最新");
                  }
                }}
              >
                检查更新
              </Button>
              {appUpdate?.hasUpdate && (
                <Button
                  size="small"
                  type="primary"
                  icon={<CloudDownloadOutlined />}
                  loading={busy === "update"}
                  onClick={() =>
                    void run("update", () => svc.ApplyAppUpdate(), "下载完成，正在重启…").then(() => {
                      svc.RestartApp();
                    })
                  }
                >
                  更新到 {appUpdate.latest}
                </Button>
              )}
            </Space>
          </div>

          {progress && (
            <Progress percent={pct} size="small" status={pct >= 100 ? "success" : "active"} />
          )}

          <div className="section-head">
            <Text>
              已安装 dsh <Text strong>{status.dshVersion || "未知"}</Text>
            </Text>
            <Button
              size="small"
              loading={busy === "check"}
              onClick={async () => {
                const info = (await run("check", () => svc.CheckDshUpdate())) as UpdateInfo | undefined;
                if (info) {
                  setDshUpdate(info);
                  message.info(
                    info.hasUpdate ? `有新版本 ${info.latest}（当前 ${info.current}）` : info.note || "已是最新",
                  );
                }
              }}
            >
              检查 dsh 更新
            </Button>
          </div>
          {dshUpdate?.hasUpdate && (
            <Alert
              type="info"
              showIcon
              message={`npm 上有新版本 ${dshUpdate.latest}`}
              description="本程序只负责查询，升级请在你的终端里执行 npm i -g @deepseek-ai/dsh 或对应版本管理器的命令。"
            />
          )}

          <Space.Compact style={{ width: "100%" }}>
            <Input.Password
              value={token}
              placeholder="GitHub Token（可选）：把 API 配额从 60 次/小时提到 5000"
              onChange={(e) => setToken(e.target.value)}
            />
            <Button
              loading={busy === "save"}
              onClick={() => void run("save", () => svc.SaveToken(token))}
            >
              保存并验证
            </Button>
          </Space.Compact>
        </Space>
      </Card>

      <Card
        size="small"
        title="日志"
        extra={
          <Space size={6}>
            <Button size="small" icon={<ReloadOutlined />} loading={busy === "log"} onClick={loadLog}>
              刷新
            </Button>
            <Button
              size="small"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(log);
                  message.success("已复制");
                } catch {
                  message.error("复制失败，请手动选中复制");
                }
              }}
            >
              复制全部
            </Button>
            <Button
              size="small"
              icon={<FolderOpenOutlined />}
              onClick={() => void run("log", () => svc.OpenLogFile())}
            >
              打开文件
            </Button>
          </Space>
        }
      >
        <Space direction="vertical" size={6} style={{ width: "100%" }}>
          <Text className="hint">{status.logPath}</Text>
          <Input.TextArea
            className="log-box"
            value={log}
            readOnly
            rows={12}
            placeholder="点“刷新”读取 dsh-web.txt"
          />
        </Space>
      </Card>
    </Space>
  );
}
