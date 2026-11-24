// 系统设置工作区：采用左侧分类导航栏 + 右侧全宽面板双栏布局，彻底消除宽屏大面积留白；统一承载云账号抽屉管理、采集程序、Passkey 安全、API/小组件/备份、系统与版本中心、以及后台任务与操作审计中心。
import { useRef, useState, type FormEvent } from "react";
import { api, ApiError, downloadBackup, uploadBrandingLogo } from "../api/client";
import { waitForJob } from "../api/jobs";
import { AGENT, API_SCOPE, AUDIT_ACTOR, errorText, JOB_STATUS, JOB_TRIGGER, JOB_TYPE, RAM_LIMITATION, RAM_POLICY, regionName, SITE } from "../api/labels";
import type { Account, APIKey, APIKeyScope, HeartbeatSettings, Instance, JobStatus, JobType, Passkey } from "../api/types";
import { AccountForm, PermissionList } from "../components/AccountForm";
import { ActivityIcon, GlobeIcon, MonitorIcon, PlusIcon, ServerIcon, ShieldIcon, ZapIcon } from "../components/icons";
import { Async, Badge, Card, Confirm, CopyButton, Drawer, EmptyState, ErrorState, errMsg, Modal, SegToggle, Select, Skeleton, Spinner, Switch, useToast, type Level } from "../components/ui";
import { useShared } from "../lib/data";
import { ago, duration, gb, setTimezone, stamp, ver } from "../lib/format";
import { Link, useQueryTab } from "../lib/router";
import { useNow, useResource } from "../lib/useResource";
import { createPasskeyCredential, passkeySupported } from "../lib/webauthn";

const TIMEZONES = ["Asia/Shanghai", "Asia/Hong_Kong", "Asia/Singapore", "Asia/Tokyo", "UTC", "Europe/London", "America/Los_Angeles", "America/New_York"];

const NAV_ITEMS = [
  { key: "accounts", label: "云账号与权限", icon: ServerIcon },
  { key: "agents", label: "采集程序 (Agent)", icon: ActivityIcon },
  { key: "security", label: "登录与安全", icon: ShieldIcon },
  { key: "access", label: "API、小组件与备份", icon: ZapIcon },
  { key: "system", label: "系统与版本中心", icon: GlobeIcon },
  { key: "activity", label: "任务与操作审计", icon: MonitorIcon },
] as const;

type SettingsTab = (typeof NAV_ITEMS)[number]["key"] | "audit";

export function Settings({ onLogout }: { onLogout: () => void }) {
  const [rawTab, setRawTab] = useQueryTab<SettingsTab>("accounts", ["accounts", "agents", "security", "access", "system", "activity", "audit"] as const);
  const tab = rawTab === "audit" ? "activity" : rawTab;

  return (
    <div className="split-layout">
      <nav className="split-nav" aria-label="设置分类导航">
        {NAV_ITEMS.map((item) => {
          const Icon = item.icon;
          return (
            <button key={item.key} type="button" className={tab === item.key ? "on" : ""} onClick={() => setRawTab(item.key)}>
              <Icon size={16} />
              <span>{item.label}</span>
            </button>
          );
        })}
      </nav>

      <div className="split-body">
        {tab === "accounts" && (
          <>
            <AccountsSection />
            <RamPoliciesCard />
          </>
        )}
        {tab === "agents" && <AgentsCard />}
        {tab === "security" && <SecuritySection onLogout={onLogout} />}
        {tab === "access" && (
          <>
            <ApiKeysCard />
            <div className="grid g2">
              <WidgetSummaryCard />
              <UrlCronCard />
            </div>
            <BackupCard />
          </>
        )}
        {tab === "system" && (
          <>
            <div className="grid g2">
              <GeneralCard />
              <RetentionCard />
            </div>
            <div className="grid g2">
              <HeartbeatCard />
              <BrandingCard />
            </div>
            <VersionCenterCard onLogout={onLogout} />
          </>
        )}
        {tab === "activity" && <ActivityCenter initialKind={rawTab === "audit" ? "audit" : "jobs"} />}
      </div>
    </div>
  );
}

/* ============================================================================
 * 1. 云账号与权限 (2-Column Bento Cards + Slide-over Account Drawer)
 * ========================================================================== */
