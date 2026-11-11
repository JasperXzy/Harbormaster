// Telegram 渠道表单（安装向导与通知页共用）。
import { useState, type FormEvent } from "react";
import { api, ApiError } from "../api/client";
import { waitForJob } from "../api/jobs";
import { errorText, fieldErrorText } from "../api/labels";
import type { TelegramChannel } from "../api/types";
import { Spinner, useToast } from "./ui";

export function TelegramForm({ value, onSaved }: { value: TelegramChannel; onSaved: (v: TelegramChannel) => void }) {
  const toast = useToast();
  const [token, setToken] = useState("");
  const [chat, setChat] = useState(value.chat_id);
  const [mode, setMode] = useState(value.proxy_mode);
  const [url, setUrl] = useState(value.proxy_url);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState<string>();
  const fe = (k: string) => (fields[k] ? <div className="field-error">{fieldErrorText(fields[k])}</div> : null);
  const dirty = token !== "" || chat !== value.chat_id || mode !== value.proxy_mode || url !== value.proxy_url;

  const save = async (e?: FormEvent) => {
    e?.preventDefault();
    setBusy("save");
    setError(undefined);
    setFields({});
    try {
      const saved = await api.updateTelegram({ ...(token ? { bot_token: token } : {}), chat_id: chat.trim(), proxy_mode: mode, proxy_url: mode === "none" ? "" : url.trim() });
      setToken("");
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
        <select id="tg-proxy" value={mode} onChange={(e) => setMode(e.target.value as TelegramChannel["proxy_mode"])}>
          <option value="none">不使用</option>
          <option value="reverse_proxy">自建的 Telegram 反向代理</option>
          <option value="socks5">SOCKS5 代理</option>
        </select>
        <div className="field-help">服务器在中国内地时连不上 Telegram，需要选一种代理。</div>
      </div>
      {mode !== "none" && (
        <>
          <label htmlFor="tg-url">代理地址</label>
          <div>
            <input id="tg-url" type="text" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={mode === "socks5" ? "socks5://127.0.0.1:1080" : "https://tg.example.com"} aria-invalid={!!fields.proxy_url} autoComplete="off" />
            {fe("proxy_url")}
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
