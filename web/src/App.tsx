// 启动与顶层路由：收敛为 6 个高内聚工作区，并对旧路由 (/billing, /notify) 做平滑兼容重定向。
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, MOCK, setReadOnly, setUnauthenticatedHandler } from "./api/client";
import { connectEvents, disconnectEvents } from "./api/events";
import { Shell } from "./components/Shell";
import { EmptyState, ErrorState, Skeleton, ToastProvider } from "./components/ui";
import { DataProvider } from "./lib/data";
import { setTimezone } from "./lib/format";
import { Link, navigate, usePath } from "./lib/router";
import { useTheme } from "./lib/theme";
import { Login } from "./pages/Login";
import { Overview } from "./pages/Overview";
import { Setup } from "./pages/Setup";

const Instances = lazy(() => import("./pages/Instances").then((m) => ({ default: m.Instances })));
const Traffic = lazy(() => import("./pages/Traffic").then((m) => ({ default: m.Traffic })));
const Network = lazy(() => import("./pages/Network").then((m) => ({ default: m.Network })));
const Automation = lazy(() => import("./pages/Automation").then((m) => ({ default: m.Automation })));
const Settings = lazy(() => import("./pages/Settings").then((m) => ({ default: m.Settings })));

const ScenePanel = import.meta.env.VITE_API_MODE === "mock" ? lazy(() => import("./components/ScenePanel")) : null;

type Phase = "booting" | "boot_error" | "setup" | "login" | "app";

const PAGES: Record<string, { title: string; sub: string; el: (p: { onLogout: () => void }) => React.ReactNode }> = {
  "/": { title: "总览", sub: "多账号免费流量池、实例运行态与关键动态", el: () => <Overview /> },
  "/instances": { title: "实例管理", sub: "云服务器与轻量应用服务器状态、实时速率与远程控制", el: () => <Instances /> },
  "/traffic": { title: "流量与费用", sub: "CDT 免费额度、趋势预测、账户余额账单与闲置巡检", el: () => <Traffic /> },
  "/network": { title: "网络监测", sub: "多点延迟矩阵、丢包趋势与去回程路由追踪", el: () => <Network /> },
  "/automation": { title: "策略与通知", sub: "自动停开机、跨账号主备容灾、告警规则与多渠道投递", el: () => <Automation /> },
  "/settings": { title: "系统设置", sub: "云账号凭证、采集程序、登录安全、API 与任务审计", el: (p) => <Settings onLogout={p.onLogout} /> },
};

export function App() {
  useTheme();
  const path = usePath();
  const [phase, setPhase] = useState<Phase>("booting");
  const [bootError, setBootError] = useState<ApiError>();
  const [minPassword, setMinPassword] = useState(10);
  const [version, setVersion] = useState<string>();
  const [dataMode, setDataMode] = useState<"demo" | "live">(MOCK ? "demo" : "live");
  const [readOnly, updateReadOnly] = useState(false);
  const returnTo = useRef<string | null>(null);

  const boot = useCallback(async () => {
    setPhase("booting");
    try {
      const s = await api.setupStatus();
      setMinPassword(s.password_min_length);
      if (!s.initialized) return setPhase("setup");
      try {
        await api.session();
        setPhase("app");
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) setPhase("login");
        else throw e;
      }
    } catch (e) {
      setBootError(e instanceof ApiError ? e : new ApiError(0, "network_error", String(e)));
      setPhase("boot_error");
    }
  }, []);

  useEffect(() => {
    boot();
    setUnauthenticatedHandler(() => setPhase((p) => (p === "app" ? "login" : p)));
  }, [boot]);

  useEffect(() => {
    if (phase !== "app") return;
    connectEvents();
    api.settings().then((s) => setTimezone(s.business_timezone), () => {});
    api.systemInfo().then(
      (i) => {
        setVersion(i.version);
        setDataMode(i.data_mode);
        updateReadOnly(i.read_only);
        setReadOnly(i.read_only);
      },
      () => {},
    );
    return disconnectEvents;
  }, [phase]);

  // 兼容旧路由与登录态重定向
  useEffect(() => {
    if (phase === "setup" && path !== "/setup") navigate("/setup", true);
    if (phase === "login" && path !== "/login") {
      returnTo.current = path === "/setup" ? null : path + location.search;
      navigate("/login", true);
    }
    if (phase === "app") {
      if (path === "/login" || path === "/setup") {
        navigate(returnTo.current ?? "/", true);
        returnTo.current = null;
      } else if (path === "/billing") {
        navigate("/traffic?tab=billing", true);
      } else if (path === "/notify") {
        navigate("/automation?tab=channels", true);
      }
    }
  }, [phase, path]);

  const logout = async () => {
    try {
      await api.logout();
    } finally {
      setPhase("login");
    }
  };

  let body: React.ReactNode;
  if (phase === "booting")
    body = (
      <div className="solo">
        <div className="solo-box">
          <Skeleton lines={3} />
        </div>
      </div>
    );
  else if (phase === "boot_error")
    body = (
      <div className="solo">
        <div className="solo-box card">
          <ErrorState error={bootError!} onRetry={boot} />
        </div>
      </div>
    );
  else if (phase === "setup") body = <Setup minPassword={minPassword} onDone={() => setPhase("app")} />;
  else if (phase === "login") body = <Login onDone={() => setPhase("app")} />;
  else {
    const page = PAGES[path];
    body = (
      <DataProvider>
        <Shell title={page?.title ?? "找不到页面"} sub={page?.sub} version={version} dataMode={dataMode} readOnly={readOnly}>
          {page ? (
            <Suspense fallback={<div className="card"><Skeleton lines={6} /></div>}>
              {page.el({ onLogout: logout })}
            </Suspense>
          ) : (
            <div className="card">
              <EmptyState title="这个地址没有对应的页面" action={<Link className="btn primary" to="/">回到总览</Link>}>
                可能是链接输入有误，或已整合进新的导航入口。
              </EmptyState>
            </div>
          )}
        </Shell>
      </DataProvider>
    );
  }

  return (
    <ToastProvider>
      {body}
      {ScenePanel && (
        <Suspense fallback={null}>
          <ScenePanel />
        </Suspense>
      )}
    </ToastProvider>
  );
}
