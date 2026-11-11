// 阿里云账号表单（安装向导与设置页共用）与权限测试结果。
import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError } from "../api/client";
import { errorText, fieldErrorText, PERMISSION, PERMISSION_RESULT, regionName, SITE } from "../api/labels";
import type { Account, AccountSite, PermissionCheck, Region } from "../api/types";
import { GB } from "../lib/format";
import { Badge, Spinner } from "./ui";

export function PermissionList({ check, pending }: { check: PermissionCheck | null; pending?: boolean }) {
  if (pending)
    return (
      <span className="sub">
        <Spinner /> 正在测试权限…
      </span>
    );
  if (!check) return <span className="sub">还没有测试过</span>;
  return (
    <div className="perm">
      {check.items.map((it) => {
        const [level, text] = PERMISSION_RESULT[it.result];
        return (
          <Badge key={it.key} level={level} title={text}>
            {PERMISSION[it.key]}
            {it.result === "passed" ? "" : ` · ${text}`}
          </Badge>
        );
      })}
    </div>
  );
}

interface Props {
  account?: Account; // 传入则为编辑
  submitText: string;
  onSaved: (account: Account, permissionJobId?: string) => void;
  onCancel?: () => void;
  cancelText?: string;
}

export function AccountForm({ account, submitText, onSaved, onCancel, cancelText = "取消" }: Props) {
  const edit = !!account;
  const [name, setName] = useState(account?.name ?? "");
  const [site, setSite] = useState<AccountSite>(account?.site ?? "china");
  const [akId, setAkId] = useState("");
  const [akSecret, setAkSecret] = useState("");
  const [regions, setRegions] = useState<string[]>(account?.region_ids ?? []);
  const [all, setAll] = useState<Region[]>();
  const [mainland, setMainland] = useState(String((account?.quota_mainland_bytes ?? 20 * GB) / GB));
  const [overseas, setOverseas] = useState(String((account?.quota_overseas_bytes ?? 200 * GB) / GB));
  const [thresholds, setThresholds] = useState((account?.alert_thresholds_percent ?? [80, 90, 95]).join(", "));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [fields, setFields] = useState<Record<string, string>>({});

  useEffect(() => {
    let live = true;
    setAll(undefined);
    api.regions(site).then((r) => live && setAll(r), () => live && setAll([]));
    return () => {
      live = false;
    };
  }, [site]);

  const toggle = (id: string) => setRegions((r) => (r.includes(id) ? r.filter((x) => x !== id) : [...r, id]));
  const fe = (k: string) => (fields[k] ? <div className="field-error">{fieldErrorText(fields[k])}</div> : null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const local: Record<string, string> = {};
    const th = thresholds.split(/[,，\s]+/).filter(Boolean).map(Number);
    if (!name.trim()) local.name = "required";
    if (!edit && !akId.trim()) local.access_key_id = "required";
    if (!edit && !akSecret.trim()) local.access_key_secret = "required";
    if (edit && !!akId.trim() !== !!akSecret.trim()) local.access_key_secret = "required";
    if (!regions.length) local.region_ids = "required";
    if (!(Number(mainland) >= 0)) local.quota_mainland_bytes = "invalid_format";
    if (!(Number(overseas) >= 0)) local.quota_overseas_bytes = "invalid_format";
    if (!th.length || th.some((t) => !(t > 0 && t <= 100))) local.alert_thresholds_percent = "out_of_range";
    setFields(local);
    setError(undefined);
    if (Object.keys(local).length) return;
    const common = {
      name: name.trim(), region_ids: regions,
      quota_mainland_bytes: Math.round(Number(mainland) * GB), quota_overseas_bytes: Math.round(Number(overseas) * GB),
      reset_day: 1 as const, alert_thresholds_percent: [...th].sort((a, b) => a - b),
    };
    setBusy(true);
    try {
      if (edit) {
        const saved = await api.updateAccount(account.id, { ...common, ...(akId.trim() ? { access_key_id: akId.trim(), access_key_secret: akSecret.trim() } : {}) });
        onSaved(saved);
      } else {
        const res = await api.createAccount({ ...common, site, access_key_id: akId.trim(), access_key_secret: akSecret.trim() });
        onSaved(res.account, res.permission_check_job_id);
      }
    } catch (err) {
      if (err instanceof ApiError) {
        setFields(err.fields ?? {});
        setError(errorText(err.code));
      } else setError(errorText("internal_error"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit} noValidate>
      <div>
        <label className="lbl" htmlFor="acc-name">名称</label>
        <input id="acc-name" type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="自己起一个好认的名字" aria-invalid={!!fields.name} autoComplete="off" />
        {fe("name")}
      </div>
      <div>
        <span className="lbl" style={{ display: "block", fontSize: "var(--fs-md)", color: "var(--text-2)", marginBottom: 4 }}>站点</span>
        {edit ? (
          <span>{SITE[site]}（创建后不能修改）</span>
        ) : (
          <div className="checks" role="radiogroup" aria-label="站点">
            {(["china", "international"] as AccountSite[]).map((s) => (
              <label key={s} className={`check ${site === s ? "on" : ""}`}>
                <input type="radio" name="site" checked={site === s} onChange={() => { setSite(s); setRegions([]); }} />
                {SITE[s]}
              </label>
            ))}
          </div>
        )}
        <div className="field-help">在 aliyun.com 注册的选国内站；在 alibabacloud.com 注册的选国际站。</div>
      </div>
      <div>
        <label className="lbl" htmlFor="acc-ak">AccessKey ID</label>
        <input id="acc-ak" type="text" value={akId} onChange={(e) => setAkId(e.target.value)} placeholder={edit ? `${account.access_key_id_masked}（留空表示不修改）` : "LTAI…"} aria-invalid={!!fields.access_key_id} autoComplete="off" spellCheck={false} />
        {fe("access_key_id")}
      </div>
      <div>
        <label className="lbl" htmlFor="acc-sk">AccessKey Secret</label>
        <input id="acc-sk" type="password" value={akSecret} onChange={(e) => setAkSecret(e.target.value)} placeholder={edit ? "留空表示不修改" : ""} aria-invalid={!!fields.access_key_secret} autoComplete="new-password" />
        {fe("access_key_secret")}
        <div className="field-help">加密保存，保存后不会再显示。建议用只有查看权限的子账号密钥。</div>
      </div>
      <div>
        <span className="lbl" style={{ display: "block", fontSize: "var(--fs-md)", color: "var(--text-2)", marginBottom: 4 }}>要扫描的地域</span>
        {!all ? (
          <span className="sub"><Spinner /> 正在读取地域列表…</span>
        ) : (
          <div className="checks">
            {all.map((r) => (
              <label key={r.id} className={`check ${regions.includes(r.id) ? "on" : ""}`} title={r.id}>
                <input type="checkbox" checked={regions.includes(r.id)} onChange={() => toggle(r.id)} />
                {regionName(r.id)}
              </label>
            ))}
          </div>
        )}
        {fe("region_ids")}
        <div className="field-help">只勾选你有机器的地域，扫描会更快。</div>
      </div>
      <details open={edit}>
        <summary className="sub" style={{ cursor: "pointer" }}>免费额度与告警（一般不用改）</summary>
        <div className="stack" style={{ marginTop: 12 }}>
          <div className="grid g2" style={{ gap: 12 }}>
            <div>
              <label className="lbl" htmlFor="acc-qo">海外免费额度（GB / 月）</label>
              <input id="acc-qo" type="number" min="0" value={overseas} onChange={(e) => setOverseas(e.target.value)} aria-invalid={!!fields.quota_overseas_bytes} />
              {fe("quota_overseas_bytes")}
            </div>
            <div>
              <label className="lbl" htmlFor="acc-qm">国内免费额度（GB / 月）</label>
              <input id="acc-qm" type="number" min="0" value={mainland} onChange={(e) => setMainland(e.target.value)} aria-invalid={!!fields.quota_mainland_bytes} />
              {fe("quota_mainland_bytes")}
            </div>
          </div>
          <div className="grid g2" style={{ gap: 12 }}>
            <div>
              <label className="lbl" htmlFor="acc-rd">官方额度重置日</label>
              <input id="acc-rd" value="每月 1 日（自然月）" readOnly />
              <div className="field-help">CDT 官方周期固定，修改面板时区不会改变计费周期。</div>
            </div>
            <div>
              <label className="lbl" htmlFor="acc-th">用到百分之几时通知</label>
              <input id="acc-th" type="text" value={thresholds} onChange={(e) => setThresholds(e.target.value)} aria-invalid={!!fields.alert_thresholds_percent} />
              {fe("alert_thresholds_percent")}
            </div>
          </div>
        </div>
      </details>
      {error && <div className="field-error" role="alert">{error}</div>}
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        {onCancel && <button type="button" className="btn" onClick={onCancel}>{cancelText}</button>}
        <button className="btn primary" disabled={busy}>{busy && <Spinner />} {submitText}</button>
      </div>
    </form>
  );
}
