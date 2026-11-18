// 设置：账号与权限测试、采集程序、通用、登录与版本。API Key、Passkey、备份等在后续里程碑加入。
import { useState, type FormEvent } from "react";
import { api, ApiError, downloadBackup } from "../api/client";
import { waitForJob } from "../api/jobs";
import { AGENT, API_SCOPE, errorText, RAM_LIMITATION, RAM_POLICY, regionName, SITE } from "../api/labels";
import type { Account, APIKey, APIKeyScope, Instance } from "../api/types";
import { AccountForm, PermissionList } from "../components/AccountForm";
import { Async, Badge, Card, Confirm, CopyButton, EmptyState, ErrorState, errMsg, Modal, SegToggle, Skeleton, Spinner, Switch, useToast } from "../components/ui";
import { useShared } from "../lib/data";
import { ago, duration, gb, setTimezone, stamp } from "../lib/format";
import { Link } from "../lib/router";
import { useNow, useResource } from "../lib/useResource";

const TIMEZONES = ["Asia/Shanghai", "Asia/Hong_Kong", "Asia/Singapore", "Asia/Tokyo", "UTC", "Europe/London", "America/Los_Angeles", "America/New_York"];

export function Settings({ onLogout }: { onLogout: () => void }) {
  const [tab, setTab] = useState<Tab>("accounts");
  return (
    <>
      <div><SegToggle label="设置分类" value={tab} onChange={setTab} options={TABS} /></div>
      {tab === "accounts" && <><Accounts /><RamPolicies /></>}
      {tab === "agents" && <Agents />}
      {tab === "access" && <><ApiKeys /><Backup /></>}
      {tab === "system" && (
        <div className="grid g2">
          <General />
          <About onLogout={onLogout} />
        </div>
      )}
    </>
  );
}

type Tab = "accounts" | "agents" | "access" | "system";
const TABS: [Tab, string][] = [["accounts", "账号"], ["agents", "采集程序"], ["access", "接口与备份"], ["system", "系统"]];

/** 权限策略模板：复制到阿里云 RAM 里自己授权，面板不会替你授权 */
function RamPolicies() {
  const [open, setOpen] = useState(false);
  const res = useResource(() => (open ? api.ramPolicies() : Promise.resolve(null)), [open]);
  return (
    <details className="card fold" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary><h2>权限策略模板</h2><span className="sub">给子账号授权时可以直接复制</span><div className="spacer" /><span className="sub">展开</span></summary>
      {open && (
        <div style={{ marginTop: 10 }}>
          <Async res={res}>
            {(items) => !items ? null : (
              <>
                {items.map((p) => (
                  <div className="rowline" key={p.id} style={{ alignItems: "flex-start" }}>
                    <div className="grow">
                      <b>{RAM_POLICY[p.id][0]}</b>
                      <div className="sub">{RAM_POLICY[p.id][1]}</div>
                    </div>
                    <CopyButton text={JSON.stringify(p.document, null, 2)} label="复制策略" />
                  </div>
                ))}
                <p className="note">这些是草稿。复制后请在阿里云 RAM 控制台里自己创建策略并授权，面板不会替你修改任何权限。目前已知的不足：</p>
                <ul className="sub" style={{ margin: "4px 0 0", paddingLeft: 18 }}>
                  {[...new Set(items.flatMap((p) => p.limitations))].map((l) => <li key={l}>{RAM_LIMITATION[l]}</li>)}
                </ul>
              </>
            )}
          </Async>
        </div>
      )}
    </details>
  );
}

