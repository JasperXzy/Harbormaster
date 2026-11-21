// 设置：账号与权限测试、采集程序、登录安全（Passkey 与密码开关）、接口与备份（含小组件说明）、系统（时区、数据保留期、外部心跳、外观品牌）、审计日志。
import { useRef, useState, type FormEvent } from "react";
import { api, ApiError, downloadBackup, uploadBrandingLogo } from "../api/client";
import { waitForJob } from "../api/jobs";
import { AGENT, API_SCOPE, AUDIT_ACTOR, errorText, RAM_LIMITATION, RAM_POLICY, regionName, SITE } from "../api/labels";
import type { Account, APIKey, APIKeyScope, HeartbeatSettings, Instance, Passkey } from "../api/types";
import { AccountForm, PermissionList } from "../components/AccountForm";
import { Async, Badge, Card, Confirm, CopyButton, EmptyState, ErrorState, errMsg, Modal, SegToggle, Skeleton, Spinner, Switch, useToast, type Level } from "../components/ui";
import { useShared } from "../lib/data";
import { ago, duration, gb, setTimezone, stamp } from "../lib/format";
import { Link } from "../lib/router";
import { useNow, useResource } from "../lib/useResource";
import { createPasskeyCredential, passkeySupported } from "../lib/webauthn";

const TIMEZONES = ["Asia/Shanghai", "Asia/Hong_Kong", "Asia/Singapore", "Asia/Tokyo", "UTC", "Europe/London", "America/Los_Angeles", "America/New_York"];

type Tab = "accounts" | "agents" | "security" | "access" | "system" | "audit";
const TABS: [Tab, string][] = [
  ["accounts", "账号"],
  ["agents", "采集程序"],
  ["security", "登录安全"],
  ["access", "接口与备份"],
  ["system", "系统"],
  ["audit", "操作记录"],
];

export function Settings({ onLogout }: { onLogout: () => void }) {
  const [tab, setTab] = useState<Tab>("accounts");
  return (
    <>
      <div style={{ overflowX: "auto" }}><SegToggle label="设置分类" value={tab} onChange={setTab} options={TABS} /></div>
      {tab === "accounts" && <><Accounts /><RamPolicies /></>}
      {tab === "agents" && <Agents />}
      {tab === "security" && <SecurityTab onLogout={onLogout} />}
      {tab === "access" && <><ApiKeys /><WidgetSummaryGuide /><Backup /></>}
      {tab === "system" && (
        <>
          <div className="grid g2">
            <General />
            <RetentionCard />
          </div>
          <div className="grid g2">
            <HeartbeatCard />
            <BrandingCard />
          </div>
          <About onLogout={onLogout} />
        </>
      )}
      {tab === "audit" && <AuditLogCard />}
    </>
  );
}

