// 外壳：侧栏导航（手机上变成底部标签栏）+ 顶栏（数据新鲜度、刷新、主题）。
import { useState, type ReactNode } from "react";
import { api } from "../api/client";
import { useConnState } from "../api/events";
import { waitForJob } from "../api/jobs";
import { useShared } from "../lib/data";
import { Link, usePath } from "../lib/router";
import { nextTheme, THEME_LABEL, useTheme } from "../lib/theme";
import { errMsg, Fresh, Spinner, useToast } from "./ui";

export const Logo = ({ size = 24 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M12 3v14" />
    <path d="M8 7h8" />
    <path d="M5 13a7 7 0 0 0 14 0" />
    <path d="M3 13h4M17 13h4" />
  </svg>
);

interface NavItem {
  path: string;
  title: string;
  icon: ReactNode;
}
export const NAV: (NavItem | "sep")[] = [
  { path: "/", title: "总览", icon: <><rect x="3" y="3" width="7" height="9" rx="1.5" /><rect x="14" y="3" width="7" height="5" rx="1.5" /><rect x="14" y="12" width="7" height="9" rx="1.5" /><rect x="3" y="16" width="7" height="5" rx="1.5" /></> },
  { path: "/instances", title: "实例", icon: <><rect x="3" y="4" width="18" height="7" rx="1.5" /><rect x="3" y="13" width="18" height="7" rx="1.5" /><path d="M7 7.5h.01M7 16.5h.01" /></> },
  { path: "/traffic", title: "流量", icon: <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" /> },
  "sep",
  { path: "/notify", title: "通知", icon: <><path d="M6 8a6 6 0 1 1 12 0c0 7 3 8 3 8H3s3-1 3-8" /><path d="M10 20a2 2 0 0 0 4 0" /></> },
  { path: "/settings", title: "设置", icon: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1" /></> },
];

export function Shell({ title, version, children, dataMode, readOnly }: { title: string; version?: string; children: ReactNode; dataMode: "demo" | "live"; readOnly: boolean }) {
  const path = usePath();
  const [theme, setTheme] = useTheme();
  const toast = useToast();
  const conn = useConnState();
  const { quotas, reloadAll } = useShared();
  const [refreshing, setRefreshing] = useState(false);

  const refresh = async () => {
    if (readOnly) { reloadAll(); toast("已更新页面显示"); return; }
    setRefreshing(true);
    try {
      const { job_id } = await api.refresh();
      const job = await waitForJob(job_id);
      if (job.status === "failed") toast(errMsg(new Error(job.error?.code ?? "internal_error")), true);
      else toast("已从阿里云重新取了一遍数据");
      reloadAll();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setRefreshing(false);
    }
  };

  const q = quotas.data ?? [];
  const official = q.map((x) => x.official_as_of).filter((x): x is string => !!x).sort()[0];
  const realtime = q.filter((x) => x.effective_bytes !== null).map((x) => x.updated_at).filter((x): x is string => !!x).sort().at(-1);

  return (
    <div className="app">
      <aside className="side">
        <div className="brand">
          <Logo />
          <span>Harbormaster</span>
        </div>
        <nav className="nav" aria-label="主导航">
          {NAV.map((item, i) =>
            item === "sep" ? (
              <div className="sep" key={i} />
            ) : (
              <Link key={item.path} to={item.path} className={path === item.path ? "active" : ""} aria-current={path === item.path ? "page" : undefined}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  {item.icon}
                </svg>
                <span>{item.title}</span>
              </Link>
            ),
          )}
        </nav>
        <div className="foot">
          {dataMode === "demo" && <span>演示数据 · 不是真实用量</span>}
          {version && <span>版本 {version}</span>}
        </div>
      </aside>
      <main className="main">
        <header className="top">
          <h1>{title}</h1>
          {dataMode === "demo" && <span className="chip hide-sm" title="演示数据，不代表账号实际用量。页面操作只改变本地示例，不会调用阿里云或发送通知。">演示数据</span>}
          <div className="spacer" />
          {q.length > 0 && (
            <>
              <span className="hide-sm" title="阿里云没有说明统计到几点时，这里不会拿读取时间顶替">
                {!official && q.some((x) => x.official_bytes !== null) ? <span className="chip stale">官方数据的统计时间不明</span> : <Fresh at={official} kind="official" prefix="官方数据截至" name="官方数据" />}
              </span>
              {realtime ? <Fresh at={realtime} kind="realtime" prefix="实时" name="实时数据" /> : <span className="chip stale">实时估算待确认</span>}
            </>
          )}
          {conn === "reconnecting" && (
            <span className="chip stale" title="和服务器的实时连接断开了，正在自动重连。页面上的数字可能不是最新的。">
              <span className="dot down" /> 实时连接已断开
            </span>
          )}
          <button className="btn" onClick={refresh} disabled={refreshing} title={readOnly ? "更新页面显示" : "立即从阿里云重新取数据"}>
            {refreshing ? <Spinner /> : "⟳"} <span className="hide-sm">{readOnly ? "更新显示" : "刷新"}</span>
          </button>
          <button className="btn" onClick={() => setTheme(nextTheme(theme))} aria-label={`主题：${THEME_LABEL[theme]}，点击切换`} title={`主题：${THEME_LABEL[theme]}`}>
            ◐ <span className="hide-sm">{THEME_LABEL[theme]}</span>
          </button>
        </header>
        <div className="content">
          <div className="page">
            {dataMode === "demo" && readOnly && <div className="preview-note" role="status">这是演示数据，不代表账号实际用量。目前只能查看，配置与资源操作尚未开放。</div>}
            {children}
          </div>
        </div>
      </main>
    </div>
  );
}
