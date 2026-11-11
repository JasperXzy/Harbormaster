// 首次安装向导：① 设置管理员密码 ② 添加阿里云账号 ③ Telegram 通知 ④ 完成。②③ 可以跳过，之后在设置里补。
import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError, MOCK } from "../api/client";
import { waitForJob } from "../api/jobs";
import { errorText, fieldErrorText } from "../api/labels";
import type { Account, TelegramChannel } from "../api/types";
import { AccountForm, PermissionList } from "../components/AccountForm";
import { Logo } from "../components/Shell";
import { TelegramForm } from "../components/TelegramForm";
import { Async, Badge, Spinner } from "../components/ui";
import { navigate } from "../lib/router";
import { useResource } from "../lib/useResource";

const STEPS = ["设置密码", "添加账号", "通知", "完成"];

export function Setup({ minPassword, onDone }: { minPassword: number; onDone: () => void }) {
  const [step, setStep] = useState(0);
  const [account, setAccount] = useState<Account>();
  const [telegramOk, setTelegramOk] = useState(false);

  return (
    <div className="solo">
      <div className="solo-box wide">
        <div className="brand"><Logo size={28} /><span>Harbormaster</span></div>
        <div>
          <div className="wizard-steps" aria-hidden="true">{STEPS.map((_, i) => <span key={i} className={i <= step ? "on" : ""} />)}</div>
          <div className="sub" style={{ marginTop: 6 }}>第 {step + 1} 步，共 {STEPS.length} 步 · {STEPS[step]}</div>
        </div>
        {step === 0 && <PasswordStep minPassword={minPassword} onNext={() => setStep(1)} />}
        {step === 1 && <AccountStep account={account} onAccount={setAccount} onNext={() => setStep(2)} />}
        {step === 2 && <TelegramStep onOk={() => setTelegramOk(true)} onNext={() => setStep(3)} />}
        {step === 3 && (
          <div className="card stack">
            <h2>设置完成</h2>
            <div className="rowline"><div className="grow">管理员密码</div><Badge level="good">已设置</Badge></div>
            <div className="rowline"><div className="grow">阿里云账号</div>{account ? <Badge level="good">{account.name}</Badge> : <Badge level="neutral">稍后添加</Badge>}</div>
            <div className="rowline"><div className="grow">Telegram 通知</div>{telegramOk ? <Badge level="good">已配置</Badge> : <Badge level="neutral">稍后配置</Badge>}</div>
            <p className="note" style={{ margin: 0 }}>
              下一步建议：到“实例”页，给流量计入免费额度的机器装上采集程序。装好之后，用量每 10 秒更新一次；没装的机器要等约 1 小时后的官方数据。
            </p>
            <div className="inline" style={{ justifyContent: "flex-end" }}>
              <button className="btn" onClick={() => { onDone(); navigate("/", true); }}>先看总览</button>
              <button className="btn primary" onClick={() => { onDone(); navigate("/instances", true); }}>去安装采集程序</button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function PasswordStep({ minPassword, onNext }: { minPassword: number; onNext: () => void }) {
  const [token, setToken] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [fields, setFields] = useState<Record<string, string>>({});

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const local: Record<string, string> = {};
    if (!token.trim()) local.setup_token = "required";
    if (pw.length < minPassword) local.password = "too_short";
    if (pw !== pw2) local.password2 = "mismatch";
    setFields(local);
    setError(undefined);
    if (Object.keys(local).length) return;
    setBusy(true);
    try {
      await api.completeSetup({ setup_token: token.trim(), password: pw });
      onNext();
    } catch (err) {
      if (err instanceof ApiError) {
        setFields(err.fields ?? {});
        setError(errorText(err.code));
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="card stack" onSubmit={submit} noValidate>
      <h2>欢迎使用。先设置管理员密码</h2>
      <div>
        <label className="lbl" htmlFor="su-token">初始化 token</label>
        <input id="su-token" type="text" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" spellCheck={false} aria-invalid={!!fields.setup_token} autoFocus />
        {fields.setup_token && <div className="field-error">{fieldErrorText(fields.setup_token)}</div>}
        <div className="field-help">hm-server 第一次启动时会在日志里打印这串字符，用来证明你就是装这台服务的人。它只能用一次。</div>
      </div>
      <div>
        <label className="lbl" htmlFor="su-pw">管理员密码</label>
        <input id="su-pw" type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="new-password" aria-invalid={!!fields.password} />
        {fields.password ? <div className="field-error">密码至少要 {minPassword} 位。</div> : <div className="field-help">至少 {minPassword} 位。以后登录面板就用它。</div>}
      </div>
      <div>
        <label className="lbl" htmlFor="su-pw2">再输入一次</label>
        <input id="su-pw2" type="password" value={pw2} onChange={(e) => setPw2(e.target.value)} autoComplete="new-password" aria-invalid={!!fields.password2} />
        {fields.password2 && <div className="field-error">两次输入的密码不一样。</div>}
      </div>
      {error && <div className="field-error" role="alert">{error}</div>}
      <button className="btn primary block" disabled={busy}>{busy && <Spinner />} 下一步</button>
      {MOCK && <p className="note" style={{ margin: 0 }}>模拟模式：token 随便填；填 wrong 可以看到出错的样子。</p>}
    </form>
  );
}

function AccountStep({ account, onAccount, onNext }: { account?: Account; onAccount: (a: Account) => void; onNext: () => void }) {
  const [jobId, setJobId] = useState<string>();
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState<string>();

  useEffect(() => {
    if (!jobId || !account) return;
    let live = true;
    setChecking(true);
    waitForJob(jobId).then(
      async (job) => {
        if (!live) return;
        if (job.status === "failed") setCheckError(errorText(job.error?.code ?? "internal_error"));
        const fresh = (await api.accounts()).find((a) => a.id === account.id);
        if (live && fresh) onAccount(fresh);
        if (live) setChecking(false);
      },
      () => live && (setChecking(false), setCheckError(errorText("job_timeout"))),
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId]);

  if (account)
    return (
      <div className="card stack">
        <h2>账号 {account.name} 已添加</h2>
        <div>
          <div className="sub" style={{ marginBottom: 6 }}>权限测试（只是试着读取，不会真的开关机或创建任何东西）</div>
          <PermissionList check={account.permission_check} pending={checking} />
          {checkError && <div className="field-error" style={{ marginTop: 6 }}>{checkError}</div>}
        </div>
        {!checking && account.permission_check?.items.some((i) => i.result !== "passed") && (
          <p className="note" style={{ margin: 0 }}>有几项没有通过。查看用量只需要“查看”类权限；缺少的权限可以之后在阿里云里补上，再到设置页重新测试。</p>
        )}
        <button className="btn primary block" onClick={onNext} disabled={checking}>下一步</button>
      </div>
    );
  return (
    <div className="card stack">
      <h2>添加第一个阿里云账号</h2>
      <p className="sub" style={{ margin: 0 }}>Harbormaster 用这组密钥读取你的实例和流量用量。之后还可以在设置里添加更多账号。</p>
      <AccountForm submitText="添加并测试权限" cancelText="先跳过" onCancel={onNext} onSaved={(a, job) => { onAccount(a); setJobId(job); }} />
    </div>
  );
}

function TelegramStep({ onOk, onNext }: { onOk: () => void; onNext: () => void }) {
  const res = useResource<TelegramChannel>(api.telegram);
  return (
    <div className="card stack">
      <h2>用量快到上限时，通知到哪里？</h2>
      <p className="sub" style={{ margin: 0 }}>目前支持 Telegram。不配置也能用，只是告警只会记录在面板里，不会主动提醒你。</p>
      <Async res={res}>
        {(tg) => <TelegramForm value={tg} onSaved={(v) => { res.mutate(() => v); if (v.bot_token_set) onOk(); }} />}
      </Async>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button className="btn" onClick={onNext}>{res.data?.bot_token_set ? "下一步" : "先跳过"}</button>
      </div>
    </div>
  );
}
