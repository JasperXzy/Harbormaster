// 总览：需要留意的问题、四个数字、各账号额度、共享带宽、最近通知。
import { api } from "../api/client";
import { useEvent } from "../api/events";
import { SCOPE_SHORT, SEVERITY, SITE, regionName } from "../api/labels";
import type { Account, Instance, Notification, QuotaScope, QuotaStatus } from "../api/types";
import { quotaLevel, QuotaLegend, QuotaMeter, quotaUnknown, QuotaUnknownNote } from "../components/QuotaMeter";
import { Async, Badge, Banner, Card, EmptyState, ErrorState, Skeleton, type Level } from "../components/ui";
import { useShared } from "../lib/data";
import { gb, stamp } from "../lib/format";
import { Link } from "../lib/router";
import { useResource } from "../lib/useResource";
import { quotaView, quotaPool as pool } from "../lib/usage";

const ORDER: Level[] = ["good", "neutral", "warning", "serious", "critical"];
const worse = (a: Level, b: Level) => (ORDER.indexOf(a) >= ORDER.indexOf(b) ? a : b);

export function NotificationItem({ n }: { n: Notification }) {
  const [level, text] = SEVERITY[n.severity];
  return (
    <div className="aitem">
      <Badge level={n.kind === "recovery" ? "good" : level}>{n.kind === "recovery" ? "已恢复" : n.kind === "test" ? "测试" : text}</Badge>
      <div>
        <div className="t">{n.title}</div>
        <div className="d">{n.body}</div>
      </div>
      <div className="when">{stamp(n.created_at)}</div>
    </div>
  );
}

