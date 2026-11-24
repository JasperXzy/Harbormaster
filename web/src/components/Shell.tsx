// 外壳：现代云控制台侧边栏（手机端为 4 核心项 +「更多」底部抽屉）+ 低噪毛玻璃顶栏。
import { useState, type ReactNode } from "react";
import { api } from "../api/client";
import { useConnState } from "../api/events";
import { waitForJob } from "../api/jobs";
import { useShared } from "../lib/data";
import { isStale } from "../lib/freshness";
import { stamp, ver } from "../lib/format";
import { Link, usePath } from "../lib/router";
import { nextTheme, THEME_LABEL, useTheme } from "../lib/theme";
import { quotaView } from "../lib/usage";
import { MonitorIcon, MoonIcon, MoreIcon, RefreshIcon, SunIcon } from "./icons";
import { errMsg, InfoTip, Spinner, useToast } from "./ui";

export const Logo = ({ size = 18 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M12 3v14" />
    <path d="M8 7h8" />
    <path d="M5 13a7 7 0 0 0 14 0" />
    <path d="M3 13h4M17 13h4" />
  </svg>
);

interface NavItem {
  path: string;
  shortTitle: string;
  title: string;
  mobilePrimary?: boolean;
  icon: ReactNode;
}

export const NAV: NavItem[] = [
  {
    path: "/",
    title: "总览",
    shortTitle: "总览",
    mobilePrimary: true,
    icon: <><rect x="3" y="3" width="7" height="9" rx="1.5" /><rect x="14" y="3" width="7" height="5" rx="1.5" /><rect x="14" y="12" width="7" height="9" rx="1.5" /><rect x="3" y="16" width="7" height="5" rx="1.5" /></>,
  },
  {
    path: "/instances",
    title: "实例",
    shortTitle: "实例",
    mobilePrimary: true,
    icon: <><rect x="3" y="4" width="18" height="7" rx="1.5" /><rect x="3" y="13" width="18" height="7" rx="1.5" /><path d="M7 7.5h.01M7 16.5h.01" /></>,
  },
  {
    path: "/traffic",
    title: "流量与费用",
    shortTitle: "流量",
    mobilePrimary: true,
    icon: <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />,
  },
  {
    path: "/network",
    title: "网络监测",
    shortTitle: "网络",
    mobilePrimary: true,
    icon: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18" /></>,
  },
  {
    path: "/automation",
    title: "策略与通知",
    shortTitle: "策略",
    icon: <path d="M13 2 4 14h7l-1 8 9-12h-7z" />,
  },
  {
    path: "/settings",
    title: "系统设置",
    shortTitle: "设置",
    icon: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1" /></>,
  },
];

export function Shell({
  title,
  version,
  children,
  dataMode,
  readOnly,
}: {
  title: string;
  sub?: string;
  version?: string;
  children: ReactNode;
  dataMode: "demo" | "live";
  readOnly: boolean;
}) {
  const path = usePath();
  const [theme, setTheme] = useTheme();
  const toast = useToast();
  const conn = useConnState();
  const { accounts, quotas, instances, branding, reloadAll } = useShared();
  const [refreshing, setRefreshing] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);

  const refresh = async () => {
    if (readOnly) {
      reloadAll();
      toast("已更新页面显示");
      return;
    }
    setRefreshing(true);
    try {
      const { job_id } = await api.refresh();
      const job = await waitForJob(job_id);
      if (job.status === "failed") toast(errMsg(new Error(job.error?.code ?? "internal_error")), true);
      else toast("已从阿里云同步最新数据");
      reloadAll();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setRefreshing(false);
    }
  };

  const q = quotas.data ?? [];
  const accs = accounts.data ?? [];
  const failingAccs = accs.filter((a) => a.enabled && a.sync_status === "failing").length;
  const official = q.map((x) => x.official_as_of).filter((x): x is string => !!x).sort()[0];
  const views = q.map((x) => quotaView(x));
  const realtime = views.filter((v) => v.usable).map((v) => v.updatedAt).filter((x): x is string => !!x).sort().at(-1);
  const pendingConfirms = (instances.data?.items ?? []).filter((i) => i.pending_intent_confirmation).length;

  const isDisconn = conn === "reconnecting";
  const hasSyncTrouble = failingAccs > 0 || (!!official && isStale(official, "official") && !realtime);
  const latestStamp = realtime ?? official ?? instances.data?.updated_at ?? null;

  const brandName = branding.data?.name || "Harbormaster";
  const brandLogo = branding.data?.logo?.url ?? null;

  const themeIcon = theme === "light" ? <SunIcon size={16} /> : theme === "dark" ? <MoonIcon size={16} /> : <MonitorIcon size={16} />;
  const moreActive = path === "/automation" || path === "/settings";

  return (
    <div className="app">
      <aside className="side">
        <div className="brand">
          {brandLogo ? (
            <img src={brandLogo} alt="" width={28} height={28} style={{ borderRadius: 8, objectFit: "cover", flex: "none" }} />
          ) : (
            <span className="brand-mark">
              <Logo size={17} />
            </span>
          )}
          <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{brandName}</span>
        </div>

        <nav className="nav" aria-label="主导航">
          {NAV.map((item, idx) => (
            <div key={item.path} className={item.mobilePrimary ? "" : "desktop-nav-extra"} style={{ display: "contents" }}>
              {idx === 4 && <div className="sep" />}
              <Link
                to={item.path}
                className={`${path === item.path ? "active" : ""} ${item.mobilePrimary ? "" : "desktop-nav-extra"}`}
                aria-current={path === item.path ? "page" : undefined}
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.85" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  {item.icon}
                </svg>
                <span className="only-wide">{item.title}</span>
                <span className="only-narrow">{item.shortTitle}</span>
                {item.path === "/automation" && pendingConfirms > 0 && (
                  <span className="count" title="有待确认的外部关机">
                    {pendingConfirms}
                  </span>
                )}
                {item.path === "/settings" && failingAccs > 0 && (
                  <span className="count" title="有连接失败的账号">
                    {failingAccs}
                  </span>
                )}
              </Link>
            </div>
          ))}

          {/* Mobile 5th slot: More sheet trigger */}
          <button
            type="button"
            className={`nav-more-btn mobile-only ${moreActive ? "active" : ""}`}
            style={moreActive ? { color: "var(--accent)", fontWeight: 600 } : undefined}
            onClick={() => setMoreOpen(true)}
          >
            <MoreIcon size={20} />
            <span>更多</span>
            {pendingConfirms + failingAccs > 0 && <span className="count">{pendingConfirms + failingAccs}</span>}
          </button>
        </nav>

        <div className="foot">
          <span>{version ? ver(version) : "Harbormaster"}</span>
          <span>{dataMode === "demo" ? "演示环境" : "生产就绪"}</span>
        </div>
      </aside>

      <main className="main">
        <header className="top">
          <div className="top-title-group">
            <h1>{title}</h1>
          </div>
          {dataMode === "demo" && <span className="chip">演示数据</span>}
          <div className="spacer" />

          {/* 单一干净的同步状态指示器，不再堆叠多个黄色标签 */}
          <span className={`chip ${isDisconn || hasSyncTrouble ? "stale" : ""}`}>
            <span className={`dot ${isDisconn ? "down" : hasSyncTrouble ? "warn" : "live"}`} />
            <span>
              {isDisconn
                ? "正在重连事件流…"
                : failingAccs > 0
                  ? `${failingAccs} 个账号同步异常`
                  : latestStamp
                    ? `同步于 ${stamp(latestStamp)}`
                    : "运行正常"}
            </span>
            <InfoTip label="数据同步口径详情">
              <div><b>实时采集：</b>{realtime ? stamp(realtime) : "等待在线 Agent 覆盖"}</div>
              <div><b>阿里云官方累计：</b>{official ? stamp(official) : "按每 5 分钟轮询（官方未返回精确截止秒数）"}</div>
              <div style={{ marginTop: 4 }}>实时流状态：{isDisconn ? "重连中" : "SSE 已连接"}</div>
            </InfoTip>
          </span>

          <button
            className="btn icon"
            onClick={refresh}
            disabled={refreshing}
            aria-label={readOnly ? "更新显示" : "立即同步"}
            title={readOnly ? "更新页面显示" : "立即从阿里云同步最新资源与流量"}
          >
            {refreshing ? <Spinner /> : <RefreshIcon size={16} />}
          </button>
          <button
            className="btn icon"
            onClick={() => setTheme(nextTheme(theme))}
            aria-label={`主题：${THEME_LABEL[theme]}，点击切换`}
            title={`当前主题：${THEME_LABEL[theme]}（点击切换）`}
          >
            {themeIcon}
          </button>
        </header>

        <div className="content">
          <div className="page">
            {dataMode === "demo" && readOnly && (
              <div className="preview-note" role="status">
                当前处于只读演示模式，数据仅供界面预览，不会调用阿里云或下发通知。
              </div>
            )}
            {children}
          </div>
        </div>
      </main>

      {moreOpen && (
        <div className="mobile-sheet-bg" onClick={() => setMoreOpen(false)}>
          <div className="mobile-sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sub" style={{ padding: "2px 8px 6px", fontWeight: 600 }}>更多工作区</div>
            {NAV.filter((x) => !x.mobilePrimary).map((item) => (
              <Link
                key={item.path}
                to={item.path}
                className={path === item.path ? "active" : ""}
                onClick={() => setMoreOpen(false)}
              >
                <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.85" strokeLinecap="round" strokeLinejoin="round">
                  {item.icon}
                </svg>
                <span>{item.title}</span>
                {item.path === "/automation" && pendingConfirms > 0 && <span className="count" style={{ marginLeft: "auto" }}>{pendingConfirms}</span>}
              </Link>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
