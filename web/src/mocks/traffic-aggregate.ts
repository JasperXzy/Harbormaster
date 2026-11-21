// Synthetic API responses only. Production aggregation is implemented in Go.
import type * as T from "../api/types";
import type { Db } from "./data";
import { quotaView } from "../lib/usage";

const sum=(values:number[])=>values.reduce((a,b)=>a+b,0);
export function mockQuotaTotals(db:Db):T.QuotaStatus[] {
  const accounts=db.accounts.filter(a=>a.enabled);
  if(!accounts.length)return [];
  const local=new Date(Date.now()+8*3600_000),cycleStart=new Date(Date.UTC(local.getUTCFullYear(),local.getUTCMonth(),1)-8*3600_000).toISOString(),cycleEnd=new Date(Date.UTC(local.getUTCFullYear(),local.getUTCMonth()+1,1)-8*3600_000).toISOString();
  return (["mainland","overseas"] as const).flatMap(scope=>{
    const scopedAccounts=accounts.filter(a=>scope==="mainland"?a.track_mainland!==false:a.track_overseas!==false);
    if(!scopedAccounts.length)return [];
    const rows=scopedAccounts.flatMap(a=>{const q=db.quotas.find(q=>q.account_id===a.id&&q.scope===scope);return q?[q]:[];});
    const views=rows.map(q=>quotaView(q)),ready=views.filter(v=>v.usable&&v.value!==null&&v.updatedAt!==null&&Date.now()-Date.parse(v.updatedAt)>=0&&Date.now()-Date.parse(v.updatedAt)<=30_000),official=rows.filter(q=>q.official_bytes!==null),forecasts=rows.flatMap(q=>q.forecast?[q.forecast]:[]),n=scopedAccounts.length;
    const now=new Date().toISOString(),m={account_count:n,official_accounts:official.length,estimated_accounts:ready.length,forecast_accounts:forecasts.length,known_official_bytes:sum(official.map(q=>q.official_bytes!)),known_estimated_bytes:sum(ready.map(v=>v.value!)),known_forecast_bytes:sum(forecasts.map(f=>f.cycle_end_bytes)),forecast_sample_days_min:forecasts.length?Math.min(...forecasts.map(f=>f.sample_days??0)):0,forecast_sample_days_max:forecasts.length?Math.max(...forecasts.map(f=>f.sample_days??0)):0,forecast_exceeding_accounts:forecasts.filter(f=>f.exceeds_on||f.already_exceeded).length};
    let forecast:T.QuotaStatus["forecast"]=null;
    if(forecasts.length===n){
      const first=forecasts[0],dates=first.sample_dates??[],same=forecasts.every(f=>JSON.stringify(f.sample_dates??[])===JSON.stringify(dates));
      const baseline_at=forecasts.map(f=>f.baseline_at??now).sort().at(-1)!,baseline=sum(forecasts.map(f=>f.baseline_bytes??0));
      const points=new Map<string,{at:string;date:string;bytes:number;count:number}>();
      for(const f of forecasts)for(const p of (f.points??[]).slice(1)){const v=points.get(p.date)??{...p,bytes:0,count:0};v.bytes+=p.bytes;v.count++;points.set(p.date,v);}
      forecast={daily_average_bytes:sum(forecasts.map(f=>f.daily_average_bytes)),cycle_end_bytes:m.known_forecast_bytes,exceeds_on:null,already_exceeded:false,sample_days:same?first.sample_days:undefined,sample_dates:same?dates:[],source:"mixed",unverified:true,baseline_bytes:baseline,baseline_at,baseline_source:"account_forecasts",baseline_at_assumed:true,calculated_at:now,points:[{at:baseline_at,date:(first.points?.[0]?.date??db.daily[scopedAccounts[0].id].days.at(-1)!.date),bytes:baseline},...[...points.values()].filter(p=>p.count===n).sort((a,b)=>a.date.localeCompare(b.date))],reasons:["independent_account_forecasts","quotas_not_transferable"]};
    }
    const template:T.QuotaStatus=rows[0]??{account_id:"all",scope,cycle_start:cycleStart,cycle_end:cycleEnd,quota_bytes:0,official_bytes:null,official_as_of:null,official_status:"never_fetched",realtime_increment_bytes:null,worst_case_bytes:null,estimated_bytes:null,effective_bytes:null,thresholds_percent:[],forecast:null,updated_at:null};
    return [{...template,account_id:"all",aggregation:m,reconciliation:null,official_bytes:official.length===n?m.known_official_bytes:null,official_as_of:null,official_status:official.length===n?"cutoff_unknown":"unavailable",official_quality:"inferred",quota_bytes:sum(scopedAccounts.map(a=>scope==="mainland"?a.quota_mainland_bytes:a.quota_overseas_bytes)),thresholds_percent:[],effective_bytes:null,estimated_bytes:null,realtime_increment_bytes:null,worst_case_bytes:null,updated_at:now,forecast,forecast_status:forecast?"ready":"insufficient_data",estimation:{mode:"unverified",status:ready.length===n?"ready":"incomplete",basis_id:"",from_at:null,to_at:now,calculated_at:now,lag_window_seconds:0,increment_bytes:ready.length===n?sum(ready.map(v=>v.increment??0)):null,known_increment_bytes:sum(views.map(v=>v.knownIncrement)),worst_case_bytes:ready.length===n?sum(ready.map(v=>v.worstCase??0)):null,estimated_bytes:ready.length===n?m.known_estimated_bytes:null,current_rate_bytes_per_second:null,qualified_for_execution:false,reasons:ready.length===n?["account_aggregation"]:["account_data_incomplete"]}}];
  });
}

