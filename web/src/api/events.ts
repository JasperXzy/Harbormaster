// 事件流（SSE）。全局只开一条连接；组件用 useEvent 订阅。
import { useEffect, useRef, useSyncExternalStore } from "react";
import type { EventMap, EventName } from "./types";

type Listener = (name: EventName, data: unknown) => void;
export type ConnState = "idle" | "connecting" | "open" | "reconnecting";

const listeners = new Set<Listener>();
const stateListeners = new Set<() => void>();
let state: ConnState = "idle";
let stop: (() => void) | null = null;

const NAMES: EventName[] = ["quota.updated", "instance.realtime", "agent.status", "job.updated", "notification.created", "ping"];

function setState(s: ConnState) {
  if (s === state) return;
  state = s;
  stateListeners.forEach((fn) => fn());
}
const emit: Listener = (name, data) => listeners.forEach((fn) => fn(name, data));

export async function connectEvents() {
  if (stop) return;
  setState("connecting");
  if (import.meta.env.VITE_API_MODE === "mock") {
    const { subscribeMock } = await import("../mocks/server");
    stop = subscribeMock(emit, (open) => setState(open ? "open" : "reconnecting"));
    return;
  }
  const es = new EventSource("/api/v1/events", { withCredentials: true });
  es.onopen = () => setState("open");
  es.onerror = () => setState("reconnecting"); // 浏览器会自动重连
  for (const name of NAMES) {
    es.addEventListener(name, (ev) => {
      try {
        emit(name, JSON.parse((ev as MessageEvent).data));
      } catch {
        /* 忽略格式不对的消息 */
      }
    });
  }
  stop = () => es.close();
}

export function disconnectEvents() {
  stop?.();
  stop = null;
  setState("idle");
}

export function subscribe<K extends EventName>(name: K, fn: (data: EventMap[K]) => void): () => void {
  const l: Listener = (n, d) => {
    if (n === name) fn(d as EventMap[K]);
  };
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useEvent<K extends EventName>(name: K, fn: (data: EventMap[K]) => void) {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => subscribe(name, (d) => ref.current(d)), [name]);
}

export function useConnState(): ConnState {
  return useSyncExternalStore(
    (cb) => {
      stateListeners.add(cb);
      return () => stateListeners.delete(cb);
    },
    () => state,
  );
}
