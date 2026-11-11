// 实例：表格（手机上是卡片）、筛选、详情、采集程序安装引导。M1 只读，开关机等操作在 M2 加入。
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { AGENT, CHARGE, INSTANCE_STATUS, KIND, KIND_SHORT, protectionOf, regionName, RULE, SCOPE, SUBJECT_KIND } from "../api/labels";
import type { AgentInstallCommand, Instance } from "../api/types";
import { Badge, Card, Confirm, CopyButton, EmptyState, ErrorState, errMsg, Fresh, Modal, Skeleton, Spinner, useToast } from "../components/ui";
import { useShared } from "../lib/data";
import { ago, duration, rate, stamp, ymd } from "../lib/format";
import { Link } from "../lib/router";
import { useNow } from "../lib/useResource";

const canInstall = (i: Instance) => i.agent.status === "not_installed";

function Rate({ i }: { i: Instance }) {
  if (i.agent.status === "offline") return <span className="sub">掉线，没有数据</span>;
  if (!i.realtime) return <span className="sub">—</span>;
  return (
    <span className="num">
      <span title="发出">↑ {rate(i.realtime.tx_bytes_per_second)}</span>{" "}
      <span className="sub" title="收到">↓ {rate(i.realtime.rx_bytes_per_second)}</span>
    </span>
  );
}

function Billing({ i }: { i: Instance }) {
  if ((i.billing_subject_ids?.length ?? 0) > 1) return <span className="sub">多个公网出口 · 分别计费</span>;
  if (!i.billing_subject_kind) return <span className="sub">{i.public_ips.length ? "公网计费类型待确认" : "未发现公网出口"}</span>;
  return (
    <div className="cell-stack">
      <span>{i.bandwidth_package_name ? `共享带宽 ${i.bandwidth_package_name} · ${SUBJECT_KIND[i.billing_subject_kind].split(" · ")[1]}` : SUBJECT_KIND[i.billing_subject_kind]}</span>
      <span className="sub">{i.counts_toward_cdt === null ? "是否计入免费额度待确认" : i.counts_toward_cdt && i.cdt_scope ? `计入${SCOPE[i.cdt_scope]}免费额度` : "不计入免费额度"}{i.bandwidth_mbps ? ` · ${i.bandwidth_mbps} Mbps` : ""}</span>
    </div>
  );
}

function AgentCell({ i, onInstall }: { i: Instance; onInstall: () => void }) {
  const [al, at] = AGENT[i.agent.status];
  const [pl, pt, pd] = protectionOf(i);
  return (
    <div className="cell-stack" style={{ justifyItems: "start" }}>
      {i.realtime_protection === "not_applicable" ? <Badge level={al}>{at}</Badge> : <Badge level={pl} title={pd}>{pt}</Badge>}
      {canInstall(i) && (
        <button className="linkbtn sub" onClick={(e) => { e.stopPropagation(); onInstall(); }}>
          {i.realtime_protection === "unprotected" ? "安装采集程序" : "也可以安装"}
        </button>
      )}
    </div>
  );
}

