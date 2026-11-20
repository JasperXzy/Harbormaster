// 费用：各账号的余额、低余额提醒阈值、本月账单、包年包月实例的到期与续费。
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { billTypeName, errorText, RENEWAL, SITE } from "../api/labels";
import type { Account, AccountFinance, AlertRule, Instance } from "../api/types";
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
                const days = Math.ceil((Date.parse(i.expires_at!) - now) / 86400_000);
                const r = i.renewal ? RENEWAL[i.renewal.status] : RENEWAL.unknown;
                const soon = days <= 7 && i.renewal?.status !== "auto_renewal";
                return (
                  <tr key={i.id}>
                    <td><b>{i.name}</b><div className="sub">{accounts.find((a) => a.id === i.account_id)?.name}</div></td>
                    <td>{ymd(i.expires_at!)}<div className="sub">{days < 0 ? "已过期" : days === 0 ? "今天到期" : soon ? <Badge level="critical">还剩 {days} 天</Badge> : `还剩 ${days} 天`}</div></td>
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
