// 总览：需要留意的问题、四个数字、各账号额度、共享带宽、最近通知。
import { api } from "../api/client";
import { useEvent } from "../api/events";
import { SCOPE_SHORT, SEVERITY, SITE, SUBJECT_KIND, regionName } from "../api/labels";
import type { Account, Instance, Notification, QuotaScope, QuotaStatus } from "../api/types";
import { quotaLevel, QuotaLegend, QuotaMeter } from "../components/QuotaMeter";
import { Async, Badge, Banner, Card, EmptyState, ErrorState, Fresh, Skeleton, type Level } from "../components/ui";
import { useShared } from "../lib/data";
import { gb, stamp } from "../lib/format";
import { Link } from "../lib/router";
import { useResource } from "../lib/useResource";
import { quotaPool as pool } from "../lib/usage";

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
  const notes = useResource(() => api.notifications({ limit: 4 }), []);
  const telegram = useResource(api.telegram, []);
  useEvent("notification.created", () => notes.reload());

  const firstError = accounts.error ?? instances.error ?? quotas.error;
  if (firstError && !accounts.data) return <div className="card"><ErrorState error={firstError} onRetry={() => { accounts.reload(); instances.reload(); quotas.reload(); }} /></div>;
  if (!accounts.data || !instances.data || !quotas.data)
    return (
      <>
        <div className="grid g4">{[0, 1, 2, 3].map((i) => <div className="card" key={i}><Skeleton lines={2} height={18} /></div>)}</div>
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
  const unprotected = insts.filter((i) => i.realtime_protection === "unprotected" && i.status === "running");
  const officialDown = qs.some((q) => q.official_status === "unavailable");
  const po = pool(qs, accs, "overseas"), pm = pool(qs, accs, "mainland");
  const tiles: [string, string, string, string][] = [
    ["阿里云账号", String(accs.length), "个", failing.length ? `${failing.length} 个连接失败` : "全部连接正常"],
    ["实例", String(insts.length), "台", `运行中 ${running} · 其他 ${insts.length - running}`],
    ["海外免费流量还剩", gb(po.left).replace(" GB", ""), po.left === null ? "" : "GB", po.left === null ? "部分用量未确认，暂不合计余量" : po.over ? `已超出 ${gb(po.over)}` : `共 ${gb(po.quota, 0)}${po.best ? ` · 剩得最多：${po.best}` : ""}`],
    ["国内免费流量还剩", gb(pm.left).replace(" GB", ""), pm.left === null ? "" : "GB", pm.left === null ? "部分用量未确认，暂不合计余量" : pm.over ? `已超出 ${gb(pm.over)}` : `共 ${gb(pm.quota, 0)}${pm.best ? ` · 剩得最多：${pm.best}` : ""}`],
  ];

  return (
    <>
      {(failing.length > 0 || worstCase.length > 0 || unprotected.length > 0 || officialDown || telegram.data?.status === "not_configured" || telegram.data?.status === "failing") && (
        <div className="grid" style={{ gap: 8 }} aria-label="需要留意">
          {worstCase.map((i) => (
            <Banner key={i.id} level="critical" action={<Link className="btn sm" to="/instances">查看</Link>}>
              <b>{i.name} 的采集程序掉线了。</b> 掉线期间按它之前的最高速度估算用量，所以数字会偏大。请登录这台机器检查 hm-agent 是否在运行。
            </Banner>
          ))}
          {failing.map((a) => (
            <Banner key={a.id} level="critical" action={<Link className="btn sm" to="/settings">去检查</Link>}>
              <b>账号 {a.name} 连不上阿里云</b>（已连续失败 {a.consecutive_failures} 次）。这个账号下的数据停在 {a.last_sync_at ? stamp(a.last_sync_at) : "很久以前"}，不再更新。
            </Banner>
          ))}
          {officialDown && (
            <Banner level="warning">
              <b>暂时取不到阿里云的官方流量数据。</b> 面板正在用上一次的官方数值加上实时增量继续估算。
            </Banner>
          )}
          {unprotected.length > 0 && (
            <Banner level="warning" action={<Link className="btn sm" to="/instances">查看实例</Link>}>
              <b>{unprotected.map((i) => i.name).join("、")} 未受实时保护。</b> 采集数据或官方计量依据尚未完整，请核对采集程序状态和流量数据。
            </Banner>
          )}
          {telegram.data?.status === "not_configured" && (
            <Banner level="warning" action={<Link className="btn sm" to="/notify">去设置</Link>}>
              <b>还没有设置通知。</b> 用量快到上限时不会有人提醒你。
            </Banner>
          )}
          {telegram.data?.status === "failing" && (
            <Banner level="critical" action={<Link className="btn sm" to="/notify">去检查</Link>}>
              <b>通知发不出去。</b> 最近一次发送 Telegram 消息失败了，告警可能收不到。
            </Banner>
          )}
        </div>
      )}

      <div className="grid g4">
        {tiles.map(([label, value, unit, hint]) => (
          <div className="card stat" key={label}>
            <div className="label">{label}</div>
            <div className="value">{value}<small>{unit}</small></div>
            <div className="hint">{hint}</div>
          </div>
        ))}
      </div>

      <div className="grid g2">
        {accs.map((a) => <AccountCard key={a.id} account={a} quotas={qs.filter((q) => q.account_id === a.id)} instances={insts.filter((i) => i.account_id === a.id)} />)}
      </div>

      <div className="grid g3">
        <Async res={packages} lines={5}>
          {(pkgs) => (
            <>
              {pkgs.map((p) => (
                <Card key={p.id} title={`共享带宽 ${p.name}`} right={<Fresh at={p.updated_at} kind="realtime" />}>
                  <dl className="kv">
                    <dt>账号 / 地域</dt><dd>{accs.find((a) => a.id === p.account_id)?.name} · {regionName(p.region_id)}</dd>
                    <dt>带宽上限</dt><dd>{p.bandwidth_mbps === null ? "待确认" : `${p.bandwidth_mbps} Mbps`}</dd>
                    <dt>计费方式</dt><dd>{SUBJECT_KIND[p.kind].replace("共享带宽 · ", "")}{p.settled_by_cdt === null ? " · CDT 归属待确认" : p.settled_by_cdt ? " · 计入免费额度" : ""}</dd>
                    <dt>成员</dt><dd>{p.members.map((m) => <span key={m.eip_address}><span className="mono">{m.eip_address}</span>{m.instance_name ? `（${m.instance_name}）` : ""} </span>)}</dd>
                    <dt>今天收 / 发</dt><dd>{p.today_rx_bytes === null ? "—" : gb(p.today_rx_bytes)} / {p.today_tx_bytes === null ? "—" : gb(p.today_tx_bytes)}</dd>
                    <dt>今天计费流量</dt><dd><b>{p.today_metered_bytes === null ? "—" : gb(p.today_metered_bytes)}</b>{p.dominant_direction ? ` · ${p.dominant_direction === "inbound" ? "收到的" : "发出的"}更多` : ""}</dd>
                  </dl>
                  {p.kind === "cbwp_dominant_traffic" && <p className="note">“按主流量”的意思：每小时比较收到和发出的流量，按较大的一方计费，不是只算发出的。</p>}
                </Card>
              ))}
            </>
          )}
        </Async>
        <Card className={packages.data?.length ? "span2" : "span2"} title="最近通知" right={<Link className="sub" to="/notify">全部</Link>}>
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
          <QuotaLegend worstCase={quotas.some((q) => q.worst_case_bytes !== null && q.worst_case_bytes > 0)} />
          {order.map((s) => quotas.filter((q) => q.scope === s).map((q) => <QuotaMeter key={s} q={q} />))}
        </>
      ) : (
        <EmptyState title="还没有流量数据">{account.sync_status === "never_synced" ? "正在第一次读取这个账号，通常一两分钟内完成。" : "这个账号暂时没有可显示的额度数据。"}</EmptyState>
      )}
    </Card>
  );
}