function AccountsSection() {
  const toast = useToast();
  const { accounts, reloadAll } = useShared();
  const [drawer, setDrawer] = useState<{ account?: Account } | null>(null);
  const [del, setDel] = useState<Account>();
  const [checking, setChecking] = useState<Set<string>>(new Set());

  const check = async (id: string, jobId?: string) => {
    setChecking((s) => new Set(s).add(id));
    try {
      const job = await waitForJob(jobId ?? (await api.checkPermissions(id)).job_id);
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("权限检测已完成");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setChecking((s) => {
        const n = new Set(s);
        n.delete(id);
        return n;
      });
      reloadAll();
    }
  };

  const toggle = async (a: Account, enabled: boolean) => {
    try {
      const saved = await api.updateAccount(a.id, { enabled });
      accounts.mutate((l) => l.map((x) => (x.id === saved.id ? saved : x)));
      toast(enabled ? `已恢复同步 ${a.name}` : `已停用 ${a.name}`);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  return (
    <>
      <Card
        title="阿里云账号管理"
        sub="AccessKey 均使用服务端主密钥 AES-256-GCM 加密存储，支持按账号独立开关国内 / 海外流量池统计"
        right={
          <button className="btn sm primary" onClick={() => setDrawer({})}>
            <PlusIcon size={14} /> 添加云账号
          </button>
        }
      >
        {accounts.error ? (
          <ErrorState error={accounts.error} onRetry={accounts.reload} />
        ) : !accounts.data ? (
          <Skeleton lines={4} />
        ) : !accounts.data.length ? (
          <EmptyState
            title="尚未添加阿里云账号"
            action={
              <button className="btn primary" onClick={() => setDrawer({})}>
                <PlusIcon size={14} /> 添加首个云账号
              </button>
            }
          >
            添加 RAM 子账号 AccessKey 后将自动扫描所选地域下的 ECS、轻量应用服务器与 CDT 流量配额。
          </EmptyState>
        ) : (
          <div className={accounts.data.length > 1 ? "grid g2" : "stack"}>
            {accounts.data.map((a) => (
              <div key={a.id} className="channel-card" style={{ gap: 12 }}>
                <div className="channel-card-top">
                  <div className="channel-meta">
                    <div className="inline" style={{ gap: 8 }}>
                      <b style={{ fontSize: "var(--fs-lg)" }}>{a.name}</b>
                      {!a.enabled ? (
                        <Badge level="neutral">已停用</Badge>
                      ) : a.sync_status === "failing" ? (
                        <Badge level="critical">连接异常</Badge>
                      ) : a.sync_status === "never_synced" ? (
                        <Badge level="neutral">首次扫描中</Badge>
                      ) : (
                        <Badge level="good">同步正常</Badge>
                      )}
                      {a.cdt_confirmed ? (
                        <Badge level="good" title="已手动确认开通阿里云 CDT，按流量及共享带宽对象默认计入 CDT 额度">已确认 CDT</Badge>
                      ) : (
                        <Badge level="neutral" title="尚未在账号级手动确认 CDT 开通状态，可点「编辑配置」开启或在流量页按对象设置">CDT 待确认</Badge>
                      )}
                    </div>
                    <div className="sub" style={{ marginTop: 2 }}>
                      {SITE[a.site]} · <span className="mono">{a.access_key_id_masked}</span> · {a.instance_count} 台实例
                    </div>
                  </div>
                  <Switch on={a.enabled} label={`启用 ${a.name}`} onChange={(v) => (v ? toggle(a, true) : setDel(a))} />
                </div>

                {a.sync_status === "failing" && a.last_error && (
                  <div className="field-error" style={{ margin: 0 }}>
                    {errorText(a.last_error.code)}（连续失败 {a.consecutive_failures} 次{a.last_sync_at ? `，上次成功 ${stamp(a.last_sync_at)}` : ""}）
                  </div>
                )}

                <div className="sub">
                  <div>
                    {a.track_overseas !== false && a.track_mainland !== false
                      ? `免费额度：海外 ${gb(a.quota_overseas_bytes, 0)} + 国内 ${gb(a.quota_mainland_bytes, 0)}`
                      : a.track_overseas !== false
                        ? `免费额度：海外 ${gb(a.quota_overseas_bytes, 0)}（不统计国内）`
                        : a.track_mainland !== false
                          ? `免费额度：国内 ${gb(a.quota_mainland_bytes, 0)}（不统计海外）`
                          : "不统计 CDT 流量池"}
                    {" · "}告警档位 {a.alert_thresholds_percent.join(" / ")}%
                  </div>
                  <div style={{ marginTop: 3 }}>
                    扫描地域：{a.region_ids.map(regionName).join("、")}
                    {Object.keys(a.cdt_subject_overrides ?? {}).length > 0 && ` · ${Object.keys(a.cdt_subject_overrides).length} 项计费对象单独覆盖 CDT`}
                  </div>
                </div>

                <PermissionList check={a.permission_check} pending={checking.has(a.id)} />

                <div className="channel-foot">
                  <button
                    className="btn sm ghost"
                    onClick={() => check(a.id)}
                    disabled={checking.has(a.id) || !a.enabled}
                    title="仅执行只读探测，不会执行任何开关机或资源创建"
                  >
                    {checking.has(a.id) && <Spinner />} 重新检测权限
                  </button>
                  <button className="btn sm" onClick={() => setDrawer({ account: a })}>编辑配置</button>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      {drawer && (
        <Drawer
          wide
          title={drawer.account ? `编辑云账号：${drawer.account.name}` : "添加阿里云账号"}
          onClose={() => setDrawer(null)}
        >
          <AccountForm
            account={drawer.account}
            submitText={drawer.account ? "保存更改" : "添加并检测权限"}
            onCancel={() => setDrawer(null)}
            onSaved={(a, job) => {
              setDrawer(null);
              reloadAll();
              toast(drawer.account ? "账号设置已保存" : `已添加 ${a.name}，正在后台检测权限`);
              if (job) check(a.id, job);
            }}
          />
        </Drawer>
      )}

      {del && (
        <Confirm
          title={`停用账号 ${del.name}？`}
          okText="确认停用"
          onClose={() => setDel(undefined)}
          body={
            <>
              停用后将暂停同步该账号数据与告警，其关联的自动停开机策略也会暂停。名下 {del.instance_count} 台实例及历史流量记录均会完整保留，可随时重新开启。
            </>
          }
          onOk={async () => {
            await toggle(del, false);
          }}
        />
      )}
    </>
  );
}

function RamPoliciesCard() {
  const res = useResource(api.ramPolicies, []);
  return (
    <Card title="阿里云 RAM 权限策略模板" sub="在阿里云 RAM 控制台创建自定义权限策略时可直接一键复制">
      <Async res={res} lines={3}>
        {(items) => (
          <>
            <div className="grid g2">
              {items.map((p) => (
                <div key={p.id} className="card compact" style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
                  <div>
                    <b>{RAM_POLICY[p.id][0]}</b>
                    <div className="sub" style={{ marginTop: 2 }}>{RAM_POLICY[p.id][1]}</div>
                  </div>
                  <CopyButton text={JSON.stringify(p.document, null, 2)} label="复制 JSON" />
                </div>
              ))}
            </div>
            <ul className="sub" style={{ margin: "12px 0 0", paddingLeft: 18 }}>
              {[...new Set(items.flatMap((p) => p.limitations))].map((l) => (
                <li key={l}>{RAM_LIMITATION[l]}</li>
              ))}
            </ul>
          </>
        )}
      </Async>
    </Card>
  );
}

/* ============================================================================
 * 2. 采集程序 (Fleet Agents)
 * ========================================================================== */
function AgentsCard() {
  const toast = useToast();
  const now = useNow(5000);
  const { instances, accounts } = useShared();
  const info = useResource(api.systemInfo, []);
  const [revoke, setRevoke] = useState<Instance>();
  const list = (instances.data?.items ?? []).filter((i) => i.agent.status !== "not_installed");

  return (
    <Card
      title="hm-agent 实例采集程序"
      sub="每 10 秒上报一次网卡收发计数并执行主动网络探测；不开放任何监听端口"
      right={<Link className="btn sm primary" to="/instances">前往实例页安装 / 升级</Link>}
    >
      {instances.error ? (
        <ErrorState error={instances.error} onRetry={instances.reload} />
      ) : !instances.data ? (
        <Skeleton lines={4} />
      ) : !list.length ? (
        <EmptyState title="尚未在任何实例安装采集程序" action={<Link className="btn primary" to="/instances">去实例页生成安装命令</Link>} />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>实例名称</th>
                <th>运行状态</th>
                <th>程序版本</th>
                <th>最近心跳</th>
                <th className="only-wide">首次注册时间</th>
                <th className="num">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.map((i) => {
                const [l, t] = AGENT[i.agent.status];
                const curVer = i.agent.version ? ver(i.agent.version) : null;
                const latestVer = info.data?.latest_agent_version ? ver(info.data.latest_agent_version) : null;
                const old = Boolean(i.agent.update_available || (curVer && latestVer && curVer !== latestVer));
                return (
                  <tr key={i.id}>
                    <td>
                      <b>{i.name}</b>
                      <div className="sub">{accounts.data?.find((a) => a.id === i.account_id)?.name}</div>
                    </td>
                    <td>
                      <Badge level={l}>{t}</Badge>
                      {i.agent.offline_since && <div className="sub">失联 {duration((now - new Date(i.agent.offline_since).getTime()) / 1000)}</div>}
                    </td>
                    <td className="mono">
                      {curVer ?? "—"}
                      {old && latestVer && <div className="sub" style={{ color: "var(--warning)" }}>可升级至 {latestVer}</div>}
                    </td>
                    <td>{i.agent.last_report_at ? ago(i.agent.last_report_at, now) : "—"}</td>
                    <td className="only-wide sub">{i.agent.registered_at ? stamp(i.agent.registered_at) : "—"}</td>
                    <td className="num">
                      <button className="btn sm danger" onClick={() => setRevoke(i)}>撤销凭证</button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {revoke && (
        <Confirm
          title={`撤销 ${revoke.name} 的采集凭证？`}
          okText="确认撤销"
          danger
          onClose={() => setRevoke(undefined)}
          body="撤销后该实例上的 hm-agent 将立即停止上报并恢复为“未安装”状态。需要恢复时重新生成安装命令即可。"
          onOk={async () => {
            await api.revokeAgentToken(revoke.id);
            instances.reload();
            toast("采集凭证已撤销");
          }}
        />
      )}
    </Card>
  );
}

/* ============================================================================
 * 3. 登录与安全 (Passkeys & Password Toggle)
 * ========================================================================== */
function SecuritySection({ onLogout }: { onLogout: () => void }) {
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
    if (!pkName.trim()) return toast("请填写安全密钥名称，如 MacBook 指纹", true);
    if (!passkeySupported()) return toast("当前浏览器不支持 WebAuthn Passkey", true);
    setBusy(true);
    try {
      const begin = await api.beginPasskeyRegistration(pkName.trim());
      const cred = await createPasskeyCredential(begin.options);
      await api.finishPasskeyRegistration(begin.ceremony_id, cred);
      setPkName("");
      setAdding(false);
      keys.reload();
      methods.reload();
      toast("Passkey 已成功绑定");
    } catch (err) {
      if (err instanceof DOMException && err.name === "NotAllowedError") toast("已取消验证", true);
      else toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  const togglePassword = async (enabled: boolean) => {
    try {
      await api.setPasswordLogin(enabled);
      methods.reload();
      toast(enabled ? "已允许密码登录" : "已关闭密码登录，仅限 Passkey 登录");
    } catch (err) {
      toast(errMsg(err), true);
    }
  };

  return (
    <div className="grid g2">
      <Card
        title="Passkey 通行密钥"
        sub="使用 Touch ID / Face ID 或硬件安全密钥免密抗钓鱼登录"
        right={
          <button className="btn sm primary" onClick={() => setAdding(true)}>
            <PlusIcon size={13} /> 绑定 Passkey
          </button>
        }
      >
        <Async res={keys}>
          {(list) =>
            !list.length ? (
              <EmptyState title="尚未绑定 Passkey" />
            ) : (
              <div>
                {list.map((k) => (
                  <div className="rowline" key={k.id}>
                    <div className="grow">
                      <b>{k.name}</b>
                      <div className="sub">
                        绑定于 {stamp(k.created_at)}
                        {k.last_used_at ? ` · 最近使用 ${ago(k.last_used_at, now)}` : " · 尚未使用"}
                      </div>
                    </div>
                    <button className="btn sm danger" onClick={() => setDelKey(k)}>移除</button>
                  </div>
                ))}
              </div>
            )
          }
        </Async>
        {adding && (
          <Modal title="绑定新 Passkey" onClose={() => setAdding(false)}>
            <form className="stack" onSubmit={register}>
              <div>
                <label className="lbl" htmlFor="pk-name">设备或密钥标识</label>
                <input id="pk-name" type="text" value={pkName} onChange={(e) => setPkName(e.target.value)} placeholder="如：MacBook Touch ID / iPhone Face ID" maxLength={80} autoFocus />
              </div>
              <div className="actions">
                <button type="button" className="btn" onClick={() => setAdding(false)}>取消</button>
                <button className="btn primary" disabled={busy}>{busy && <Spinner />} 验证并绑定</button>
              </div>
            </form>
          </Modal>
        )}
        {delKey && (
          <Confirm
            title={`移除「${delKey.name}」？`}
            okText="确认移除"
            danger
            body="移除后无法再使用该安全密钥登录。若已禁用密码登录，系统将确保至少保留一把可用 Passkey 防止锁死。"
            onClose={() => setDelKey(undefined)}
            onOk={async () => {
              await api.deletePasskey(delKey.id);
              keys.reload();
              methods.reload();
              toast("Passkey 已移除");
            }}
          />
        )}
      </Card>

      <Card title="登录方式与当前会话" sub="绑定至少一把当前域名下的 Passkey 后可禁用传统密码登录">
        <Async res={methods}>
          {(m) => (
            <>
              <div className="rowline">
                <div className="grow">
                  <b>允许管理员密码登录</b>
                  <div className="sub">
                    禁用后登录页仅接受 Passkey；应急情况下可在服务器终端执行 <span className="mono">hm-server reset-password</span> 恢复。
                  </div>
                </div>
                <Switch on={m.password_enabled} label="允许使用管理员密码登录" onChange={togglePassword} />
              </div>
              <div className="rowline">
                <div className="grow">
                  <b>当前浏览器会话</b>
                </div>
                <button className="btn sm" onClick={onLogout}>退出登录</button>
              </div>
            </>
          )}
        </Async>
      </Card>
    </div>
  );
}

/* ============================================================================
 * 4. API、小组件与加密备份
 * ========================================================================== */
function ApiKeysCard() {
  const toast = useToast();
  const res = useResource(api.apiKeys, []);
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<{ name: string; token: string }>();
  const [revoke, setRevoke] = useState<APIKey>();
  const [showRevoked, setShowRevoked] = useState(false);
  const now = useNow(60_000);

  return (
    <Card
      title="API 访问凭证"
      sub="供手机小组件、外部状态栏或定时触发器使用的最小权限 Bearer Token"
      right={
        <button className="btn sm primary" onClick={() => setCreating(true)}>
          <PlusIcon size={13} /> 创建 API Key
        </button>
      }
    >
      <Async res={res}>
        {(all) => {
          const active = all.filter((k) => !k.revoked_at);
          const revoked = all.filter((k) => k.revoked_at);
          const row = (k: APIKey) => (
            <div className="rowline" key={k.id} style={k.revoked_at ? { opacity: 0.55 } : undefined}>
              <div className="grow">
                <b>{k.name}</b>{" "}
                {k.scopes.map((sc) => (
                  <span className="tag" key={sc} title={API_SCOPE[sc][1]} style={{ marginLeft: 4 }}>{API_SCOPE[sc][0]}</span>
                ))}
                <div className="sub">
                  {k.revoked_at ? `已于 ${stamp(k.revoked_at)} 撤销` : k.last_used_at ? `最近调用：${ago(k.last_used_at, now)}` : "尚未调用"}
                </div>
              </div>
              {!k.revoked_at && <button className="btn sm danger" onClick={() => setRevoke(k)}>撤销</button>}
            </div>
          );
          return (
            <>
              {!active.length ? <EmptyState title="暂无有效 API Key" /> : active.map(row)}
              {revoked.length > 0 && (
                <div style={{ marginTop: 8 }}>
                  <button className="linkbtn sub" onClick={() => setShowRevoked(!showRevoked)}>
                    {showRevoked ? "收起已撤销记录" : `查看 ${revoked.length} 个已撤销凭证`}
                  </button>
                </div>
              )}
              {showRevoked && revoked.map(row)}
            </>
          );
        }}
      </Async>

      {creating && (
        <ApiKeyForm
          onClose={() => setCreating(false)}
          onCreated={(name, token) => {
            setCreating(false);
            setCreated({ name, token });
            res.reload();
          }}
        />
      )}
      {created && (
        <Modal title={`API Key「${created.name}」已生成`} onClose={() => {}}>
          <p><b style={{ color: "var(--text)" }}>此完整密钥仅显示一次</b>，关闭窗口后无法再次查看，请立即复制保存。</p>
          <div className="codebox">
            <code>{created.token}</code>
            <CopyButton text={created.token} className="btn" />
          </div>
          <div className="actions">
            <button className="btn primary" onClick={() => setCreated(undefined)}>我已妥善保存</button>
          </div>
        </Modal>
      )}
      {revoke && (
        <Confirm
          title={`撤销「${revoke.name}」？`}
          okText="确认撤销"
          danger
          onClose={() => setRevoke(undefined)}
          body="撤销后使用该 Token 的小组件或外部脚本将立即失效且不可恢复。"
          onOk={async () => {
            await api.revokeApiKey(revoke.id);
            res.reload();
            toast("API Key 已撤销");
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
    if (!name.trim()) return setError("请填写凭证名称。");
    if (!scopes.length) return setError("请至少勾选一项权限范围。");
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
          <label className="lbl" htmlFor="key-name">凭证名称</label>
          <input id="key-name" type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="如：iOS 桌面小组件" maxLength={80} autoComplete="off" autoFocus />
        </div>
        <div>
          <span className="lbl">授权范围</span>
          {(Object.keys(API_SCOPE) as APIKeyScope[]).map((sc) => (
            <label key={sc} className="rowline" style={{ cursor: "pointer", flexWrap: "nowrap", alignItems: "flex-start" }}>
              <input
                type="checkbox"
                checked={scopes.includes(sc)}
                onChange={() => setScopes((v) => (v.includes(sc) ? v.filter((x) => x !== sc) : [...v, sc]))}
                style={{ width: "auto", marginTop: 4 }}
              />
              <span>
                <b>{API_SCOPE[sc][0]}</b>
                <span className="sub" style={{ display: "block" }}>{API_SCOPE[sc][1]}</span>
              </span>
            </label>
          ))}
        </div>
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 生成密钥</button>
        </div>
      </form>
    </Modal>
  );
}

function WidgetSummaryCard() {
  const origin = typeof location !== "undefined" ? location.origin : "https://example.com";
  const curl = `curl -H "Authorization: Bearer <API_KEY>" ${origin}/api/v1/integrations/summary`;
  return (
    <Card title="手机小组件与状态栏接口">
      <div className="codebox">
        <code>{curl}</code>
        <CopyButton text={curl} className="btn" />
      </div>
    </Card>
  );
}

function UrlCronCard() {
  const origin = typeof location !== "undefined" ? location.origin : "https://example.com";
  const curl = `curl -X POST -H "Authorization: Bearer <API_KEY>" ${origin}/api/v1/integrations/cron/run`;
  return (
    <Card title="外部 URL Cron 定时触发">
      <div className="codebox">
        <code>{curl}</code>
        <CopyButton text={curl} className="btn" />
      </div>
    </Card>
  );
}

function BackupCard() {
  const toast = useToast();
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const bytesLen = new TextEncoder().encode(pw).length;
    if (bytesLen < 12) return setError("备份加密口令至少需要 12 字节（中文字符计 3 字节）。");
    if (bytesLen > 1024) return setError("口令过长。");
    if (pw !== pw2) return setError("两次输入的备份口令不一致。");
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
      toast("加密备份已导出");
    } catch (err) {
      setError(err instanceof ApiError && err.code === "not_implemented" ? "演示模式不支持导出备份。" : errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="全量加密迁移备份" sub="使用独立口令重新加密云账号密钥与通知渠道凭证后导出完整快照">
      <form className="stack" onSubmit={submit} noValidate style={{ maxWidth: 640 }}>
        <div className="grid g2">
          <div>
            <label className="lbl" htmlFor="bk-pw">备份加密口令（至少 12 位）</label>
            <input id="bk-pw" type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="new-password" />
          </div>
          <div>
            <label className="lbl" htmlFor="bk-pw2">确认备份口令</label>
            <input id="bk-pw2" type="password" value={pw2} onChange={(e) => setPw2(e.target.value)} autoComplete="new-password" />
          </div>
        </div>
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="inline">
          <button className="btn primary" disabled={busy || !pw}>{busy && <Spinner />} 导出加密备份</button>
          <span className="sub">新机恢复：<span className="mono">hm-server restore --input 备份文件 --data-dir 数据目录</span></span>
        </div>
      </form>
    </Card>
  );
}

/* ============================================================================
 * 5. 系统与版本中心 (Timezone, Retention, Heartbeat, Branding, Version Center)
 * ========================================================================== */
function GeneralCard() {
  const toast = useToast();
  const res = useResource(api.settings, []);
  const [saving, setSaving] = useState(false);

  const change = async (tz: string) => {
    setSaving(true);
    try {
      const s = await api.updateSettings({ business_timezone: tz });
      setTimezone(s.business_timezone);
      res.mutate(() => s);
      toast("业务时区已更新");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="业务时区与采集节拍" sub="控制自然日切分、定时开关机、每日简报与免打扰窗口时区">
      <Async res={res} lines={4}>
        {(s) => {
          const zones = TIMEZONES.includes(s.business_timezone) ? TIMEZONES : [s.business_timezone, ...TIMEZONES];
          return (
            <div className="stack">
              <div>
                <label className="lbl" htmlFor="set-tz">业务时区</label>
                <div className="inline">
                  <Select
                    id="set-tz"
                    value={s.business_timezone}
                    onChange={change}
                    disabled={saving}
                    ariaLabel="业务时区"
                    style={{ width: 220 }}
                    options={zones.map((z) => ({ value: z, label: z }))}
                  />
                  {saving && <Spinner />}
                </div>
              </div>
              <div className="sub" style={{ lineHeight: 1.6 }}>
                官方 CDT 轮询：每 <b>{duration(s.collection.cdt_poll_interval_seconds)}</b> · Agent 心跳：每 <b>{duration(s.collection.agent_report_interval_seconds)}</b>（失联判定 {duration(s.collection.agent_offline_after_seconds)}）· 资源发现：每 <b>{duration(s.collection.resource_scan_interval_seconds)}</b>
              </div>
            </div>
          );
        }}
      </Async>
    </Card>
  );
}

function RetentionCard() {
  const toast = useToast();
  const res = useResource(api.settings, []);
  return (
    <Card title="历史数据保留策略" sub="自动清理过期原始样本，秒级流量在聚合为小时归档后自动回收">
      <Async res={res} lines={4}>
        {(s) => (
          <RetentionForm
            initial={s}
            onSaved={(ns) => {
              res.mutate(() => ns);
              toast("保留策略已更新");
            }}
          />
        )}
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
      <div className="grid g3">
        <div>
          <label className="lbl">秒级流量 (天)</label>
          <input type="number" min={1} max={90} value={rawDays} onChange={(e) => setRawDays(e.target.value)} />
        </div>
        <div>
          <label className="lbl">小时归档 (月)</label>
          <input type="number" min={1} max={60} value={hourlyMonths} onChange={(e) => setHourlyMonths(e.target.value)} />
        </div>
        <div>
          <label className="lbl">延迟探测 (天)</label>
          <input type="number" min={1} max={3650} value={probeDays} onChange={(e) => setProbeDays(e.target.value)} />
        </div>
      </div>
      <div className="grid g2">
        <div>
          <label className="lbl">任务与通知 (天)</label>
          <input type="number" min={7} max={3650} value={jobDays} onChange={(e) => setJobDays(e.target.value)} />
        </div>
        <div>
          <label className="lbl">操作审计 (天)</label>
          <input type="number" min={30} max={3650} value={auditDays} onChange={(e) => setAuditDays(e.target.value)} />
        </div>
      </div>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button className="btn sm primary" disabled={busy}>{busy && <Spinner />} 保存策略</button>
      </div>
    </form>
  );
}

function heartbeatBadge(hb: HeartbeatSettings): [Level, string] {
  if (!hb.url_configured) return ["neutral", "未配置"];
  if (hb.last_error) return ["critical", "上报异常"];
  if (hb.last_success_at) return ["good", "上报正常"];
  return ["warning", "待上报"];
}

function HeartbeatCard() {
  const toast = useToast();
  const res = useResource(api.heartbeat, []);
  const badge = res.data ? heartbeatBadge(res.data) : null;
  return (
    <Card
      title="外部心跳自监控"
      sub="定时向 Uptime Kuma / Healthchecks 推送存活信号"
      right={badge && <Badge level={badge[0]}>{badge[1]}</Badge>}
    >
      <Async res={res} lines={3}>
        {(hb) => (
          <HeartbeatForm
            value={hb}
            onSaved={(nh) => res.mutate(() => nh)}
            onTested={() => {
              toast("心跳测试已送达");
              res.reload();
            }}
          />
        )}
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
      toast("外部心跳设置已保存");
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
        <input type="number" min={15} max={86400} value={interval} onChange={(e) => setIntervalSecs(e.target.value)} style={{ width: 84, padding: "4px 8px" }} />
        <span>秒主动上报</span>
      </div>
      <div>
        <label className="lbl" htmlFor="hb-url">Push URL（加密存储不回显）</label>
        <input
          id="hb-url"
          type="text"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder={value.url_configured ? "已加密保存，留空不变" : "https://uptime.example.com/api/push/xxxx"}
          autoComplete="off"
        />
      </div>
      {value.last_success_at && <div className="sub">最近成功：{stamp(value.last_success_at)}</div>}
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        {value.url_configured && (
          <button type="button" className="btn sm" disabled={testing || busy} onClick={runTest}>
            {testing && <Spinner />} 测试上报
          </button>
        )}
        <button className="btn sm primary" disabled={busy || testing}>{busy && <Spinner />} 保存心跳</button>
      </div>
    </form>
  );
}

function BrandingCard() {
  const toast = useToast();
  const { branding } = useShared();
  const fileRef = useRef<HTMLInputElement>(null);
  const [name, setName] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <Card title="面板品牌外观" sub="自定义侧栏、登录页及浏览器标签页显示的名称与图标">
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
              toast("自定义图标已上传");
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
                  <button className="btn sm primary" disabled={busy || curName.trim() === b.name}>{busy && <Spinner />} 保存</button>
                </div>
              </div>
              <div className="inline">
                <input ref={fileRef} type="file" accept="image/png,image/jpeg" hidden onChange={(e) => onPickLogo(e.target.files?.[0])} />
                <button type="button" className="btn sm" disabled={busy} onClick={() => fileRef.current?.click()}>上传 PNG / JPEG 图标</button>
                {b.logo && <button type="button" className="btn sm danger" disabled={busy} onClick={clearLogo}>恢复默认</button>}
                <span className="sub">≤ 256 KB</span>
              </div>
            </form>
          );
        }}
      </Async>
    </Card>
  );
}

function VersionCenterCard({ onLogout }: { onLogout: () => void }) {
  const toast = useToast();
  const vc = useResource(api.versionCenter, []);
  const [checking, setChecking] = useState(false);

  const checkUpdate = async () => {
    setChecking(true);
    try {
      const next = await api.checkLatestVersion();
      vc.mutate(() => next);
      if (next.last_error) toast(errorText(next.last_error.code), true);
      else if (next.update_available) toast(`发现新版本 ${next.latest_version}`);
      else toast("当前已是最新版本");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setChecking(false);
    }
  };

  return (
    <Card
      title="版本中心与安全升级指引"
      sub="零特权容器设计：不挂载宿主机 docker.sock，提供标准一键升级命令"
      right={
        <div className="inline">
          <button className="btn sm" disabled={checking} onClick={checkUpdate}>
            {checking && <Spinner />} 检查更新
          </button>
          <button className="btn sm ghost" onClick={onLogout}>退出登录</button>
        </div>
      }
    >
      <Async res={vc} lines={4}>
        {(v) => (
          <div className="stack">
            <dl className="kv">
              <dt>服务端版本</dt>
              <dd>
                <b>{ver(v.server_version)}</b>
                <span className="sub"> · {v.server_commit} · {v.deployment_mode}</span>
                {v.update_available && v.latest_version && (
                  <span style={{ marginLeft: 8 }}>
                    <Badge level="warning">新版本 {ver(v.latest_version)}</Badge>
                    {v.release_url && (
                      <a href={v.release_url} target="_blank" rel="noreferrer" style={{ marginLeft: 8, fontSize: "var(--fs-sm)" }}>
                        更新日志 ↗
                      </a>
                    )}
                  </span>
                )}
              </dd>
              <dt>内置 Agent</dt>
              <dd className="mono">{v.bundled_agent_version ? ver(v.bundled_agent_version) : "未内置"}</dd>
              {v.checked_at && (
                <>
                  <dt>最近检查</dt>
                  <dd className="sub">{stamp(v.checked_at)}{v.latest_version ? `（最新稳定版 ${v.latest_version}）` : ""}</dd>
                </>
              )}
            </dl>
            <div className="grid g2">
              <div>
                <div className="sub" style={{ marginBottom: 4 }}>Docker Compose 升级命令</div>
                <div className="codebox">
                  <code>{v.upgrade_commands.docker_compose}</code>
                  <CopyButton text={v.upgrade_commands.docker_compose} className="btn" />
                </div>
              </div>
              <div>
                <div className="sub" style={{ marginBottom: 4 }}>二进制 / Systemd 升级命令</div>
                <div className="codebox">
                  <code>{v.upgrade_commands.binary_hint}</code>
                  <CopyButton text={v.upgrade_commands.binary_hint} className="btn" />
                </div>
              </div>
            </div>
          </div>
        )}
      </Async>
    </Card>
  );
}

/* ============================================================================
 * 6. 任务队列与操作审计中心 (Unified Jobs + Audit Log!)
 * ========================================================================== */
function ActivityCenter({ initialKind }: { initialKind: "jobs" | "audit" }) {
  const [kind, setKind] = useState<"jobs" | "audit">(initialKind);
  return (
    <>
      <div className="toolbar">
        <SegToggle
          label="活动视图"
          value={kind}
          onChange={setKind}
          options={[
            ["jobs", "后台异步任务队列"],
            ["audit", "配置与操作审计日志"],
          ]}
        />
      </div>

      {kind === "jobs" ? <JobsTableCard /> : <AuditLogCard />}
    </>
  );
}

function JobsTableCard() {
  const { accounts, instances } = useShared();
  const [type, setType] = useState<JobType | "">("");
  const [status, setStatus] = useState<JobStatus | "">("");
  const [accountId, setAccountId] = useState("");
  const [cursor, setCursor] = useState<string>();

  const res = useResource(
    () => api.jobs({ type: type || undefined, status: status || undefined, account_id: accountId || undefined, cursor, limit: 35 }),
    [type, status, accountId, cursor],
  );

  const accs = accounts.data ?? [];
  const accName = (id: string) => accs.find((a) => a.id === id)?.name ?? id;
  const instName = (id: string) => instances.data?.items.find((i) => i.id === id)?.name ?? id;

  return (
    <Card
      title="后台任务执行记录"
      right={
        <div className="filters">
          <Select<JobType | "">
            size="sm"
            value={type}
            onChange={(val) => { setType(val); setCursor(undefined); }}
            ariaLabel="按任务类型筛选"
            options={[
              { value: "", label: "全部任务类型" },
              ...(Object.keys(JOB_TYPE) as JobType[]).map((k) => ({ value: k, label: JOB_TYPE[k] })),
            ]}
          />
          <Select<JobStatus | "">
            size="sm"
            value={status}
            onChange={(val) => { setStatus(val); setCursor(undefined); }}
            ariaLabel="按状态筛选"
            options={[
              { value: "", label: "全部状态" },
              ...(Object.keys(JOB_STATUS) as JobStatus[]).map((k) => ({ value: k, label: JOB_STATUS[k][1] })),
            ]}
          />
          {accs.length > 0 && (
            <Select
              size="sm"
              value={accountId}
              onChange={(val) => { setAccountId(val); setCursor(undefined); }}
              ariaLabel="按账号筛选"
              options={[
                { value: "", label: "全部账号" },
                ...accs.map((a) => ({ value: a.id, label: a.name })),
              ]}
            />
          )}
        </div>
      }
    >
      <Async res={res} lines={6}>
        {(p) =>
          !p.items.length ? (
            <EmptyState title="没有匹配的任务记录" />
          ) : (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>创建时间</th>
                      <th>任务与对象</th>
                      <th>触发来源</th>
                      <th>执行状态</th>
                      <th>耗时 / 诊断</th>
                    </tr>
                  </thead>
                  <tbody>
                    {p.items.map((j) => {
                      const [lv, lb] = JOB_STATUS[j.status];
                      const durMs = j.started_at && j.finished_at ? Math.max(0, Date.parse(j.finished_at) - Date.parse(j.started_at)) : null;
                      return (
                        <tr key={j.id}>
                          <td>{stamp(j.created_at)}</td>
                          <td>
                            <b>{JOB_TYPE[j.type] ?? j.type}</b>
                            <div className="sub">
                              {j.instance_id ? instName(j.instance_id) : j.account_ids?.length ? j.account_ids.map(accName).join("、") : "系统全局"}
                            </div>
                          </td>
                          <td className="sub">{j.trigger ? (JOB_TRIGGER[j.trigger] ?? j.trigger) : "—"}</td>
                          <td><Badge level={lv}>{lb}</Badge></td>
                          <td className="wrap">
                            {j.error ? <span className="field-error">{j.error.code}</span> : durMs !== null ? <span className="sub num">{(durMs / 1000).toFixed(1)}s</span> : <span className="sub">—</span>}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              {(cursor || p.next_cursor) && (
                <div className="inline" style={{ marginTop: 12 }}>
                  {cursor && <button className="btn sm" onClick={() => setCursor(undefined)}>返回最新</button>}
                  {p.next_cursor && <button className="btn sm" onClick={() => setCursor(p.next_cursor!)}>查看更早任务</button>}
                </div>
              )}
            </>
          )
        }
      </Async>
    </Card>
  );
}

function AuditLogCard() {
  const { accounts } = useShared();
  const [actor, setActor] = useState("");
  const [accountId, setAccountId] = useState("");
  const [cursor, setCursor] = useState<string>();
  const res = useResource(() => api.auditLog({ actor: actor || undefined, account_id: accountId || undefined, cursor, limit: 40 }), [actor, accountId, cursor]);
  const accName = (id: string | null) => (id ? accounts.data?.find((a) => a.id === id)?.name ?? id : "—");

  return (
    <Card
      title="操作审计日志"
      right={
        <div className="filters">
          <Select
            size="sm"
            value={actor}
            onChange={(val) => { setActor(val); setCursor(undefined); }}
            ariaLabel="按操作来源筛选"
            options={[
              { value: "", label: "全部操作来源" },
              ...(Object.keys(AUDIT_ACTOR) as (keyof typeof AUDIT_ACTOR)[]).map((k) => ({ value: k, label: AUDIT_ACTOR[k] })),
            ]}
          />
          {(accounts.data?.length ?? 0) > 0 && (
            <Select
              size="sm"
              value={accountId}
              onChange={(val) => { setAccountId(val); setCursor(undefined); }}
              ariaLabel="按账号筛选"
              options={[
                { value: "", label: "全部账号" },
                ...accounts.data!.map((a) => ({ value: a.id, label: a.name })),
              ]}
            />
          )}
        </div>
      }
    >
      <Async res={res} lines={6}>
        {(p) =>
          !p.items.length ? (
            <EmptyState title="没有匹配的操作审计记录" />
          ) : (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>操作主体</th>
                      <th>动作摘要</th>
                      <th>关联账号</th>
                      <th>字段变更</th>
                    </tr>
                  </thead>
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
                          {a.details.credentials_changed && <Badge level="warning">密钥已更新</Badge>}
                          {a.details.before || a.details.after ? (
                            <div className="sub mono" style={{ marginTop: 2 }}>
                              {Object.keys({ ...(a.details.before ?? {}), ...(a.details.after ?? {}) }).map((k) => (
                                <div key={k}>
                                  {k}: {JSON.stringify(a.details.before?.[k])} → {JSON.stringify(a.details.after?.[k])}
                                </div>
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
                  {cursor && <button className="btn sm" onClick={() => setCursor(undefined)}>返回最新</button>}
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
