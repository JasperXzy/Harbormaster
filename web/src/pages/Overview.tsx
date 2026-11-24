// 总览：低噪 Bento 仪表盘（可折叠待办栏 + 四大核心指标卡 + 账号配额双列网格 + 活跃实例与最近动态）。
import { api } from "../api/client";
import { useEvent } from "../api/events";
import { INSTANCE_STATUS, SCOPE_SHORT, SEVERITY, regionName } from "../api/labels";
import type { Account, Instance, Notification, QuotaScope, QuotaStatus } from "../api/types";
import { ActivityIcon, ChevronRight, GlobeIcon, ServerIcon, ShieldIcon } from "../components/icons";
import { quotaLevel, QuotaLegend, QuotaMeter, quotaUnknown, QuotaUnknownNote } from "../components/QuotaMeter";
import { Async, Badge, Card, EmptyState, ErrorState, KpiCard, Select, Skeleton, type Level } from "../components/ui";
import { useShared } from "../lib/data";
import { gb, rate, stamp } from "../lib/format";
import {
  ACCOUNT_SORT_OPTIONS,
  INSTANCE_SORT_OPTIONS,
  sortAccounts,
  sortInstances,
  useAccountSort,
  useInstanceSort,
  type AccountSortKey,
  type InstanceSortKey,
} from "../lib/instanceSort";
import { Link } from "../lib/router";
import { useResource } from "../lib/useResource";
import { quotaPool as pool } from "../lib/usage";

const ORDER: Level[] = ["good", "neutral", "warning", "serious", "critical"];
const worse = (a: Level, b: Level) => (ORDER.indexOf(a) >= ORDER.indexOf(b) ? a : b);

const LEADING_STATUS_EMOJI = /^[🟢🟡🟠🔴⚪️⚠️🚨✅❌]+\s*/u;

export function NotificationItem({ n }: { n: Notification }) {
  const [level, text] = SEVERITY[n.severity];
  const title = n.title.replace(LEADING_STATUS_EMOJI, "");
  return (
    <div className="aitem" title={n.body || undefined}>
      <Badge level={n.kind === "recovery" ? "good" : level}>
        {n.kind === "recovery" ? "恢复" : n.kind === "test" ? "测试" : text}
      </Badge>
      <div className="t">{title}</div>
      <div className="when">{stamp(n.created_at)}</div>
    </div>
  );
}

