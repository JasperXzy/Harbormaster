// 多个页面共用的数据：账号、实例、额度。事件流推来的更新直接合并进来，页面不用各自订阅。
import { createContext, useContext, useEffect, useRef, type ReactNode } from "react";
import { api } from "../api/client";
import { useConnState, useEvent } from "../api/events";
import type { Account, Instance, QuotaStatus } from "../api/types";
import { useResource, type Resource } from "./useResource";

interface InstanceList {
  items: Instance[];
  updated_at: string | null;
}
interface Shared {
  accounts: Resource<Account[]>;
  instances: Resource<InstanceList>;
  quotas: Resource<QuotaStatus[]>;
  reloadAll: () => void;
}
const Ctx = createContext<Shared | null>(null);

// 实例很少（PRD 按 10 台估算），一次取完；将来变多再改成按页加载。
async function allInstances(): Promise<InstanceList> {
  const items: Instance[] = [];
  let cursor: string | undefined;
  let updated_at: string | null = null;
  do {
    const page = await api.instances({ limit: 200, cursor });
    items.push(...page.items);
    updated_at = page.updated_at;
    cursor = page.next_cursor ?? undefined;
  } while (cursor);
  return { items, updated_at };
}

export function DataProvider({ children }: { children: ReactNode }) {
  const accounts = useResource(api.accounts, [], { refreshMs: 60_000 });
  const instances = useResource(allInstances, [], { refreshMs: 60_000 });
  const quotas = useResource(() => api.quotas(), [], { refreshMs: 60_000 });
  const reloadAll = () => {
    accounts.reload();
    instances.reload();
    quotas.reload();
  };

  useEvent("quota.updated", (q) => quotas.mutate((list) => list.map((x) => (x.account_id === q.account_id && x.scope === q.scope ? q : x))));
  useEvent("instance.realtime", (e) => instances.mutate((l) => ({ ...l, items: l.items.map((i) => (i.id === e.instance_id ? { ...i, realtime: e.realtime, agent: { ...i.agent, last_report_at: e.realtime.sampled_at } } : i)) })));
  useEvent("agent.status", (e) => instances.mutate((l) => ({ ...l, items: l.items.map((i) => (i.id === e.instance_id ? { ...i, agent: e.agent, realtime_protection: e.realtime_protection, realtime: e.agent.status === "online" ? i.realtime : null } : i)) })));

  // 事件流断开后重新连上：期间可能漏了事件，整体重新取一次
  const conn = useConnState();
  const wasDown = useRef(false);
  useEffect(() => {
    if (conn === "reconnecting") wasDown.current = true;
    if (conn === "open" && wasDown.current) {
      wasDown.current = false;
      reloadAll();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn]);

  return <Ctx.Provider value={{ accounts, instances, quotas, reloadAll }}>{children}</Ctx.Provider>;
}

export function useShared(): Shared {
  const v = useContext(Ctx);
  if (!v) throw new Error("useShared must be used inside DataProvider");
  return v;
}