export function mockDailyTotal(db:Db):T.DailyTraffic {
  const n=db.accounts.filter(a=>a.enabled).length;
  const rows=db.accounts.filter(a=>a.enabled).flatMap(a=>db.daily[a.id]?[db.daily[a.id]]:[]);
  const first=rows[0]??Object.values(db.daily)[0];
  if(!first)return {account_id:"all",data_timezone:"Asia/Shanghai",cycle_start:new Date().toISOString(),cycle_end:new Date().toISOString(),days:[],updated_at:null,aggregation:{account_count:n}};
  if(n===0)return {...first,account_id:"all",aggregation:{account_count:0},days:[],official_points:{mainland:[],overseas:[]}};
  const days=first.days.map(day=>{
    const scopes={} as NonNullable<typeof day.scopes>;
    for(const scope of ["mainland","overseas"] as const){
      const parts=rows.flatMap(r=>{const d=r.days.find(x=>x.date===day.date)?.scopes?.[scope];return d?[d]:[];}),observed=parts.filter(s=>s.has_data),complete=parts.length===n&&parts.every(s=>s.bytes!==null&&s.status==="complete"),known=sum(observed.map(s=>s.known_bytes)),sources=new Set(observed.map(s=>s.source));
      const fallback={from_at:day.date+"T00:00:00+08:00",until_at:day.date+"T00:00:00+08:00"};
      scopes[scope]={...(parts[0]??fallback),bytes:complete?known:null,known_bytes:known,has_data:observed.length>0,source:sources.size>1?"mixed":observed[0]?.source??"unavailable",status:complete?"complete":observed.length?"partial":"unavailable",is_complete_day:complete&&parts.every(s=>s.is_complete_day),observed_from:observed.flatMap(s=>s.observed_from?[s.observed_from]:[]).sort()[0]??null,observed_until:observed.flatMap(s=>s.observed_until?[s.observed_until]:[]).sort().at(-1)??null,reasons:complete?["account_aggregation"]:["account_data_incomplete"]};
    }
    return {...day,scopes,mainland_bytes:scopes.mainland.bytes,overseas_bytes:scopes.overseas.bytes,provisional:true};
  });
  const official_points={} as NonNullable<T.DailyTraffic["official_points"]>;
  for(const scope of ["mainland","overseas"] as const)official_points[scope]=(first.official_points?.[scope]??[]).flatMap(p=>{
    const values=rows.map(r=>r.official_points?.[scope].find(x=>x.date===p.date));return values.length===n&&values.every(x=>x!==undefined)?[{...p,bytes:sum(values.map(x=>x!.bytes))}]:[];
  });
  return {...first,account_id:"all",aggregation:{account_count:n},days,official_points,source_status:"partial"};
}
