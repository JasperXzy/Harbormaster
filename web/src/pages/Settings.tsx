// 设置：账号与权限测试、采集程序、通用、登录与版本。API Key、Passkey、备份等在后续里程碑加入。
import { useState } from "react";
import { api } from "../api/client";
import { waitForJob } from "../api/jobs";
import { AGENT, errorText, regionName, SITE } from "../api/labels";
import type { Account, Instance } from "../api/types";
import { AccountForm, PermissionList } from "../components/AccountForm";
import { Async, Badge, Card, Confirm, EmptyState, ErrorState, errMsg, Modal, Skeleton, Spinner, Switch, useToast } from "../components/ui";
import { useShared } from "../lib/data";
import { ago, duration, gb, setTimezone, stamp } from "../lib/format";
import { Link } from "../lib/router";
import { useNow, useResource } from "../lib/useResource";

const TIMEZONES = ["Asia/Shanghai", "Asia/Hong_Kong", "Asia/Singapore", "Asia/Tokyo", "UTC", "Europe/London", "America/Los_Angeles", "America/New_York"];

export function Settings({ onLogout }: { onLogout: () => void }) {
  return (
    <>
      <Accounts />
      <Agents />
      <div className="grid g2">
        <General />
        <About onLogout={onLogout} />
      </div>
    </>
  );
}

