import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError, MOCK } from "../api/client";
import { errorText } from "../api/labels";
import { Logo } from "../components/Shell";
import { Spinner } from "../components/ui";
import { getPasskeyAssertion, passkeySupported } from "../lib/webauthn";

export function Login({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [passwordEnabled, setPasswordEnabled] = useState(true);
  const [passkeyEnabled, setPasskeyEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [pkBusy, setPkBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [wait, setWait] = useState(0); // 登录限速的剩余秒数

  useEffect(() => {
    api.loginMethods().then(
      (m) => {
        setPasswordEnabled(m.password_enabled);
        setPasskeyEnabled(m.passkey_enabled);
      },
      () => {},
    );
  }, []);

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

  const loginWithPasskey = async () => {
    if (!passkeySupported()) {
      setError("当前浏览器不支持 Passkey，请改用密码登录或更换浏览器。");
      return;
    }
    setPkBusy(true);
    setError(undefined);
    try {
      const begin = await api.beginPasskeyLogin();
      const assertion = await getPasskeyAssertion(begin.options);
      await api.finishPasskeyLogin(begin.ceremony_id, assertion);
      onDone();
    } catch (err) {
      if (err instanceof DOMException && err.name === "NotAllowedError") {
        setError("已取消 Passkey 验证。");
      } else {
        const code = err instanceof ApiError ? err.code : "invalid_passkey";
        setError(errorText(code));
        if (err instanceof ApiError && err.retryAfterSeconds) setWait(err.retryAfterSeconds);
      }
    } finally {
      setPkBusy(false);
    }
  };

  return (
    <div className="solo">
      <div className="solo-box">
        <div className="brand"><Logo size={28} /><span>Harbormaster</span></div>
        <div className="card stack">
          {passkeyEnabled && (
            <button type="button" className={`btn ${passwordEnabled ? "" : "primary"} block`} disabled={pkBusy || busy || wait > 0} onClick={loginWithPasskey}>
              {pkBusy && <Spinner />} 使用 Passkey 指纹 / 安全密钥登录
            </button>
          )}

          {passwordEnabled ? (
            <form className="stack" onSubmit={submit} noValidate>
              <div>
                <label className="lbl" htmlFor="login-pw">管理员密码</label>
                <input id="login-pw" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus aria-invalid={!!error} />
              </div>
              {error && <div className="field-error" role="alert">{error}{wait > 0 ? ` 请在 ${wait} 秒后再试。` : ""}</div>}
              <button className="btn primary block" disabled={busy || pkBusy || wait > 0}>{busy && <Spinner />} 密码登录</button>
            </form>
          ) : (
            <>
              {error && <div className="field-error" role="alert">{error}{wait > 0 ? ` 请在 ${wait} 秒后再试。` : ""}</div>}
              <p className="sub" style={{ margin: 0 }}>已关闭密码登录。若安全密钥丢失，请登录服务器执行 <span className="mono">hm-server reset-password</span> 恢复密码登录。</p>
            </>
          )}

          {passwordEnabled && (
            <p className="note" style={{ margin: 0 }}>
              忘记密码？网页上不能重置。请登录运行 Harbormaster 的服务器，执行 <span className="mono">hm-server reset-password</span>。
            </p>
          )}
        </div>
        {MOCK && <p className="note" style={{ textAlign: "center", margin: 0 }}>模拟模式：随便输入 4 位以上的密码或点击 Passkey 即可登录；输入 wrong 可以看到出错的样子。</p>}
      </div>
    </div>
  );
}