function ApiKeys() {
  const toast = useToast();
  const res = useResource(api.apiKeys, []);
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<{ name: string; token: string }>();
  const [revoke, setRevoke] = useState<APIKey>();
  const [showRevoked, setShowRevoked] = useState(false);
  const now = useNow(60_000);
  return (
    <Card title="API Key" sub="给手机小组件或脚本用的访问凭证" right={<button className="btn primary" onClick={() => setCreating(true)}>＋ 创建</button>}>
      <Async res={res}>
        {(all) => {
          const active = all.filter((k) => !k.revoked_at), revoked = all.filter((k) => k.revoked_at);
          const row = (k: APIKey) => (
            <div className="rowline" key={k.id} style={k.revoked_at ? { opacity: 0.6 } : undefined}>
              <div className="grow">
                <b>{k.name}</b> {k.scopes.map((sc) => <span className="tag" key={sc} title={API_SCOPE[sc][1]} style={{ marginLeft: 4 }}>{API_SCOPE[sc][0]}</span>)}
                <div className="sub">{k.revoked_at ? `已于 ${stamp(k.revoked_at)} 撤销` : k.last_used_at ? `最近使用：${ago(k.last_used_at, now)}` : "还没有用过"}</div>
              </div>
              {!k.revoked_at && <button className="btn sm danger" onClick={() => setRevoke(k)}>撤销</button>}
            </div>
          );
          return (
            <>
              {!active.length ? <EmptyState title="还没有 API Key">需要让手机小组件或脚本读取用量时，在这里创建一个。</EmptyState> : active.map(row)}
              {revoked.length > 0 && <div style={{ marginTop: 8 }}><button className="linkbtn sub" onClick={() => setShowRevoked(!showRevoked)}>{showRevoked ? "收起" : `另有 ${revoked.length} 个已撤销`}</button></div>}
              {showRevoked && revoked.map(row)}
            </>
          );
        }}
      </Async>
      {creating && <ApiKeyForm onClose={() => setCreating(false)} onCreated={(name, token) => { setCreating(false); setCreated({ name, token }); res.reload(); }} />}
      {created && (
        <Modal title={`“${created.name}”已创建`} onClose={() => { /* 只能点下面的按钮关闭，避免误触后丢失凭证 */ }}>
          <p><b style={{ color: "var(--text)" }}>下面这串字符只显示这一次。</b>关掉这个窗口后就再也看不到了，请现在复制并保存好。</p>
          <div className="codebox"><code>{created.token}</code><CopyButton text={created.token} className="btn" /></div>
          <div className="actions"><button className="btn primary" onClick={() => setCreated(undefined)}>我已经保存好了</button></div>
        </Modal>
      )}
      {revoke && (
        <Confirm
          title={`撤销“${revoke.name}”？`} okText="撤销" danger onClose={() => setRevoke(undefined)}
          body="撤销后，用这个凭证的小组件或脚本会立刻失效，而且不能恢复。需要时只能重新创建一个。"
          onOk={async () => {
            try {
              await api.revokeApiKey(revoke.id);
              res.reload();
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

function ApiKeyForm({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string, token: string) => void }) {
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<APIKeyScope[]>(["summary:read"]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return setError("请填写名称。");
    if (!scopes.length) return setError("至少选一项权限。");
    setBusy(true);
    setError(undefined);
    try {
      const r = await api.createApiKey(name.trim(), scopes);
      onCreated(r.api_key.name, r.token);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title="创建 API Key" onClose={onClose}>
      <form className="stack" onSubmit={submit} noValidate>
        <div>
          <label className="lbl" htmlFor="key-name">名称</label>
          <input id="key-name" type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="比如：手机小组件" maxLength={80} autoComplete="off" autoFocus />
        </div>
        <div>
          <span className="lbl" style={{ display: "block", fontSize: "var(--fs-md)", color: "var(--text-2)", marginBottom: 4 }}>允许它做什么</span>
          {(Object.keys(API_SCOPE) as APIKeyScope[]).map((sc) => (
            <label key={sc} className="rowline" style={{ cursor: "pointer", flexWrap: "nowrap", alignItems: "flex-start" }}>
              <input type="checkbox" checked={scopes.includes(sc)} onChange={() => setScopes((v) => (v.includes(sc) ? v.filter((x) => x !== sc) : [...v, sc]))} style={{ width: "auto", marginTop: 4 }} />
              <span><b>{API_SCOPE[sc][0]}</b><span className="sub" style={{ display: "block" }}>{API_SCOPE[sc][1]}</span></span>
            </label>
          ))}
          <div className="field-help">创建后不能改权限；要改只能撤销后重新创建。</div>
        </div>
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="inline" style={{ justifyContent: "flex-end" }}>
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 创建</button>
        </div>
      </form>
    </Modal>
  );
}

function Backup() {
  const toast = useToast();
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const bytes = new TextEncoder().encode(pw).length;
    if (bytes < 12) return setError("备份密码至少要 12 位（一个汉字算 3 位）。");
    if (bytes > 1024) return setError("备份密码太长了。");
    if (pw !== pw2) return setError("两次输入的密码不一样。");
    setBusy(true);
    setError(undefined);
    try {
      const { blob, filename } = await downloadBackup(pw);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      a.click();
      URL.revokeObjectURL(url);
      setPw("");
      setPw2("");
      toast("备份已下载");
    } catch (err) {
      setError(err instanceof ApiError && err.code === "not_implemented" ? "演示模式下不能导出备份。" : errMsg(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="备份" sub="下载一份用密码加密的完整备份，可用于迁移到新机器">
      <form className="form" onSubmit={submit} noValidate>
        <label htmlFor="bk-pw">备份密码</label>
        <div>
          <input id="bk-pw" type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="new-password" />
          <div className="field-help">至少 12 位。不要用登录密码。面板不保存它，忘了就打不开这份备份。</div>
        </div>
        <label htmlFor="bk-pw2">再输入一次</label>
        <div><input id="bk-pw2" type="password" value={pw2} onChange={(e) => setPw2(e.target.value)} autoComplete="new-password" /></div>
        <span />
        <div>
          {error && <div className="field-error" role="alert" style={{ marginBottom: 8 }}>{error}</div>}
          <button className="btn primary" disabled={busy || !pw}>{busy && <Spinner />} 下载备份</button>
        </div>
      </form>
      <p className="note">恢复不在网页上做：把备份文件传到新机器，执行 <span className="mono">hm-server restore --input 备份文件 --data-dir 新的数据目录</span>。恢复后需要重新登录。</p>
    </Card>
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
    <Card title="阿里云账号" right={<button className="btn primary" onClick={() => setForm({})}>＋ 添加账号</button>}>
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
                  免费额度 海外 {gb(a.quota_overseas_bytes, 0)} + 国内 {gb(a.quota_mainland_bytes, 0)} · 用到 {a.alert_thresholds_percent.join(" / ")}% 时通知 · 扫描 {a.region_ids.map(regionName).join("、")}
                </div>
                <PermissionList check={a.permission_check} pending={checking.has(a.id)} />
              </div>
              <div className="inline">
                <button className="btn sm" onClick={() => check(a.id)} disabled={checking.has(a.id) || !a.enabled} title="只是试着读取，不会真的开关机或创建任何东西">重新测试权限</button>
                <button className="btn sm" onClick={() => setForm({ account: a })}>修改</button>
                <Switch on={a.enabled} label={`启用 ${a.name}`} onChange={(v) => (v ? toggle(a, true) : setDel(a))} />
              </div>
            </div>
          ))}
        </div>
      )}
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
    <Card title="采集程序" sub="装在实例上、上报公网流量的小程序" right={<Link className="btn" to="/instances">去实例页安装</Link>}>
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
    <Card title="时区">
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
              <p className="note" style={{ margin: 0 }}>
                官方流量数据每 {duration(s.collection.cdt_poll_interval_seconds)}读取一次（数据本身约晚 1 小时）；采集程序每 {duration(s.collection.agent_report_interval_seconds)}上报一次，{duration(s.collection.agent_offline_after_seconds)}没上报算掉线；实例每 {duration(s.collection.resource_scan_interval_seconds)}重新扫描一次。
              </p>
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
