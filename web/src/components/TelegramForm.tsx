// Telegram 渠道表单（安装向导与通知页共用）。
import { useState, type FormEvent } from "react";
import { api, ApiError } from "../api/client";
import { waitForJob } from "../api/jobs";
import { errorText, fieldErrorText } from "../api/labels";
import type { TelegramChannel } from "../api/types";
import { Select, Spinner, useToast } from "./ui";

export function TelegramForm({ value, onSaved }: { value: TelegramChannel; onSaved: (v: TelegramChannel) => void }) {
  const toast = useToast();
  const [token, setToken] = useState("");
  const [chat, setChat] = useState(value.chat_id);
  const [mode, setMode] = useState(value.proxy_mode);
  const [url, setUrl] = useState(value.proxy_url);
  const [proxyUser, setProxyUser] = useState("");
  const [proxyPass, setProxyPass] = useState("");
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState<string>();
  const fe = (k: string) => (fields[k] ? <div className="field-error">{fieldErrorText(fields[k])}</div> : null);
  const dirty = token !== "" || chat !== value.chat_id || mode !== value.proxy_mode || url !== value.proxy_url || proxyUser !== "" || proxyPass !== "";
  // 后端规则：代理模式和地址都没变时，留空的认证保留；任一变化就清除旧认证。
  const authKept = value.proxy_auth_set && mode === value.proxy_mode && url.trim() === value.proxy_url;

  const save = async (e?: FormEvent) => {
    e?.preventDefault();
    setBusy("save");
    setError(undefined);
    setFields({});
    try {
      const auth = mode !== "none" && (proxyUser || proxyPass) ? { proxy_username: proxyUser, proxy_password: proxyPass } : {};
      const saved = await api.updateTelegram({ ...(token ? { bot_token: token } : {}), chat_id: chat.trim(), proxy_mode: mode, proxy_url: mode === "none" ? "" : url.trim(), ...auth });
      setToken("");
      setProxyUser("");
      setProxyPass("");
      onSaved(saved);
      toast("Telegram 设置已保存");
      return true;
    } catch (err) {
      if (err instanceof ApiError) {
        setFields(err.fields ?? {});
        setError(errorText(err.code));
      }
      return false;
    } finally {
      setBusy(null);
    }
  };

  const test = async () => {
    if (dirty && !(await save())) return;
    setBusy("test");
    setError(undefined);
    try {
      const { job_id } = await api.testTelegram();
      const job = await waitForJob(job_id);
      if (job.status === "succeeded") toast("测试消息已发出，请到 Telegram 里看一下");
      else setError(errorText(job.error?.code ?? "internal_error"));
      onSaved(await api.telegram());
    } catch (err) {
      setError(errorText(err instanceof ApiError ? err.code : err instanceof Error ? err.message : "internal_error"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <form className="form" onSubmit={save} noValidate>
      <label htmlFor="tg-token">机器人 Token</label>
      <div>
        <input id="tg-token" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder={value.bot_token_set ? "已保存（留空表示不修改）" : "从 @BotFather 获得"} aria-invalid={!!fields.bot_token} autoComplete="new-password" />
        {fe("bot_token")}
      </div>
      <label htmlFor="tg-chat">对话 ID（Chat ID）</label>
      <div>
        <input id="tg-chat" type="text" value={chat} onChange={(e) => setChat(e.target.value)} placeholder="例如 -1001234567890" aria-invalid={!!fields.chat_id} autoComplete="off" />
        {fe("chat_id")}
        <div className="field-help">通知会发到这个对话。先把机器人加进对话，再填这里。</div>
      </div>
      <label htmlFor="tg-proxy">代理</label>
      <div>
        <Select<TelegramChannel["proxy_mode"]>
          id="tg-proxy"
          value={mode}
          onChange={setMode}
          ariaLabel="代理模式"
          options={[
            { value: "none", label: "不使用" },
            { value: "reverse_proxy", label: "自建的 Telegram 反向代理" },
            { value: "socks5", label: "SOCKS5 代理" },
          ]}
        />
      </div>
      {mode !== "none" && (
        <>
          <label htmlFor="tg-url">代理地址</label>
          <div>
            <input id="tg-url" type="text" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={mode === "socks5" ? "socks5://127.0.0.1:1080" : "https://tg.example.com"} aria-invalid={!!fields.proxy_url} autoComplete="off" />
            {fe("proxy_url")}
          </div>
          <label htmlFor="tg-proxy-user">代理认证</label>
          <div>
            <div className="inline">
              <input id="tg-proxy-user" type="text" value={proxyUser} onChange={(e) => setProxyUser(e.target.value)} placeholder={authKept ? "已保存（留空不修改）" : "用户名（可不填）"} aria-label="代理用户名" aria-invalid={!!fields.proxy_username} autoComplete="off" />
              <input id="tg-proxy-pass" type="password" value={proxyPass} onChange={(e) => setProxyPass(e.target.value)} placeholder={authKept ? "已保存（留空不修改）" : "密码"} aria-label="代理密码" aria-invalid={!!fields.proxy_password} autoComplete="new-password" />
            </div>
            {fe("proxy_username")}
            {fe("proxy_password")}
            {value.proxy_auth_set && !authKept && <div className="field-help">代理地址改了，保存后会清除之前的用户名和密码。</div>}
          </div>
        </>
      )}
      <span />
      <div>
        {error && <div className="field-error" role="alert" style={{ marginBottom: 8 }}>{error}</div>}
        <div className="inline">
          <button className="btn primary" disabled={busy !== null || !dirty}>{busy === "save" && <Spinner />} 保存</button>
          <button type="button" className="btn" onClick={test} disabled={busy !== null || (!value.bot_token_set && !token)}>{busy === "test" && <Spinner />} 发送测试消息</button>
        </div>
      </div>
    </form>
  );
}