export function Overview() {
  const { accounts, instances, quotas } = useShared();
  const packages = useResource(api.bandwidthPackages, [], { refreshMs: 30_000 });
  const notes = useResource(() => api.notifications({ limit: 3 }), []);
  const telegram = useResource(api.telegram, []);
  const sims = useResource(api.quotaSimulations, [], { refreshMs: 30_000 });
  useEvent("notification.created", () => notes.reload());

  const firstError = accounts.error ?? instances.error ?? quotas.error;
  if (firstError && !accounts.data) return <div className="card"><ErrorState error={firstError} onRetry={() => { accounts.reload(); instances.reload(); quotas.reload(); }} /></div>;
  if (!accounts.data || !instances.data || !quotas.data)
    return (
      <>
        <div className="card"><Skeleton lines={2} height={18} /></div>
        <div className="grid g2">{[0, 1].map((i) => <div className="card" key={i}><Skeleton lines={5} /></div>)}</div>
      </>
    );

  const accs = accounts.data, insts = instances.data.items, qs = quotas.data;
  if (!accs.length)
    return (
      <div className="card">
        <EmptyState title="还没有添加阿里云账号" action={<Link className="btn primary" to="/settings">去添加账号</Link>}>
          添加账号后，这里会显示所有机器的状态和本月免费流量还剩多少。
        </EmptyState>
      </div>
    );

  const running = insts.filter((i) => i.status === "running").length;
  const failing = accs.filter((a) => a.sync_status === "failing");
  const worstCase = insts.filter((i) => i.realtime_protection === "worst_case");
  const unprotected = insts.filter((i) => i.realtime_protection === "unprotected" && i.status === "running" && i.agent.status === "not_installed");
  const pendingConfirms = insts.filter((i) => i.pending_intent_confirmation);
  const activeSims = sims.data ?? [];
  const trackedQuotas = qs.filter((q) => {
    const a = accs.find((x) => x.id === q.account_id);
    return a && a.enabled && (q.scope === "mainland" ? a.track_mainland !== false : a.track_overseas !== false);
  });
  const usageUnknown = trackedQuotas.some((q) => !quotaView(q).usable);
  const officialDown = trackedQuotas.some((q) => q.official_status === "unavailable");
  const po = pool(qs, accs, "overseas"), pm = pool(qs, accs, "mainland");
  const tiles: [string, string, string, string][] = [
    ["阿里云账号", String(accs.length), "个", failing.length ? `${failing.length} 个连接失败` : "全部连接正常"],
    ["实例", String(insts.length), "台", `运行中 ${running} · 其他 ${insts.length - running}`],
    [po.unverified ? "海外估算剩余" : "海外免费流量还剩", gb(po.left).replace(" GB", ""), po.left === null ? "" : "GB", po.disabledAll ? "未开启海外流量记录" : po.left === null ? "有账号的用量还没确认，先不合计" : po.over ? `已超出 ${gb(po.over)}` : `共 ${gb(po.quota, 0)}${po.best ? ` · 剩得最多：${po.best}` : ""}`],
    [pm.unverified ? "国内估算剩余" : "国内免费流量还剩", gb(pm.left).replace(" GB", ""), pm.left === null ? "" : "GB", pm.disabledAll ? "未开启国内流量记录" : pm.left === null ? "有账号的用量还没确认，先不合计" : pm.over ? `已超出 ${gb(pm.over)}` : `共 ${gb(pm.quota, 0)}${pm.best ? ` · 剩得最多：${pm.best}` : ""}`],
  ];

  return (
    <>
      {(activeSims.length > 0 || pendingConfirms.length > 0 || failing.length > 0 || worstCase.length > 0 || unprotected.length > 0 || usageUnknown || officialDown || telegram.data?.status === "not_configured" || telegram.data?.status === "failing") && (
        <div className="grid" style={{ gap: 8 }} aria-label="需要留意">
          {activeSims.length > 0 && (
            <Banner level="warning" action={<Link className="btn sm" to="/automation">管理模拟</Link>}>
              <b>当前有 {activeSims.length} 项模拟用量演练正在生效。</b> 模拟状态下只发送测试通知，不会真实关机。
            </Banner>
          )}
          {pendingConfirms.length > 0 && (
            <Banner level="warning" action={<Link className="btn sm" to="/automation">去确认</Link>}>
              <b>{pendingConfirms.map((i) => i.name).join("、")} 在面板外被关机。</b> 请确认是保持停止还是恢复运行。
            </Banner>
          )}
          {worstCase.map((i) => (
            <Banner key={i.id} level="critical" action={<Link className="btn sm" to="/instances">查看</Link>}>
              <b>{i.name} 的采集程序掉线了。</b> 掉线期间按它之前的最高速度估算，数字会偏大。
            </Banner>
          ))}
          {failing.map((a) => (
            <Banner key={a.id} level="critical" action={<Link className="btn sm" to="/settings">去检查</Link>}>
              <b>账号 {a.name} 连不上阿里云</b> 它的数据停在 {a.last_sync_at ? stamp(a.last_sync_at) : "很久以前"}，已连续失败 {a.consecutive_failures} 次。
            </Banner>
          ))}
          {officialDown && (
            <Banner level="warning">
              <b>暂时取不到阿里云的官方流量数据。</b> 正用上一次的数值继续估算。
            </Banner>
          )}
          {unprotected.length > 0 && (
            <Banner level="warning" action={<Link className="btn sm" to="/instances">去安装</Link>}>
              <b>{unprotected.map((i) => i.name).join("、")} 未受实时保护。</b> 没装采集程序，只有延迟的官方累计可参考。
            </Banner>
          )}
          {usageUnknown && (
            <Banner level="warning" action={<Link className="btn sm" to="/traffic">查看流量</Link>}>
              <b>部分范围的估算覆盖还不完整。</b> 可用范围继续显示估算，缺失范围保留官方累计。
            </Banner>
          )}
          {telegram.data?.status === "not_configured" && (
            <Banner level="warning" action={<Link className="btn sm" to="/notify">去设置</Link>}>
              <b>还没有设置通知。</b> 用量快到上限时不会有人提醒你。
            </Banner>
          )}
          {telegram.data?.status === "failing" && (
            <Banner level="critical" action={<Link className="btn sm" to="/notify">去检查</Link>}>
              <b>通知发不出去。</b> 告警可能收不到。
            </Banner>
          )}
        </div>
      )}

      <div className="card statstrip">
        {tiles.map(([label, value, unit, hint]) => (
          <div className="stat" key={label}>
            <div className="label">{label}</div>
            <div className="value">{value}<small>{unit}</small></div>
            <div className="hint">{hint}</div>
          </div>
        ))}
      </div>

      {qs.some((q) => !quotaUnknown(q)) && <QuotaLegend worstCase={qs.some((q) => q.worst_case_bytes !== null && q.worst_case_bytes > 0)} />}
      <div className="grid g2">
        {accs.map((a) => <AccountCard key={a.id} account={a} quotas={qs.filter((q) => q.account_id === a.id)} instances={insts.filter((i) => i.account_id === a.id)} />)}
      </div>

      <div className="grid g2">
        {!!packages.data?.length && (
          <Card title="共享带宽" sub="每小时按收、发中较大的一方计费" right={<Link className="sub" to="/traffic">详情</Link>}>
            {packages.data.map((p) => (
              <div className="rowline" key={p.id}>
                <div className="grow" style={{ minWidth: 0, flexBasis: 140 }}>
                  <b>{p.name}</b>
                  <div className="sub">{accs.find((a) => a.id === p.account_id)?.name} · {regionName(p.region_id)}{p.bandwidth_mbps === null ? "" : ` · ${p.bandwidth_mbps} Mbps`} · {p.members.map((m) => m.instance_name ?? m.eip_address).join("、")}</div>
                </div>
                <div style={{ textAlign: "right", marginLeft: "auto" }}>
                  <b className="num">{p.today_metered_bytes === null ? "—" : gb(p.today_metered_bytes)}</b>
                  <div className="sub num">今天 · 收 {p.today_rx_bytes === null ? "—" : gb(p.today_rx_bytes)} / 发 {p.today_tx_bytes === null ? "—" : gb(p.today_tx_bytes)}</div>
                </div>
              </div>
            ))}
          </Card>
        )}
        <Card className={packages.data?.length ? "" : "span2"} title="最近通知" right={<Link className="sub" to="/notify">全部</Link>}>
          <Async res={notes}>
            {(n) => (n.items.length ? <div className="alist">{n.items.map((x) => <NotificationItem key={x.id} n={x} />)}</div> : <EmptyState title="还没有通知">用量越过档位、采集程序掉线等情况发生时，会记录在这里。</EmptyState>)}
          </Async>
        </Card>
      </div>
    </>
  );
}