/* ---------- 登录安全：Passkey 与关闭密码登录 ---------- */
function SecurityTab({ onLogout }: { onLogout: () => void }) {
  const toast = useToast();
  const now = useNow(60_000);
  const keys = useResource(api.passkeys, []);
  const methods = useResource(api.loginMethods, []);
  const [adding, setAdding] = useState(false);
  const [pkName, setPkName] = useState("");
  const [busy, setBusy] = useState(false);
  const [delKey, setDelKey] = useState<Passkey>();

  const register = async (e: FormEvent) => {
    e.preventDefault();
    if (!pkName.trim()) return toast("请给这把 Passkey 起个名字，例如 MacBook 指纹。", true);
    if (!passkeySupported()) return toast("当前浏览器不支持 Passkey。", true);
    setBusy(true);
    try {
      const begin = await api.beginPasskeyRegistration(pkName.trim());
      const cred = await createPasskeyCredential(begin.options);
      await api.finishPasskeyRegistration(begin.ceremony_id, cred);
      setPkName("");
      setAdding(false);
      keys.reload();
      methods.reload();
      toast("Passkey 已绑定");
    } catch (err) {
      if (err instanceof DOMException && err.name === "NotAllowedError") toast("已取消绑定", true);
      else toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  const togglePassword = async (enabled: boolean) => {
    try {
      await api.setPasswordLogin(enabled);
      methods.reload();
      toast(enabled ? "已恢复允许密码登录" : "已关闭密码登录，以后仅可使用 Passkey 登录");
    } catch (err) {
      toast(errMsg(err), true);
    }
  };

  return (
    <div className="grid g2">
      <Card title="Passkey 安全密钥" sub="用指纹、面容或硬件密钥免密登录，防钓鱼更安全" right={<button className="btn primary" onClick={() => setAdding(true)}>＋ 绑定 Passkey</button>}>
        <Async res={keys}>
          {(list) =>
            !list.length ? (
              <EmptyState title="还没有绑定 Passkey">通过正式 HTTPS 域名访问面板时，可绑定手机或电脑的指纹 / 面容登录。</EmptyState>
            ) : (
              <div>
                {list.map((k) => (
                  <div className="rowline" key={k.id}>
                    <div className="grow">
                      <b>{k.name}</b>
                      <div className="sub">添加于 {stamp(k.created_at)}{k.last_used_at ? ` · 最近使用 ${ago(k.last_used_at, now)}` : " · 尚未使用"}</div>
                    </div>
                    <button className="btn sm danger" onClick={() => setDelKey(k)}>移除</button>
                  </div>
                ))}
              </div>
            )
          }
        </Async>
        {adding && (
          <Modal title="绑定新的 Passkey" onClose={() => setAdding(false)}>
            <form className="stack" onSubmit={register}>
              <div>
                <label className="lbl" htmlFor="pk-name">设备或密钥名称</label>
                <input id="pk-name" type="text" value={pkName} onChange={(e) => setPkName(e.target.value)} placeholder="例如：iPhone 面容、MacBook 指纹" maxLength={80} autoFocus />
              </div>
              <div className="actions">
                <button type="button" className="btn" onClick={() => setAdding(false)}>取消</button>
                <button className="btn primary" disabled={busy}>{busy && <Spinner />} 开始验证并绑定</button>
              </div>
            </form>
          </Modal>
        )}
        {delKey && (
          <Confirm
            title={`移除“${delKey.name}”？`}
            okText="移除"
            danger
            body="移除后不能再用这把安全密钥登录。如果已关闭密码登录，系统会保留至少一把可用 Passkey 以防锁死。"
            onClose={() => setDelKey(undefined)}
            onOk={async () => {
              await api.deletePasskey(delKey.id);
              keys.reload();
              methods.reload();
              toast("已移除");
            }}
          />
        )}
      </Card>

      <Card title="登录方式与当前会话" sub="至少绑定一把当前域名下的 Passkey 后，可关闭密码登录">
        <Async res={methods}>
          {(m) => (
            <>
              <div className="rowline">
                <div className="grow">
                  <b>允许使用管理员密码登录</b>
                  <div className="sub">关闭后登录页不再接受密码；万一丢失密钥可在服务器执行 <span className="mono">hm-server reset-password</span> 恢复。</div>
                </div>
                <Switch on={m.password_enabled} label="允许使用管理员密码登录" onChange={togglePassword} />
              </div>
              <div className="rowline">
                <div className="grow"><b>退出当前登录</b><div className="sub">在公用电脑上操作完后请及时退出。</div></div>
                <button className="btn" onClick={onLogout}>退出登录</button>
              </div>
            </>
          )}
        </Async>
      </Card>
    </div>
  );
}

/* ---------- 小组件摘要说明 ---------- */
function WidgetSummaryGuide() {
  const origin = typeof location !== "undefined" ? location.origin : "https://example.com";
  const url = `${origin}/api/v1/integrations/summary`;
  const curl = `curl -H "Authorization: Bearer 你的API_Key" ${url}`;
  return (
    <details className="card fold">
      <summary><h2>手机小组件与状态栏接入说明</h2><span className="sub">只读概况接口，不受管理端 IP 白名单限制</span><div className="spacer" /><span className="sub">展开</span></summary>
      <div className="stack" style={{ marginTop: 12 }}>
        <p className="sub" style={{ margin: 0 }}>
          在上方创建一个仅勾选 <b>“读取概况”</b> 的 API Key，即可在 Scriptable、快捷指令、桌面状态栏或自建首页中安全轮询各账号免费流量余量与实例在线数。
        </p>
        <div>
          <div className="sub" style={{ marginBottom: 4 }}>请求示例</div>
          <div className="codebox"><code>{curl}</code><CopyButton text={curl} className="btn" /></div>
        </div>
      </div>
    </details>
  );
}

/* ---------- 数据保留期 ---------- */
function RetentionCard() {
  const toast = useToast();
  const res = useResource(api.settings, []);
  return (
    <Card title="历史数据保留时长" sub="到期自动清理旧样本，原始秒级流量在归档为小时数据后才会回收">
      <Async res={res} lines={4}>
        {(s) => <RetentionForm initial={s} onSaved={(ns) => { res.mutate(() => ns); toast("数据保留时长已保存"); }} />}
      </Async>
    </Card>
  );
}

function RetentionForm({ initial, onSaved }: { initial: import("../api/types").Settings; onSaved: (s: import("../api/types").Settings) => void }) {
  const toast = useToast();
  const r = initial.retention ?? { latency_probe_days: 30, agent_raw_days: 7, agent_hourly_months: 13, audit_log_days: 365, job_history_days: 90 };
  const [rawDays, setRawDays] = useState(String(r.agent_raw_days ?? 7));
  const [hourlyMonths, setHourlyMonths] = useState(String(r.agent_hourly_months ?? 13));
  const [probeDays, setProbeDays] = useState(String(r.latency_probe_days ?? 30));
  const [jobDays, setJobDays] = useState(String(r.job_history_days ?? 90));
  const [auditDays, setAuditDays] = useState(String(r.audit_log_days ?? 365));
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const saved = await api.updateSettings({
        retention: {
          agent_raw_days: Number(rawDays),
          agent_hourly_months: Number(hourlyMonths),
          latency_probe_days: Number(probeDays),
          job_history_days: Number(jobDays),
          audit_log_days: Number(auditDays),
        },
      });
      onSaved(saved);
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      <div className="grid g2">
        <div>
          <label className="lbl" htmlFor="rt-raw">原始秒级流量（1–90 天）</label>
          <input id="rt-raw" type="number" min={1} max={90} value={rawDays} onChange={(e) => setRawDays(e.target.value)} />
        </div>
        <div>
          <label className="lbl" htmlFor="rt-hr">按小时汇总流量（1–60 个月）</label>
          <input id="rt-hr" type="number" min={1} max={60} value={hourlyMonths} onChange={(e) => setHourlyMonths(e.target.value)} />
        </div>
        <div>
          <label className="lbl" htmlFor="rt-job">任务与通知历史（7–3650 天）</label>
          <input id="rt-job" type="number" min={7} max={3650} value={jobDays} onChange={(e) => setJobDays(e.target.value)} />
        </div>
        <div>
          <label className="lbl" htmlFor="rt-aud">审计日志（30–3650 天）</label>
          <input id="rt-aud" type="number" min={30} max={3650} value={auditDays} onChange={(e) => setAuditDays(e.target.value)} />
        </div>
      </div>
      <div>
        <label className="lbl" htmlFor="rt-prb">网络延迟探测记录（1–3650 天）</label>
        <input id="rt-prb" type="number" min={1} max={3650} value={probeDays} onChange={(e) => setProbeDays(e.target.value)} />
      </div>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button className="btn sm primary" disabled={busy}>{busy && <Spinner />} 保存保留策略</button>
      </div>
    </form>
  );
}

/* ---------- 外部心跳自监控 ---------- */
function heartbeatBadge(hb: HeartbeatSettings): [Level, string] {
  if (!hb.url_configured) return ["neutral", "未配置"];
  if (hb.last_error) return ["critical", "上报失败"];
  if (hb.last_success_at) return ["good", "上报正常"];
  return ["warning", "已保存，待上报"];
}

function HeartbeatCard() {
  const toast = useToast();
  const res = useResource(api.heartbeat, []);
  const badge = res.data ? heartbeatBadge(res.data) : null;
  return (
    <Card
      title="外部心跳自监控"
      sub="定时向 Uptime Kuma / Healthchecks 等外部地址发请求；万一面板自身宕机由外部提醒你"
      right={badge && <Badge level={badge[0]}>{badge[1]}</Badge>}
    >
      <Async res={res} lines={3}>
        {(hb) => <HeartbeatForm value={hb} onSaved={(nh) => res.mutate(() => nh)} onTested={() => { toast("心跳测试已送达"); res.reload(); }} />}
      </Async>
    </Card>
  );
}

function HeartbeatForm({ value, onSaved, onTested }: { value: HeartbeatSettings; onSaved: (v: HeartbeatSettings) => void; onTested: () => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [interval, setIntervalSecs] = useState(String(value.interval_seconds));
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const payload: { revision: number; enabled: boolean; interval_seconds: number; url?: string } = {
        revision: value.revision,
        enabled,
        interval_seconds: Math.max(15, Math.min(86400, Number(interval) || 60)),
      };
      if (url.trim() !== "") payload.url = url.trim();
      const saved = await api.updateHeartbeat(payload);
      setUrl("");
      onSaved(saved);
      toast("心跳监控设置已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    try {
      const { job_id } = await api.testHeartbeat();
      const job = await waitForJob(job_id);
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else onTested();
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setTesting(false);
    }
  };

  return (
    <form className="stack" onSubmit={save}>
      <div className="inline">
        <Switch on={enabled} label="开启外部心跳上报" onChange={setEnabled} />
        <span>每</span>
        <input type="number" min={15} max={86400} value={interval} onChange={(e) => setIntervalSecs(e.target.value)} style={{ width: 80, padding: "4px 8px" }} />
        <span>秒主动上报一次</span>
      </div>
      <div>
        <label className="lbl" htmlFor="hb-url">Push 地址（HTTPS，保存后加密存储且不再回显）</label>
        <input id="hb-url" type="text" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={value.url_configured ? "已加密保存；留空表示保持原地址不变" : "https://uptime.example.com/api/push/xxxx"} autoComplete="off" />
      </div>
      {value.last_success_at && <div className="sub">最近成功上报：{stamp(value.last_success_at)}</div>}
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        {value.url_configured && <button type="button" className="btn sm" disabled={testing || busy} onClick={runTest}>{testing && <Spinner />} 立即测试一次</button>}
        <button className="btn sm primary" disabled={busy || testing}>{busy && <Spinner />} 保存心跳设置</button>
      </div>
    </form>
  );
}

/* ---------- 面板外观品牌 ---------- */
function BrandingCard() {
  const toast = useToast();
  const { branding } = useShared();
  const fileRef = useRef<HTMLInputElement>(null);
  const [name, setName] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <Card title="面板名称与图标" sub="自定义左上角和浏览器标签页显示的名称与小图标">
      <Async res={branding} lines={3}>
        {(b) => {
          const curName = name ?? b.name;
          const saveName = async (e: FormEvent) => {
            e.preventDefault();
            if (!curName.trim()) return toast("名称不能为空", true);
            setBusy(true);
            try {
              const saved = await api.updateBrandingName(b.revision, curName.trim());
              branding.mutate(() => saved);
              setName(null);
              toast("面板名称已更新");
            } catch (err) {
              toast(errMsg(err), true);
            } finally {
              setBusy(false);
            }
          };
          const onPickLogo = async (file?: File) => {
            if (!file) return;
            setBusy(true);
            try {
              const saved = await uploadBrandingLogo(b.revision, file);
              branding.mutate(() => saved);
              toast("图标已上传");
            } catch (err) {
              toast(errMsg(err), true);
            } finally {
              setBusy(false);
              if (fileRef.current) fileRef.current.value = "";
            }
          };
          const clearLogo = async () => {
            setBusy(true);
            try {
              const saved = await api.deleteBrandingLogo(b.revision);
              branding.mutate(() => saved);
              toast("已恢复默认锚形图标");
            } catch (err) {
              toast(errMsg(err), true);
            } finally {
              setBusy(false);
            }
          };

          return (
            <form className="stack" onSubmit={saveName}>
              <div>
                <label className="lbl" htmlFor="br-name">显示名称（最多 40 字）</label>
                <div className="inline">
                  <input id="br-name" type="text" value={curName} maxLength={40} onChange={(e) => setName(e.target.value)} style={{ flex: 1 }} />
                  <button className="btn sm primary" disabled={busy || curName.trim() === b.name}>{busy && <Spinner />} 保存名称</button>
                </div>
              </div>
              <div className="inline">
                <input ref={fileRef} type="file" accept="image/png,image/jpeg" hidden onChange={(e) => onPickLogo(e.target.files?.[0])} />
                <button type="button" className="btn sm" disabled={busy} onClick={() => fileRef.current?.click()}>上传 PNG/JPEG 图标</button>
                {b.logo && <button type="button" className="btn sm danger" disabled={busy} onClick={clearLogo}>恢复默认图标</button>}
                <span className="sub">不超过 256 KB</span>
              </div>
            </form>
          );
        }}
      </Async>
    </Card>
  );
}