export function Overview() {
  const { accounts, instances, quotas } = useShared();
  const packages = useResource(api.bandwidthPackages, [], { refreshMs: 30_000 });
  const notes = useResource(() => api.notifications({ limit: 5 }), []);
  const telegram = useResource(api.telegram, []);
  const email = useResource(api.emailChannel, []);
  const webhook = useResource(api.webhookChannel, []);
  const sims = useResource(api.quotaSimulations, [], { refreshMs: 30_000 });
  const { sort: instSort, updateSort: updateInstSort } = useInstanceSort();
  const { sort: accSort, updateSort: updateAccSort } = useAccountSort();
  useEvent("notification.created", () => notes.reload());

  const firstError = accounts.error ?? instances.error ?? quotas.error;
  if (firstError && !accounts.data) {
    return (
      <div className="card">
        <ErrorState
          error={firstError}
          onRetry={() => {
            accounts.reload();
            instances.reload();
            quotas.reload();
          }}
        />
      </div>
    );
  }
  if (!accounts.data || !instances.data || !quotas.data) {
    return (
      <>
        <div className="kpi-grid">
          {[0, 1, 2, 3].map((i) => (
            <div className="kpi-card" key={i}>
              <Skeleton lines={2} height={18} />
            </div>
          ))}
        </div>
        <div className="grid g2">
          {[0, 1].map((i) => (
            <div className="card" key={i}>
              <Skeleton lines={5} />
            </div>
          ))}
        </div>
      </>
    );
  }

  const accs = accounts.data;
  const insts = instances.data.items;
  const qs = quotas.data;
  const accName = (id: string) => accs.find((a) => a.id === id)?.name ?? "—";
  const sortedAccs = sortAccounts(accs, qs, accSort);
  const sortedInsts = sortInstances(insts, instSort, accName);

  if (!accs.length) {
    return (
      <div className="card">
        <EmptyState title="欢迎使用 Harbormaster · 尚未添加阿里云账号" action={<Link className="btn primary" to="/settings">添加第一个阿里云账号</Link>}>
          绑定只读或开关机 RAM AccessKey 后，这里将实时汇总各账号 CDT 免费流量池、实例速率与账单健康状态。
        </EmptyState>
      </div>
    );
  }

  const running = insts.filter((i) => i.status === "running");
  const failing = accs.filter((a) => a.enabled && a.sync_status === "failing");
  const worstCase = insts.filter((i) => i.realtime_protection === "worst_case");
  const pendingConfirms = insts.filter((i) => i.pending_intent_confirmation);
  const activeSims = sims.data ?? [];

  const anyChannelConfigured =
    (telegram.data && telegram.data.status !== "not_configured") ||
    (email.data && email.data.status !== "not_configured") ||
    (webhook.data && webhook.data.status !== "not_configured");
  const channelFailing =
    telegram.data?.status === "failing" || email.data?.status === "failing" || webhook.data?.status === "failing";

  // 仅收集真正需要人工介入的事项，聚合进单一待办栏，杜绝满屏黄条轰炸
  const alerts: { key: string; critical?: boolean; text: React.ReactNode; to: string; cta: string }[] = [];
  for (const a of failing) {
    alerts.push({
      key: `fail-${a.id}`,
      critical: true,
      text: <>账号 <b>{a.name}</b> 同步失败（已连续 {a.consecutive_failures} 次）</>,
      to: "/settings",
      cta: "排查凭证",
    });
  }
  for (const i of worstCase) {
    alerts.push({
      key: `wc-${i.id}`,
      critical: true,
      text: <>实例 <b>{i.name}</b> 采集 Agent 掉线，当前按历史峰值保守推算流量</>,
      to: "/instances",
      cta: "查看实例",
    });
  }
  if (pendingConfirms.length > 0) {
    alerts.push({
      key: "confirm",
      text: <>检测到 <b>{pendingConfirms.map((x) => x.name).join("、")}</b> 在面板外被关机，请确认意图</>,
      to: "/automation",
      cta: "立即确认",
    });
  }
  if (activeSims.length > 0) {
    alerts.push({
      key: "sim",
      text: <>当前有 <b>{activeSims.length} 项模拟用量演练</b>正在生效（不会真实停机）</>,
      to: "/automation",
      cta: "管理演练",
    });
  }
  if (channelFailing) {
    alerts.push({
      key: "ch-fail",
      critical: true,
      text: <>有通知渠道投递失败，告警可能无法送达</>,
      to: "/automation?tab=channels",
      cta: "检查渠道",
    });
  } else if (telegram.data && email.data && webhook.data && !anyChannelConfigured) {
    alerts.push({
      key: "ch-empty",
      text: <>尚未配置任何外部通知渠道（Telegram / 邮件 / Webhook）</>,
      to: "/automation?tab=channels",
      cta: "去配置",
    });
  }

  const po = pool(qs, accs, "overseas");
  const pm = pool(qs, accs, "mainland");

  return (
    <>
      {alerts.length > 0 && (
        <div className={`attn-bar ${alerts.some((a) => a.critical) ? "critical" : ""}`} role="region" aria-label="待处理事项">
          {alerts.map((a) => (
            <div key={a.key} style={{ display: "flex", alignItems: "center", gap: 10, justifyContent: "space-between", flexWrap: "wrap" }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8, fontSize: "var(--fs-sm)" }}>
                <Badge level={a.critical ? "critical" : "warning"}>{a.critical ? "需处理" : "待关注"}</Badge>
                <span>{a.text}</span>
              </div>
              <Link className="btn sm" to={a.to}>
                {a.cta} <ChevronRight size={13} />
              </Link>
            </div>
          ))}
        </div>
      )}

      {/* 核心指标卡：自动隐藏未启用的国内/海外流量池，不展示无意义的空卡片与多余小字 */}
      <div className="kpi-grid">
        <KpiCard
          label="纳管阿里云账号"
          value={accs.filter((a) => a.enabled).length}
          unit={accs.length > accs.filter((a) => a.enabled).length ? `/ ${accs.length} 个` : "个"}
          icon={<ShieldIcon size={16} />}
          foot={failing.length ? <span style={{ color: "var(--critical)" }}>{failing.length} 个同步异常</span> : undefined}
        />
        <KpiCard
          label="运行中实例"
          value={running.length}
          unit={`/ ${insts.length} 台`}
          icon={<ServerIcon size={16} />}
          foot={
            insts.length - running.length > 0
              ? `在线 Agent ${insts.filter((i) => i.agent.status === "online").length} 台 · 已停止 ${insts.length - running.length} 台`
              : `在线 Agent ${insts.filter((i) => i.agent.status === "online").length} 台`
          }
        />
        {!po.disabledAll && (
          <KpiCard
            label="海外免费流量池剩余"
            value={po.left === null ? "—" : gb(po.left).replace(" GB", "")}
            unit={po.left === null ? undefined : "GB"}
            icon={<GlobeIcon size={16} />}
            foot={
              po.over
                ? <span style={{ color: "var(--critical)" }}>已超出 {gb(po.over)}</span>
                : po.left !== null
                  ? `总额度 ${gb(po.quota, 0)}`
                  : undefined
            }
          />
        )}
        {!pm.disabledAll && (
          <KpiCard
            label="国内免费流量池剩余"
            value={pm.left === null ? "—" : gb(pm.left).replace(" GB", "")}
            unit={pm.left === null ? undefined : "GB"}
            icon={<ActivityIcon size={16} />}
            foot={
              pm.over
                ? <span style={{ color: "var(--critical)" }}>已超出 {gb(pm.over)}</span>
                : pm.left !== null
                  ? `总额度 ${gb(pm.quota, 0)}`
                  : undefined
            }
          />
        )}
      </div>

      {/* 各账号配额卡片 */}
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
        <div className="inline" style={{ gap: 10 }}>
          <div className="sub" style={{ fontWeight: 600 }}>各账号本月免费流量进度</div>
          {accs.length > 1 && (
            <Select<AccountSortKey>
              size="sm"
              value={accSort}
              onChange={updateAccSort}
              ariaLabel="账号卡片排序"
              options={ACCOUNT_SORT_OPTIONS.map(([value, label]) => ({ value, label }))}
            />
          )}
        </div>
        {qs.some((q) => !quotaUnknown(q)) && <QuotaLegend worstCase={qs.some((q) => q.worst_case_bytes !== null && q.worst_case_bytes > 0)} />}
      </div>

      <div className="grid g2">
        {sortedAccs.map((a) => (
          <AccountCard
            key={a.id}
            account={a}
            quotas={qs.filter((q) => q.account_id === a.id)}
            instances={insts.filter((i) => i.account_id === a.id)}
          />
        ))}
      </div>

      {/* 下半屏 Bento：左「实例实时动态 + 共享带宽」，右「最近告警与通知」 */}
      <div className="grid g2">
        <Card
          title="实例运行速览"
          right={
            <div className="inline" style={{ gap: 8, flexWrap: "wrap" }}>
              {insts.length > 1 && (
                <div className="control-group sm">
                  <Select<InstanceSortKey>
                    size="sm"
                    value={instSort.key}
                    onChange={(key) => {
                      const defDir = INSTANCE_SORT_OPTIONS.find(([k]) => k === key)?.[2] ?? "asc";
                      updateInstSort(key, defDir);
                    }}
                    ariaLabel="总览实例排序字段"
                    options={INSTANCE_SORT_OPTIONS.map(([value, label]) => ({ value, label: `按${label}` }))}
                  />
                  <button
                    type="button"
                    className="sort-dir-btn"
                    onClick={() => updateInstSort(instSort.key, instSort.dir === "asc" ? "desc" : "asc")}
                    title={instSort.dir === "asc" ? "当前升序，点击切换为降序" : "当前降序，点击切换为升序"}
                    aria-label="切换总览实例升降序"
                  >
                    {instSort.dir === "asc" ? "↑" : "↓"}
                  </button>
                </div>
              )}
              <Link className="btn sm ghost" to="/instances">
                管理全部实例 <ChevronRight size={14} />
              </Link>
            </div>
          }
        >
          {insts.length === 0 ? (
            <EmptyState title="暂无纳管实例" />
          ) : (
            <div>
              {sortedInsts.slice(0, 6).map((i) => {
                const [sl, st] = INSTANCE_STATUS[i.status];
                const stopped = i.status === "stopped";
                const tx = stopped ? null : (i.realtime?.tx_bytes_per_second ?? (i.cloud_network?.streams?.[0]?.egress.value != null ? i.cloud_network.streams[0].egress.value / 8 : null));
                const rx = stopped ? null : (i.realtime?.rx_bytes_per_second ?? (i.cloud_network?.streams?.[0]?.ingress.value != null ? i.cloud_network.streams[0].ingress.value / 8 : null));
                return (
                  <div className="rowline" key={i.id}>
                    <div className="grow" style={{ minWidth: 150 }}>
                      <div className="inline" style={{ gap: 6 }}>
                        <Link to="/instances" style={{ fontWeight: 600, color: "var(--text)" }}>
                          {i.name}
                        </Link>
                        <Badge level={sl}>{st}</Badge>
                      </div>
                      <div className="sub" style={{ marginTop: 2 }}>
                        {accs.find((a) => a.id === i.account_id)?.name} · {regionName(i.region_id)}
                        {i.public_ips[0] ? ` · ${i.public_ips[0].address}` : ""}
                      </div>
                    </div>
                    <div className="num" style={{ textAlign: "right", fontSize: "var(--fs-sm)" }}>
                      {tx !== null ? (
                        <>
                          <div>↑ {rate(tx)}</div>
                          <div className="sub">↓ {rate(rx ?? 0)}</div>
                        </>
                      ) : (
                        <span className="sub">—</span>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          )}

          {!!packages.data?.length && (
            <div style={{ marginTop: 16, paddingTop: 14, borderTop: "1px solid var(--border)" }}>
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 6 }}>
                <span className="sub" style={{ fontWeight: 600 }}>共享带宽包今日主流量</span>
                <Link className="sub" to="/traffic">查看每小时明细 →</Link>
              </div>
              {packages.data.map((p) => (
                <div className="rowline" key={p.id} style={{ padding: "6px 0" }}>
                  <div className="grow">
                    <b>{p.name}</b>
                    <span className="sub"> · {regionName(p.region_id)}</span>
                  </div>
                  <b className="num">{p.today_metered_bytes === null ? "—" : gb(p.today_metered_bytes)}</b>
                </div>
              ))}
            </div>
          )}
        </Card>

        <Card
          title="最近告警与通知"
          right={
            <Link className="btn sm ghost" to="/automation?tab=history">
              查看全部 <ChevronRight size={14} />
            </Link>
          }
        >
          <Async res={notes}>
            {(n) =>
              n.items.length ? (
                <div className="alist">
                  {n.items.map((x) => (
                    <NotificationItem key={x.id} n={x} />
                  ))}
                </div>
              ) : (
                <EmptyState title="近期暂无告警与通知" />
              )
            }
          </Async>
        </Card>
      </div>
    </>
  );
}

function AccountCard({ account, quotas }: { account: Account; quotas: QuotaStatus[]; instances: Instance[] }) {
  let level: Level = "good";
  let text = "配额充裕";
  for (const q of quotas) {
    const [l, t] = quotaLevel(q);
    if (worse(l, level) !== level) [level, text] = [l, `${SCOPE_SHORT[q.scope]} · ${t}`];
  }
  if (!account.enabled) [level, text] = ["neutral", "已停用"];
  else if (account.sync_status === "failing") [level, text] = ["critical", "连接失败"];

  const order: QuotaScope[] = ["overseas", "mainland"];
  return (
    <Card
      title={account.name}
      right={
        <div className="inline" style={{ gap: 6 }}>
          <Badge level={level}>{text}</Badge>
          <Link className="btn sm ghost" to="/automation" title="配置额度保护自动停机">
            保护策略
          </Link>
        </div>
      }
    >
      {quotas.length ? (
        <>
          {order.map((s) => quotas.filter((q) => q.scope === s).map((q) => <QuotaMeter key={s} q={q} />))}
          <QuotaUnknownNote quotas={quotas} />
        </>
      ) : (
        <EmptyState title={account.sync_status === "never_synced" ? "正在首次同步…" : "暂无可展示配额"} />
      )}
    </Card>
  );
}
