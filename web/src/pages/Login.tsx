import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError, MOCK } from "../api/client";
import { errorText } from "../api/labels";
import { Logo } from "../components/Shell";
import { Spinner } from "../components/ui";

export function Login({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [wait, setWait] = useState(0); // 登录限速的剩余秒数

  useEffect(() => {
    if (wait <= 0) return;
    const t = setTimeout(() => setWait((w) => w - 1), 1000);
    return () => clearTimeout(t);
  }, [wait]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!password) return setError("请输入密码。");
    setBusy(true);
    setError(undefined);
    try {
      await api.login(password);
      onDone();
    } catch (err) {
      const code = err instanceof ApiError ? err.code : "internal_error";
      setError(errorText(code));
      if (err instanceof ApiError && err.retryAfterSeconds) setWait(err.retryAfterSeconds);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="solo">
      <div className="solo-box">
        <div className="brand"><Logo size={28} /><span>Harbormaster</span></div>
        <form className="card stack" onSubmit={submit} noValidate>
          <div>
            <label className="lbl" htmlFor="login-pw">管理员密码</label>
            <input id="login-pw" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus aria-invalid={!!error} />
            {error && <div className="field-error" role="alert">{error}{wait > 0 ? ` 请在 ${wait} 秒后再试。` : ""}</div>}
          </div>
          <button className="btn primary block" disabled={busy || wait > 0}>{busy && <Spinner />} 登录</button>
          <p className="note" style={{ margin: 0 }}>
            忘记密码？网页上不能重置。请登录运行 Harbormaster 的服务器，执行 <span className="mono">hm-server reset-password</span>。
          </p>
        </form>
        {MOCK && <p className="note" style={{ textAlign: "center", margin: 0 }}>模拟模式：随便输入 4 位以上的密码即可登录；输入 wrong 可以看到出错的样子。</p>}
      </div>
    </div>
  );
}
