// 费用：各账号的余额、低余额提醒阈值、本月账单、包年包月实例的到期与续费。
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { billTypeName, CHARGE, errorText, regionName, RENEWAL, SITE } from "../api/labels";
import type { Account, AccountFinance, AlertRule, IdleCandidate, Instance } from "../api/types";
import { Async, Badge, Card, EmptyState, ErrorState, errMsg, Skeleton, Spinner, useToast } from "../components/ui";
import { useShared } from "../lib/data";
import { stamp, ymd } from "../lib/format";
import { Link } from "../lib/router";
import { useNow, useResource } from "../lib/useResource";

const SYMBOL: Record<string, string> = { CNY: "¥", USD: "$" };
const money = (amount: string, currency: string) => `${SYMBOL[currency] ?? currency + " "}${amount}`;

export function Billing() {
  const { accounts, instances } = useShared();
  const rules = useResource(() => api.alertRules(false), []);
  const balanceRule = rules.data?.find((r) => r.key === "balance_low");

  if (accounts.error) return <div className="card"><ErrorState error={accounts.error} onRetry={accounts.reload} /></div>;
  if (!accounts.data) return <div className="card"><Skeleton lines={5} /></div>;
  if (!accounts.data.length)
    return (
      <div className="card">
        <EmptyState title="还没有费用数据" action={<Link className="btn primary" to="/settings">去添加账号</Link>}>添加阿里云账号后，这里会显示余额、本月账单和快到期的机器。</EmptyState>
      </div>
    );
  return (
    <>
      <div className="grid g2">
        {accounts.data.map((a) => (
          <FinanceCard
            key={a.id}
            account={a}
            balanceRule={balanceRule}
            onSavedRule={(nr) => rules.mutate((l) => l.map((x) => (x.key === nr.key ? nr : x)))}
          />
        ))}
      </div>
      <Renewals instances={instances.data?.items} accounts={accounts.data} />
      <InstanceMonthlyCostCard instances={instances.data?.items} accounts={accounts.data} />
      <IdleResourcesCard accounts={accounts.data} />
    </>
  );
}

function FinanceCard({ account, balanceRule, onSavedRule }: { account: Account; balanceRule?: AlertRule; onSavedRule: (r: AlertRule) => void }) {
  const res = useResource(() => api.finance(account.id), [account.id], { refreshMs: 5 * 60_000 });
  return (
    <Card title={account.name} sub={SITE[account.site]} right={res.data?.updated_at ? <span className="sub">更新于 {stamp(res.data.updated_at)}</span> : undefined}>
      <Async res={res} lines={5}>{(f) => <Finance f={f} account={account} balanceRule={balanceRule} onSavedRule={onSavedRule} />}</Async>
    </Card>
  );
}