export function Instances() {
  const { accounts, instances } = useShared();
  const [account, setAccount] = useState("");
  const [kind, setKind] = useState("");
  const [only, setOnly] = useState(false);
  const [detail, setDetail] = useState<string>();
  const [install, setInstall] = useState<string>();

  if (instances.error) return <div className="card"><ErrorState error={instances.error} onRetry={instances.reload} /></div>;
  if (!instances.data || !accounts.data) return <div className="card"><Skeleton lines={6} height={18} /></div>;
  const all = instances.data.items, accs = accounts.data;
  const accName = (id: string) => accs.find((a) => a.id === id)?.name ?? "—";
  const needs = (i: Instance) => i.realtime_protection === "worst_case" || (i.realtime_protection === "unprotected" && i.status === "running" && i.agent.status === "not_installed");
  const rows = all.filter((i) => (!account || i.account_id === account) && (!kind || i.kind === kind) && (!only || needs(i)));
  const cur = all.find((i) => i.id === detail), inst = all.find((i) => i.id === install);

  if (!all.length)
    return (
      <div className="card">
        {accs.length ? (
          <EmptyState title="还没有发现实例">这些账号在所选地域里没有云服务器或轻量服务器。如果机器在别的地域，请到设置页为账号勾选对应地域。</EmptyState>
        ) : (
          <EmptyState title="还没有添加阿里云账号" action={<Link className="btn primary" to="/settings">去添加账号</Link>}>添加账号后会自动发现它名下的机器。</EmptyState>
        )}
      </div>
    );

  return (
    <>
      <Card
        title="全部实例" sub={`${rows.length} / ${all.length} 台`}
        right={
          <div className="filters">
            <Fresh at={instances.data.updated_at} kind="resources" prefix="资源信息截至" name="资源信息" />
            <select value={account} onChange={(e) => setAccount(e.target.value)} aria-label="按账号筛选">
              <option value="">全部账号</option>
              {accs.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
            <select value={kind} onChange={(e) => setKind(e.target.value)} aria-label="按类型筛选">
              <option value="">全部类型</option>
              <option value="ecs">{KIND.ecs}</option>
              <option value="swas">{KIND.swas}</option>
            </select>
            <label className={`check ${only ? "on" : ""}`}><input type="checkbox" checked={only} onChange={(e) => setOnly(e.target.checked)} />只看需要处理的</label>
          </div>
        }
      >
        {!rows.length ? (
          <EmptyState title="没有符合条件的实例" action={<button className="btn" onClick={() => { setAccount(""); setKind(""); setOnly(false); }}>清除筛选</button>} />
        ) : (
          <>
            <div className="table-wrap only-wide">
              <table>
                <thead>
                  <tr><th>名称</th><th>账号 / 地域</th><th>状态</th><th>公网 IP</th><th>公网怎么计费</th><th>当前速度</th><th>实时保护</th><th>付费 / 到期</th></tr>
                </thead>
                <tbody>
                  {rows.map((i) => {
                    const [sl, st] = INSTANCE_STATUS[i.status];
                    return (
                      <tr key={i.id} className="clickable" onClick={() => setDetail(i.id)}>
                        <td><div className="cell-stack" style={{ justifyItems: "start" }}><button className="linkbtn" style={{ fontWeight: 650, color: "var(--text)", textDecoration: "none" }} onClick={(e) => { e.stopPropagation(); setDetail(i.id); }}>{i.name}</button><span className="sub">{KIND[i.kind]}</span></div></td>
                        <td><div className="cell-stack"><span>{accName(i.account_id)}</span><span className="sub">{regionName(i.region_id)}</span></div></td>
                        <td><Badge level={sl}>{st}</Badge></td>
                        <td className="mono">{i.public_ips.map((p) => <div key={p.address}>{p.address}{p.type === "eip" ? "（弹性）" : ""}</div>)}{!i.public_ips.length && "—"}</td>
                        <td><Billing i={i} /></td>
                        <td><Rate i={i} /></td>
                        <td><AgentCell i={i} onInstall={() => setInstall(i.id)} /></td>
                        <td><div className="cell-stack"><span>{CHARGE[i.charge_type]}</span>{i.expires_at && <span className="sub">{ymd(i.expires_at)} 到期</span>}</div></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div className="icards">
              {rows.map((i) => {
                const [sl, st] = INSTANCE_STATUS[i.status];
                return (
                  <div key={i.id} className="icard" role="button" tabIndex={0} onClick={() => setDetail(i.id)} onKeyDown={(e) => e.key === "Enter" && setDetail(i.id)}>
                    <div className="hd"><b>{i.name}</b><span className="tag">{KIND_SHORT[i.kind]}</span><div className="spacer" /><Badge level={sl}>{st}</Badge></div>
                    <div className="sub">{accName(i.account_id)} · {regionName(i.region_id)} · <span className="mono">{i.public_ips[0]?.address ?? "没有公网 IP"}</span></div>
                    <Billing i={i} />
                    <div className="hd"><AgentCell i={i} onInstall={() => setInstall(i.id)} /><div className="spacer" /><Rate i={i} /></div>
                  </div>
                );
              })}
            </div>
          </>
        )}
        <p className="note">
          “公网怎么计费”说明这台机器的流量按哪种规则计入免费额度。“实时保护”表示机器上是否装了采集程序：装了，用量每 10 秒更新；没装，要等约 1 小时后的官方数据。
        </p>
      </Card>
      {cur && <InstanceDetail i={cur} account={accName(cur.account_id)} onClose={() => setDetail(undefined)} onInstall={() => { setDetail(undefined); setInstall(cur.id); }} />}
      {inst && <AgentInstall i={inst} onClose={() => setInstall(undefined)} />}
    </>
  );
}

function InstanceDetail({ i, account, onClose, onInstall }: { i: Instance; account: string; onClose: () => void; onInstall: () => void }) {
  const toast = useToast();
  const now = useNow();
  const [revoke, setRevoke] = useState(false);
  const { instances } = useShared();
  const [sl, st] = INSTANCE_STATUS[i.status];
  const [al, at] = AGENT[i.agent.status];
  const [pl, pt, pd] = protectionOf(i);
  if (revoke)
    return (
      <Confirm
        title={`撤销 ${i.name} 的采集程序凭证？`} okText="撤销" danger onClose={() => setRevoke(false)}
        body="撤销后，这台机器上的采集程序立刻无法再上报，这台机器会变回“未安装”。想恢复需要重新生成安装命令并在机器上再执行一次。"
        onOk={async () => {
          try {
            await api.revokeAgentToken(i.id);
            instances.reload();
            toast("已撤销");
            onClose();
          } catch (e) {
            toast(errMsg(e), true);
            throw e;
          }
        }}
      />
    );
  return (
    <Modal title={i.name} onClose={onClose} wide>
      <dl className="kv">
        <dt>状态</dt><dd><Badge level={sl}>{st}</Badge></dd>
        <dt>账号 / 地域</dt><dd>{account} · {regionName(i.region_id)}{i.zone_id ? `（${i.zone_id}）` : ""}</dd>
        <dt>类型</dt><dd>{KIND[i.kind]} · {CHARGE[i.charge_type]}{i.expires_at ? ` · ${ymd(i.expires_at)} 到期` : ""}</dd>
        <dt>规格</dt><dd>{i.spec}</dd>
        <dt>阿里云实例 ID</dt><dd className="mono">{i.cloud_instance_id}</dd>
        <dt>公网 IP</dt><dd className="mono">{i.public_ips.map((p) => `${p.address}${p.type === "eip" ? "（弹性公网 IP）" : ""}`).join("、") || "—"}</dd>
        <dt>公网怎么计费</dt><dd>{(i.billing_subject_ids?.length ?? 0) > 1 ? "多个公网出口，按各自规则计费，详见流量页" : i.billing_subject_kind ? SUBJECT_KIND[i.billing_subject_kind] : i.public_ips.length ? "公网计费类型待确认" : "未发现公网出口"}{i.bandwidth_package_name ? `（${i.bandwidth_package_name}）` : ""}</dd>
        <dt>计量规则</dt><dd>{i.metering_rule ? RULE[i.metering_rule] : "—"}{i.counts_toward_cdt === null ? " · 是否计入免费额度待确认" : i.counts_toward_cdt && i.cdt_scope ? ` · 计入${SCOPE[i.cdt_scope]}免费额度` : " · 不计入免费额度"}</dd>
        {i.kind === "swas" && <><dt>套餐流量包</dt><dd>{i.billing_subject_kind === "swas_no_traffic_package" ? "无流量包（这个套餐不按流量计费）" : i.billing_subject_kind === "swas_traffic_package" ? "有流量包" : "待确认"}</dd></>}
        <dt>带宽上限</dt><dd>{i.bandwidth_mbps ? `${i.bandwidth_mbps} Mbps` : "—"}</dd>
        <dt>资源信息更新于</dt><dd>{stamp(i.updated_at)}</dd>
      </dl>
      <h3 style={{ fontSize: "var(--fs-md)", margin: "18px 0 8px" }}>采集程序（hm-agent）</h3>
      <dl className="kv">
        <dt>实时保护</dt><dd><Badge level={pl}>{pt}</Badge><div className="sub" style={{ marginTop: 4 }}>{pd}</div></dd>
        <dt>采集程序</dt><dd><Badge level={al}>{at}</Badge>{i.agent.version ? ` 版本 ${i.agent.version}` : ""}</dd>
        {i.agent.last_report_at && <><dt>最后上报</dt><dd>{stamp(i.agent.last_report_at)}（{ago(i.agent.last_report_at, now)}）</dd></>}
        {i.agent.offline_since && <><dt>已掉线</dt><dd>{duration((now - new Date(i.agent.offline_since).getTime()) / 1000)}</dd></>}
        {i.agent.worst_case && <><dt>推算依据</dt><dd>{i.agent.worst_case.basis === "recent_peak" ? "掉线前 10 分钟的最高速度" : "带宽上限"} {rate(i.agent.worst_case.rate_bytes_per_second)}</dd></>}
        {i.realtime && <><dt>当前速度</dt><dd>发出 {rate(i.realtime.tx_bytes_per_second)} · 收到 {rate(i.realtime.rx_bytes_per_second)}</dd></>}
      </dl>
      {i.agent.status === "offline" && (
        <p className="note">排查方法：登录这台机器，执行 <span className="mono">systemctl status hm-agent</span> 看它是否在运行；再确认机器能访问面板的地址。</p>
      )}
      <div className="actions">
        {i.agent.status !== "not_installed" && <button className="btn danger" onClick={() => setRevoke(true)}>撤销凭证</button>}
        {canInstall(i) && <button className="btn primary" onClick={onInstall}>安装采集程序</button>}
        <button className="btn" onClick={onClose}>关闭</button>
      </div>
    </Modal>
  );
}

/** 采集程序安装引导：生成命令 -> 复制 -> 等待上线 */
function AgentInstall({ i, onClose }: { i: Instance; onClose: () => void }) {
  const now = useNow();
  const [cmd, setCmd] = useState<AgentInstallCommand>();
  const [error, setError] = useState<string>();
  const [startedAt, setStartedAt] = useState(Date.now());
  const online = i.agent.status === "online";

  const generate = () => {
    setCmd(undefined);
    setError(undefined);
    api.agentInstallCommand(i.id).then((c) => { setCmd(c); setStartedAt(Date.now()); }, (e) => setError(errMsg(e)));
  };
  useEffect(generate, [i.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const left = cmd ? Math.max(0, (new Date(cmd.expires_at).getTime() - now) / 1000) : 0;
  const expired = !!cmd && left <= 0 && !online;
  const waited = (now - startedAt) / 1000;

  return (
    <Modal title={`给 ${i.name} 安装采集程序`} onClose={onClose} wide>
      <p>采集程序只统计这台机器的公网流量并每 10 秒上报一次。它不会执行任何其他命令，也不需要在机器上开放端口。</p>
      <ol className="steps">
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">复制安装命令</div>
            {error ? (
              <div className="field-error">{error} <button className="linkbtn" onClick={generate}>再试一次</button></div>
            ) : !cmd ? (
              <div className="sub"><Spinner /> 正在生成…</div>
            ) : (
              <>
                <div className="codebox" style={{ marginTop: 6 }}>
                  <code>{cmd.command}</code>
                  <CopyButton text={cmd.command} className="btn" />
                </div>
                <div className="sub" style={{ marginTop: 4 }}>
                  {online ? "命令已使用。" : expired ? <>命令已过期。<button className="linkbtn" onClick={generate}>重新生成</button></> : `这条命令只能用一次，${duration(left)}后失效。它只对 ${i.name} 有效。`}
                </div>
              </>
            )}
          </div>
        </li>
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">登录 {i.name}，粘贴并执行</div>
            <div className="sub">需要管理员权限（命令里已经带了 sudo）。支持常见的 64 位 Linux 系统。</div>
          </div>
        </li>
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">等待上线</div>
            {online ? (
              <div style={{ marginTop: 4 }}>
                <Badge level="good">已上线</Badge>{" "}
                {i.realtime && <span className="sub">当前速度：发出 {rate(i.realtime.tx_bytes_per_second)} · 收到 {rate(i.realtime.rx_bytes_per_second)}</span>}
              </div>
            ) : cmd && !expired ? (
              <div className="sub">
                <Spinner /> 正在等它第一次上报（已等 {duration(waited)}）。执行命令后通常几十秒内出现。
                {waited > 90 && <div style={{ marginTop: 4 }}>等了比较久。请确认命令执行没有报错，并且这台机器能访问面板的地址。</div>}
              </div>
            ) : (
              <div className="sub">执行命令后，这里会自动变成“已上线”。</div>
            )}
          </div>
        </li>
      </ol>
      <div className="actions">
        <button className={`btn ${online ? "primary" : ""}`} onClick={onClose}>{online ? "完成" : "关闭（不影响安装）"}</button>
      </div>
    </Modal>
  );
}