/* ---------- 审计日志 ---------- */
function AuditLogCard() {
  const { accounts } = useShared();
  const [actor, setActor] = useState("");
  const [accountId, setAccountId] = useState("");
  const [cursor, setCursor] = useState<string>();
  const res = useResource(() => api.auditLog({ actor: actor || undefined, account_id: accountId || undefined, cursor, limit: 40 }), [actor, accountId, cursor]);
  const accName = (id: string | null) => (id ? accounts.data?.find((a) => a.id === id)?.name ?? id : "—");

  return (
    <Card
      title="操作审计记录"
      sub="所有配置改动、开关机请求、API Key 与 Telegram 操作均留痕；密钥与密码永不写入日志"
      right={
        <div className="filters">
          <select value={actor} onChange={(e) => { setActor(e.target.value); setCursor(undefined); }} aria-label="按操作来源筛选">
            <option value="">全部来源</option>
            {(Object.keys(AUDIT_ACTOR) as (keyof typeof AUDIT_ACTOR)[]).map((k) => <option key={k} value={k}>{AUDIT_ACTOR[k]}</option>)}
          </select>
          {(accounts.data?.length ?? 0) > 0 && (
            <select value={accountId} onChange={(e) => { setAccountId(e.target.value); setCursor(undefined); }} aria-label="按账号筛选">
              <option value="">全部账号</option>
              {accounts.data!.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          )}
        </div>
      }
    >
      <Async res={res} lines={6}>
        {(p) =>
          !p.items.length ? <EmptyState title="没有符合条件的操作记录" /> : (
            <>
              <div className="table-wrap">
                <table>
                  <thead><tr><th>时间</th><th>操作人</th><th>摘要</th><th>账号</th><th>变更详情</th></tr></thead>
                  <tbody>
                    {p.items.map((a) => (
                      <tr key={a.id}>
                        <td>{stamp(a.created_at)}</td>
                        <td>
                          <span className="tag">{AUDIT_ACTOR[a.actor.kind] ?? a.actor.kind}</span>
                          {a.actor.id && <div className="sub mono">{a.actor.id}</div>}
                        </td>
                        <td className="wrap">
                          <b>{a.summary || a.action}</b>
                          <div className="sub mono">{a.target_type}:{a.target_id}</div>
                        </td>
                        <td>{accName(a.account_id)}</td>
                        <td className="wrap">
                          {a.details.credentials_changed && <Badge level="warning">凭证已更换</Badge>}
                          {a.details.before || a.details.after ? (
                            <div className="sub mono" style={{ marginTop: 2 }}>
                              {Object.keys({ ...(a.details.before ?? {}), ...(a.details.after ?? {}) }).map((k) => (
                                <div key={k}>{k}: {JSON.stringify(a.details.before?.[k])} → {JSON.stringify(a.details.after?.[k])}</div>
                              ))}
                            </div>
                          ) : a.details.changed_fields.length > 0 ? (
                            <span className="sub">字段：{a.details.changed_fields.join("、")}</span>
                          ) : (
                            !a.details.credentials_changed && <span className="sub">—</span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {(cursor || p.next_cursor) && (
                <div className="inline" style={{ marginTop: 12 }}>
                  {cursor && <button className="btn sm" onClick={() => setCursor(undefined)}>回到最新</button>}
                  {p.next_cursor && <button className="btn sm" onClick={() => setCursor(p.next_cursor!)}>查看更早记录</button>}
                </div>
              )}
            </>
          )
        }
      </Async>
    </Card>
  );
}

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
            await api.revokeApiKey(revoke.id);
            res.reload();
            toast("已撤销");
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
      <p className="note">恢复不在网页上做：把备份文件传到新机器，执行 <span className="mono">hm-server restore --input 备份文件 --data-dir 新的数据目录</span>。恢复后需要重新登录，且为防误操作，恢复后所有自动停机规则会默认转为试运行状态。</p>
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
                  {a.track_overseas !== false && a.track_mainland !== false
                    ? <>免费额度 海外 {gb(a.quota_overseas_bytes, 0)} + 国内 {gb(a.quota_mainland_bytes, 0)} · 用到 {a.alert_thresholds_percent.join(" / ")}% 时通知</>
                    : a.track_overseas !== false
                      ? <>免费额度 海外 {gb(a.quota_overseas_bytes, 0)}（不记录国内） · 用到 {a.alert_thresholds_percent.join(" / ")}% 时通知</>
                      : a.track_mainland !== false
                        ? <>免费额度 国内 {gb(a.quota_mainland_bytes, 0)}（不记录海外） · 用到 {a.alert_thresholds_percent.join(" / ")}% 时通知</>
                        : <>不记录 CDT 流量</>}{" "}
                  · 扫描 {a.region_ids.map(regionName).join("、")}
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
          body={<>停用后，面板不再读取这个账号的数据，也不再为它发告警，该账号下的自动化开关机规则也会同时停用。它名下 {del.instance_count} 台实例的记录和历史数据都会保留，随时可以重新打开。</>}
          onOk={async () => {
            await toggle(del, false);
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
    <Card title="采集程序" sub="装在实例上、上报公网流量的小程序" right={<Link className="btn" to="/instances">去实例页安装或升级</Link>}>
      {instances.error ? <ErrorState error={instances.error} onRetry={instances.reload} /> : !instances.data ? <Skeleton /> : !list.length ? (
        <EmptyState title="还没有实例装上采集程序">到实例页，点某台机器的“安装采集程序”，按提示在机器上执行一行命令即可。</EmptyState>
      ) : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>实例</th><th>状态</th><th className="only-wide">版本</th><th>最后上报</th><th className="only-wide">安装于</th><th></th></tr></thead>
            <tbody>
              {list.map((i) => {
                const [l, t] = AGENT[i.agent.status];
                const old = Boolean(i.agent.update_available || (info.data && i.agent.version && i.agent.version !== info.data.latest_agent_version));
                return (
                  <tr key={i.id}>
                    <td><b>{i.name}</b><div className="sub">{accounts.data?.find((a) => a.id === i.account_id)?.name}</div></td>
                    <td><Badge level={l}>{t}</Badge>{i.agent.offline_since && <div className="sub">已掉线 {duration((now - new Date(i.agent.offline_since).getTime()) / 1000)}</div>}</td>
                    <td className="only-wide">{i.agent.version ?? "—"}{old && <div className="sub" style={{ color: "var(--warning)" }}>可升级至 {info.data?.latest_agent_version}</div>}</td>
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
            await api.revokeAgentToken(revoke.id);
            instances.reload();
            toast("已撤销");
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
    <Card title="时区与采集周期">
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
                  <div className="field-help">面板上的时间、“今天”从几点算起、定时开关机与静默时段，都按这个时区。</div>
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
    <Card title="版本与退出">
      <div className="rowline">
        <div className="grow"><b>退出登录</b><div className="sub">在公用电脑上用完后记得退出。</div></div>
        <button className="btn" onClick={onLogout}>退出登录</button>
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