function Finance({ f, account, balanceRule, onSavedRule }: { f: AccountFinance; account: Account; balanceRule?: AlertRule; onSavedRule: (r: AlertRule) => void }) {
  const toast = useToast();
  const bal = f.balance, bill = f.bill;
  const b = bal.data as { currency: string; available_amount: string; available_cash_amount: string } | null;
  const items = (bill.data as { items: import("../api/types").BillOverviewItem[] } | null)?.items;
  const currency = b?.currency ?? (account.site === "international" ? "USD" : "CNY");

  const existing = balanceRule?.params.balance_thresholds?.find((t) => t.account_id === account.id);
  const [threshold, setThreshold] = useState(existing?.amount ?? "");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setThreshold(existing?.amount ?? "");
  }, [existing?.amount]);

  const saveThreshold = async () => {
    if (!balanceRule) return;
    const trimmed = threshold.trim();
    if (trimmed && !/^\d+(\.\d{1,4})?$/.test(trimmed)) {
      toast("请填写有效的提醒金额（例如 50 或 100.00），留空表示取消该账号提醒。", true);
      return;
    }
    const others = (balanceRule.params.balance_thresholds ?? []).filter((t) => t.account_id !== account.id);
    const next = trimmed ? [...others, { account_id: account.id, currency, amount: trimmed }] : others;
    setBusy(true);
    try {
      const saved = await api.updateAlertRule("balance_low", { enabled: true, params: { balance_thresholds: next } });
      onSavedRule(saved);
      toast(trimmed ? `已设置低于 ${money(trimmed, currency)} 时提醒` : "已取消该账号的低余额提醒");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };

  const dirty = threshold.trim() !== (existing?.amount ?? "");

  return (
    <>
      <div className="stat">
        <div className="label">账户余额</div>
        {b ? (
          <>
            <div className="value">{money(b.available_cash_amount, b.currency)}</div>
            {b.available_amount !== b.available_cash_amount && <div className="hint">含信用额度共可用 {money(b.available_amount, b.currency)}</div>}
          </>
        ) : (
          <div className="hint" style={{ marginTop: 6 }}>{bal.status === "failing" ? <span className="field-error">没取到：{errorText(bal.last_error?.code ?? "internal_error")}</span> : "还没有读取"}</div>
        )}
      </div>

      {balanceRule && (
        <div className="inline sub" style={{ marginTop: 10 }}>
          <label htmlFor={`bal-th-${account.id}`}>低余额提醒阈值（{currency}）</label>
          <input
            id={`bal-th-${account.id}`}
            type="text"
            inputMode="decimal"
            placeholder="未设置"
            value={threshold}
            onChange={(e) => setThreshold(e.target.value)}
            style={{ width: 92, padding: "3px 8px" }}
          />
          {dirty && <button className="btn sm" disabled={busy} onClick={saveThreshold}>{busy && <Spinner />} 保存</button>}
        </div>
      )}

      <h3>本月账单（{f.billing_cycle}）</h3>
      {!items ? (
        <div className="sub">{bill.status === "failing" ? <span className="field-error">没取到：{errorText(bill.last_error?.code ?? "internal_error")}</span> : "还没有读取"}</div>
      ) : !items.length ? (
        <div className="sub">这个月还没有账单。</div>
      ) : (
        <div className="table-wrap">
          <table>
            <tbody>
              {items.map((it, k) => (
                <tr key={k}>
                  <td>{it.product_name}<span className="sub"> · {billTypeName(it.bill_type)}</span></td>
                  <td className="num">{money(it.pretax_amount, it.currency)}{Number(it.outstanding_amount) > 0 && <div className="sub">未付 {money(it.outstanding_amount, it.currency)}</div>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function Renewals({ instances, accounts }: { instances: Instance[] | undefined; accounts: Account[] }) {
  const now = useNow(60_000);
  if (!instances) return <div className="card"><Skeleton /></div>;
  const rows = instances.filter((i) => i.expires_at).sort((a, b) => a.expires_at!.localeCompare(b.expires_at!));
  return (
    <Card title="到期与续费" sub="包年包月的机器">
      {!rows.length ? <EmptyState title="没有包年包月的机器">按量付费和抢占式的机器不会到期，不在这里显示。</EmptyState> : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>实例</th><th>到期</th><th>续费方式</th></tr></thead>
            <tbody>
              {rows.map((i) => {
                const diffMs = Date.parse(i.expires_at!) - now;
                const expired = diffMs <= 0;
                const days = Math.ceil(diffMs / 86400_000);
                const r = i.renewal ? RENEWAL[i.renewal.status] : RENEWAL.unknown;
                const soon = !expired && days <= 7 && i.renewal?.status !== "auto_renewal";
                return (
                  <tr key={i.id}>
                    <td><b>{i.name}</b><div className="sub">{accounts.find((a) => a.id === i.account_id)?.name}</div></td>
                    <td>{ymd(i.expires_at!)}<div className="sub">{expired ? <Badge level="critical">已过期</Badge> : soon ? <Badge level="critical">还剩 {days} 天</Badge> : `还剩 ${days} 天`}</div></td>
                    <td><Badge level={r[0]}>{r[1]}</Badge></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

const IDLE_REASON_TEXT: Record<string, string> = {
  stopped_subscription: "包年包月机器已关机闲置，仍在计费",
  stopped_keep_charging: "按量机器普通停机（未节省停机），仍收计算与磁盘费",
  stopped_not_charge_exempt: "机器已停机但未进入免计费状态",
  unattached_cloud_disk: "未挂载的独立云盘仍在持续计费",
  disk_on_stopped_instances: "挂载在停机实例上的云盘仍在计费",
  unbound_eip: "未绑定实例的弹性公网 IP（可能产生闲置占用费）",
};

const IDLE_KIND_TEXT: Record<string, string> = {
  ecs: "ECS 实例",
  swas: "轻量服务器",
  disk: "云盘",
  eip: "弹性公网 IP",
};

function InstanceMonthlyCostCard({ instances, accounts }: { instances: Instance[] | undefined; accounts: Account[] }) {
  const [selectedId, setSelectedId] = useState<string>("");
  useEffect(() => {
    if (!selectedId && instances?.length) setSelectedId(instances[0].id);
  }, [instances, selectedId]);

  const finRes = useResource(
    () => (selectedId ? api.instanceFinance(selectedId) : Promise.resolve(null)),
    [selectedId],
  );

  if (!instances?.length) return null;
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;

  return (
    <Card
      title="单实例本月费用明细"
      sub="来自阿里云账单接口按实例归集的原始账单行（不捏造预估抵扣）"
      right={
        <select value={selectedId} onChange={(e) => setSelectedId(e.target.value)} aria-label="选择实例查看本月账单">
          {instances.map((i) => (
            <option key={i.id} value={i.id}>
              {i.name} · {accName(i.account_id)}
            </option>
          ))}
        </select>
      }
    >
      <Async res={finRes}>
        {(fin) => {
          if (!fin) return null;
          const rows = fin.bill.data?.items ?? [];
          if (fin.bill.status === "failing") {
            return <div className="field-error">读取失败：{errorText(fin.bill.last_error?.code ?? "internal_error")}</div>;
          }
          if (!rows.length) {
            return <EmptyState title={`本月（${fin.billing_cycle}）暂无该实例的独立账单行`}>包年包月在往月已付清、或处于免费额度内的按量项目可能不产生当月扣费行。</EmptyState>;
          }
          return (
            <div className="table-wrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>计费产品</th>
                    <th>账单类型</th>
                    <th className="num">原价</th>
                    <th className="num">折后应付</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((it, idx) => (
                    <tr key={idx}>
                      <td><b>{it.product_name || it.product_code}</b></td>
                      <td className="sub">{billTypeName(it.bill_type)}</td>
                      <td className="num sub">{money(it.pretax_amount, it.currency)}</td>
                      <td className="num"><b>{money(it.after_discount_amount, it.currency)}</b></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          );
        }}
      </Async>
    </Card>
  );
}

function IdleResourcesCard({ accounts }: { accounts: Account[] }) {
  const res = useResource(() => api.idleResources({ limit: 100 }), [], { refreshMs: 120_000 });
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;

  return (
    <Card title="闲置计费资源巡检" sub="自动识别未绑定 EIP、未挂载云盘、普通停机未省钱的按量 ECS 等仍在产生费用的资源">
      <Async res={res}>
        {(page) =>
          !page.items.length ? (
            <EmptyState title="当前未发现闲置计费资源">各账号下的弹性公网 IP、云盘与停机实例均无明显闲置浪费。</EmptyState>
          ) : (
            <div className="table-wrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>资源名称 / ID</th>
                    <th>类型与地域</th>
                    <th>计费方式</th>
                    <th>闲置原因</th>
                    <th>发现时间</th>
                  </tr>
                </thead>
                <tbody>
                  {page.items.map((c: IdleCandidate) => (
                    <tr key={c.id}>
                      <td>
                        <b>{c.name}</b>
                        <div className="sub">{accName(c.account_id)} · <span className="mono">{c.cloud_id}</span>{c.public_ip ? ` · ${c.public_ip}` : ""}{c.size_gib ? ` · ${c.size_gib} GiB` : ""}</div>
                      </td>
                      <td>
                        <Badge level="warning">{IDLE_KIND_TEXT[c.kind] ?? c.kind}</Badge>
                        <div className="sub">{regionName(c.region_id)}</div>
                      </td>
                      <td className="sub">{CHARGE[c.charge_type as Instance["charge_type"]] ?? c.charge_type}</td>
                      <td className="wrap">{IDLE_REASON_TEXT[c.reason] ?? c.reason}</td>
                      <td className="sub">{stamp(c.observed_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
    </Card>
  );
}