function AccountCard({ account, quotas, instances }: { account: Account; quotas: QuotaStatus[]; instances: Instance[] }) {
  let level: Level = "good", text = "正常";
  for (const q of quotas) {
    const [l, t] = quotaLevel(q);
    if (worse(l, level) !== level) [level, text] = [l, `${SCOPE_SHORT[q.scope]}${t}`];
  }
  if (account.sync_status === "failing") [level, text] = ["critical", "连接失败"];
  const order: QuotaScope[] = ["overseas", "mainland"];
  return (
    <Card title={account.name} sub={`${SITE[account.site]} · ${instances.length} 台实例`} right={<Badge level={level}>{text}</Badge>}>
      {quotas.length ? (
        <>
          {order.map((s) => quotas.filter((q) => q.scope === s).map((q) => <QuotaMeter key={s} q={q} />))}
          <QuotaUnknownNote quotas={quotas} />
        </>
      ) : (
        <EmptyState title="还没有流量数据">{account.track_mainland === false && account.track_overseas === false ? "已关闭该账号的国内与海外 CDT 流量记录。" : account.sync_status === "never_synced" ? "正在第一次读取这个账号，通常一两分钟内完成。" : "这个账号暂时没有可显示的额度数据。"}</EmptyState>
      )}
    </Card>
  );
}
