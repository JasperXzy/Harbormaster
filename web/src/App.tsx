// 启动流程：是否已安装 -> 是否已登录 -> 进入面板。
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, MOCK, setReadOnly, setUnauthenticatedHandler } from "./api/client";
import { connectEvents, disconnectEvents } from "./api/events";
import { Shell } from "./components/Shell";
import { ErrorState, Skeleton, ToastProvider } from "./components/ui";
import { DataProvider } from "./lib/data";
import { setTimezone } from "./lib/format";
import { navigate, usePath } from "./lib/router";
import { useTheme } from "./lib/theme";
import { Billing } from "./pages/Billing";
import { Instances } from "./pages/Instances";
import { Login } from "./pages/Login";
import { Notify } from "./pages/Notify";
import { Overview } from "./pages/Overview";
import { Settings } from "./pages/Settings";
import { Setup } from "./pages/Setup";
import { Traffic } from "./pages/Traffic";
import { EmptyState } from "./components/ui";
import { Link } from "./lib/router";

const ScenePanel = import.meta.env.VITE_API_MODE === "mock" ? lazy(() => import("./components/ScenePanel")) : null;

type Phase = "booting" | "boot_error" | "setup" | "login" | "app";

const PAGES: Record<string, { title: string; el: (p: { onLogout: () => void }) => React.ReactNode }> = {
  "/": { title: "总览", el: () => <Overview /> },
  "/instances": { title: "实例", el: () => <Instances /> },
  "/traffic": { title: "流量", el: () => <Traffic /> },
  "/billing": { title: "费用", el: () => <Billing /> },
  "/notify": { title: "通知", el: () => <Notify /> },
  "/settings": { title: "设置", el: (p) => <Settings onLogout={p.onLogout} /> },
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

  // 进入面板后：连上事件流，读取时区与版本
  useEffect(() => {
    if (phase !== "app") return;
    connectEvents();
    api.settings().then((s) => setTimezone(s.business_timezone), () => {});
    api.systemInfo().then((i) => { setVersion(i.version); setDataMode(i.data_mode); updateReadOnly(i.read_only); setReadOnly(i.read_only); }, () => {});
    return disconnectEvents;
  }, [phase]);

  // 地址与阶段保持一致
  useEffect(() => {
    if (phase === "setup" && path !== "/setup") navigate("/setup", true);
    if (phase === "login" && path !== "/login") {
      returnTo.current = path === "/setup" ? null : path; // 登录后回到原来想去的页面
      navigate("/login", true);
    }
    if (phase === "app" && (path === "/login" || path === "/setup")) {
      navigate(returnTo.current ?? "/", true);
      returnTo.current = null;
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
        <Shell title={page?.title ?? "找不到页面"} version={version} dataMode={dataMode} readOnly={readOnly}>
          {page ? (
            page.el({ onLogout: logout })
          ) : (
            <div className="card">
              <EmptyState title="这个地址没有对应的页面" action={<Link className="btn primary" to="/">回到总览</Link>}>
                可能是链接写错了，或者这个功能还没有上线。
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