function Accounts() {
  const toast = useToast();
  const { accounts, reloadAll } = useShared();
  const [form, setForm] = useState<{ account?: Account } | null>(null);
  const [del, setDel] = useState<Account>();
  const [checking, setChecking] = useState<Set<string>>(new Set());

  const check = async (id: string, jobId?: string) => {
    setChecking((s) => new Set(s).add(id));
    try {
      const job = await waitForJob(jobId ?? (await api.checkPermissions(id)).job_id);
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setChecking((s) => { const n = new Set(s); n.delete(id); return n; });
      reloadAll();
    }
  };
  const toggle = async (a: Account, enabled: boolean) => {
    try {
      const saved = await api.updateAccount(a.id, { enabled });
      accounts.mutate((l) => l.map((x) => (x.id === saved.id ? saved : x)));
      toast(enabled ? `已恢复读取 ${a.name}` : `已停用 ${a.name}，不再读取它的数据`);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  return (
    <Card title="阿里云账号" sub="密钥加密保存，保存后不再显示" right={<button className="btn primary" onClick={() => setForm({})}>＋ 添加账号</button>}>
      {accounts.error ? <ErrorState error={accounts.error} onRetry={accounts.reload} /> : !accounts.data ? <Skeleton lines={4} /> : !accounts.data.length ? (
        <EmptyState title="还没有添加阿里云账号" action={<button className="btn primary" onClick={() => setForm({})}>添加第一个账号</button>}>添加后会自动发现账号下的机器，并开始读取流量用量。</EmptyState>
      ) : (
        <div>
          {accounts.data.map((a) => (
            <div className="rowline" key={a.id} style={{ alignItems: "flex-start" }}>
              <div className="grow" style={{ display: "grid", gap: 6 }}>
                <div className="inline">
                  <b>{a.name}</b>
                  <span className="sub">{SITE[a.site]} · <span className="mono">{a.access_key_id_masked}</span> · {a.instance_count} 台实例</span>
                  {!a.enabled ? <Badge level="neutral">已停用</Badge> : a.sync_status === "failing" ? <Badge level="critical">连接失败</Badge> : a.sync_status === "never_synced" ? <Badge level="neutral">正在第一次读取</Badge> : <Badge level="good">连接正常</Badge>}
                </div>
                {a.sync_status === "failing" && a.last_error && (
                  <div className="field-error" style={{ margin: 0 }}>{errorText(a.last_error.code)}已连续失败 {a.consecutive_failures} 次{a.last_sync_at ? `，数据停在 ${stamp(a.last_sync_at)}` : ""}。</div>
                )}
                <div className="sub">
                  免费额度：海外 {gb(a.quota_overseas_bytes, 0)} + 国内 {gb(a.quota_mainland_bytes, 0)} · 每月 {a.reset_day} 号重新计算 · 用到 {a.alert_thresholds_percent.join("% / ")}% 时通知
                </div>
                <div className="sub">扫描地域：{a.region_ids.map(regionName).join("、")}</div>
                <PermissionList check={a.permission_check} pending={checking.has(a.id)} />
              </div>
              <div className="inline">
                <button className="btn sm" onClick={() => check(a.id)} disabled={checking.has(a.id) || !a.enabled}>重新测试权限</button>
                <button className="btn sm" onClick={() => setForm({ account: a })}>修改</button>
                <Switch on={a.enabled} label={`启用 ${a.name}`} onChange={(v) => (v ? toggle(a, true) : setDel(a))} />
              </div>
            </div>
          ))}
        </div>
      )}
      <p className="note">权限测试只是试着读取，不会真的开关机或创建任何东西。</p>
      {form && (
        <Modal title={form.account ? `修改账号 ${form.account.name}` : "添加阿里云账号"} onClose={() => setForm(null)} wide>
          <AccountForm
            account={form.account} submitText={form.account ? "保存" : "添加并测试权限"} onCancel={() => setForm(null)}
            onSaved={(a, job) => { setForm(null); reloadAll(); toast(form.account ? "已保存" : `已添加 ${a.name}，正在测试权限`); if (job) check(a.id, job); }}
          />
        </Modal>
      )}
      {del && (
        <Confirm
          title={`停用账号 ${del.name}？`} okText="停用" onClose={() => setDel(undefined)}
          body={<>停用后，面板不再读取这个账号的数据，也不再为它发告警。它名下 {del.instance_count} 台实例的记录和历史数据都会保留，随时可以重新打开。这不会影响阿里云上的机器本身。</>}
          onOk={async () => {
            try {
              await toggle(del, false);
            } catch (e) {
              toast(errMsg(e), true);
              throw e;
            }
          }}
        />
      )}
    </Card>
  );
}

function Agents() {
  const toast = useToast();
  const now = useNow(5000);
  const { instances, accounts } = useShared();
  const info = useResource(api.systemInfo, []);
  const [revoke, setRevoke] = useState<Instance>();
  const list = (instances.data?.items ?? []).filter((i) => i.agent.status !== "not_installed");
  return (
    <Card title="采集程序" sub="装在实例上、负责上报公网流量的小程序（hm-agent）" right={<Link className="btn" to="/instances">去实例页安装</Link>}>
      {instances.error ? <ErrorState error={instances.error} onRetry={instances.reload} /> : !instances.data ? <Skeleton /> : !list.length ? (
        <EmptyState title="还没有实例装上采集程序">到实例页，点某台机器的“安装采集程序”，按提示在机器上执行一行命令即可。</EmptyState>
      ) : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>实例</th><th>状态</th><th className="only-wide">版本</th><th>最后上报</th><th className="only-wide">安装于</th><th></th></tr></thead>
            <tbody>
              {list.map((i) => {
                const [l, t] = AGENT[i.agent.status];
                const old = info.data && i.agent.version && i.agent.version !== info.data.latest_agent_version;
                return (
                  <tr key={i.id}>
                    <td><b>{i.name}</b><div className="sub">{accounts.data?.find((a) => a.id === i.account_id)?.name}</div></td>
                    <td><Badge level={l}>{t}</Badge>{i.agent.offline_since && <div className="sub">已掉线 {duration((now - new Date(i.agent.offline_since).getTime()) / 1000)}</div>}</td>
                    <td className="only-wide">{i.agent.version ?? "—"}{old && <div className="sub">有新版本 {info.data!.latest_agent_version}</div>}</td>
                    <td>{i.agent.last_report_at ? ago(i.agent.last_report_at, now) : "—"}</td>
                    <td className="only-wide"><span className="sub">{i.agent.registered_at ? stamp(i.agent.registered_at) : "—"}</span></td>
                    <td><button className="btn sm danger" onClick={() => setRevoke(i)}>撤销凭证</button></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {revoke && (
        <Confirm
          title={`撤销 ${revoke.name} 的采集程序凭证？`} okText="撤销" danger onClose={() => setRevoke(undefined)}
          body="撤销后，这台机器上的采集程序立刻无法再上报，这台机器会变回“未安装”。想恢复需要重新生成安装命令并在机器上再执行一次。怀疑凭证泄露时使用。"
          onOk={async () => {
            try {
              await api.revokeAgentToken(revoke.id);
              instances.reload();
              toast("已撤销");
            } catch (e) {
              toast(errMsg(e), true);
              throw e;
            }
          }}
        />
      )}
    </Card>
  );
}

function General() {
  const toast = useToast();
  const res = useResource(api.settings, []);
  const [saving, setSaving] = useState(false);
  const change = async (tz: string) => {
    setSaving(true);
    try {
      const s = await api.updateSettings({ business_timezone: tz });
      setTimezone(s.business_timezone);
      res.mutate(() => s);
      toast("时区已修改");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Card title="时间与数据">
      <Async res={res} lines={5}>
        {(s) => {
          const zones = TIMEZONES.includes(s.business_timezone) ? TIMEZONES : [s.business_timezone, ...TIMEZONES];
          return (
            <>
              <div className="form" style={{ marginBottom: 14 }}>
                <label htmlFor="set-tz">显示时间用的时区</label>
                <div>
                  <div className="inline">
                    <select id="set-tz" value={s.business_timezone} onChange={(e) => change(e.target.value)} disabled={saving}>
                      {zones.map((z) => <option key={z} value={z}>{z}</option>)}
                    </select>
                    {saving && <Spinner />}
                  </div>
                  <div className="field-help">面板上的时间、“今天”从几点算起，都按这个时区。</div>
                </div>
              </div>
              <dl className="kv">
                <dt>阿里云官方流量数据</dt><dd>每 {duration(s.collection.cdt_poll_interval_seconds)}取一次（数据本身约晚 1 小时）</dd>
                <dt>采集程序上报</dt><dd>每 {duration(s.collection.agent_report_interval_seconds)}一次</dd>
                <dt>多久没上报算掉线</dt><dd>{duration(s.collection.agent_offline_after_seconds)}</dd>
                <dt>重新扫描实例</dt><dd>每 {duration(s.collection.resource_scan_interval_seconds)}一次</dd>
              </dl>
              <p className="note">这些间隔由服务器决定，面板里暂时不能修改。</p>
            </>
          );
        }}
      </Async>
    </Card>
  );
}

function About({ onLogout }: { onLogout: () => void }) {
  const info = useResource(api.systemInfo, []);
  return (
    <Card title="登录与版本">
      <div className="rowline">
        <div className="grow"><b>退出登录</b><div className="sub">在公用电脑上用完后记得退出。</div></div>
        <button className="btn" onClick={onLogout}>退出登录</button>
      </div>
      <div className="rowline">
        <div className="grow"><b>忘记密码</b><div className="sub">网页上不能重置密码。请登录服务器执行 <span className="mono">hm-server reset-password</span>。</div></div>
      </div>
      <div style={{ marginTop: 12 }}>
        <Async res={info} lines={2}>
          {(v) => (
            <dl className="kv">
              <dt>当前版本</dt><dd>{v.version}<span className="sub">（{v.commit} · {v.built_at ? `${stamp(v.built_at)} 构建` : "构建时间未提供"}）</span></dd>
              <dt>采集程序最新版本</dt><dd>{v.latest_agent_version}</dd>
            </dl>
          )}
        </Async>
      </div>
    </Card>
  );
}
