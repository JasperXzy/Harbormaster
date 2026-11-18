import test from "node:test";
import assert from "node:assert/strict";
import { dailyValue, forecastNote, usageTimeline } from "../src/lib/daily.ts";

test("partial daily measurements are visible without becoming full-day totals",()=>{
  const day={date:"2026-11-18",mainland_bytes:null,overseas_bytes:null,scopes:{overseas:{bytes:null,known_bytes:123,has_data:true,source:"agent_estimate",status:"partial",is_complete_day:false,reasons:["coverage_gap"],observed_from:null,observed_until:null}}};
  assert.equal(dailyValue(day,"overseas").value,123);
  assert.equal(dailyValue(day,"overseas").partial,true);
  assert.equal(day.overseas_bytes,null);
  day.scopes.overseas.has_data=false;
  assert.equal(dailyValue(day,"overseas").value,null);
  day.scopes.overseas.has_data=true;day.scopes.overseas.bytes=0;day.scopes.overseas.is_complete_day=true;day.scopes.overseas.status="complete";
  assert.equal(dailyValue(day,"overseas").value,0);
  assert.equal(dailyValue(day,"overseas").partial,false);
});

test("official reading points and supplied forecasts survive an unknown month prefix",()=>{
  const now=Date.parse("2026-11-18T12:00:00Z");
  const d={cycle_start:"2026-11-01T00:00:00Z",cycle_end:"2026-12-01T00:00:00Z",days:[{date:"2026-11-01",overseas_bytes:null},{date:"2026-11-18",overseas_bytes:20}],official_points:{overseas:[{date:"2026-11-18",bytes:100}]}};
  const q={effective_bytes:120,forecast:{daily_average_bytes:99999,cycle_end_bytes:444,sample_days:1,unverified:true,points:[{date:"2026-11-18",bytes:120},{date:"2026-11-18",bytes:130},{date:"2026-11-30",bytes:444}]}};
  const s=usageTimeline(d,q,"overseas",now);
  assert.equal(s.collected.every(x=>x===null),true);
  assert.equal(s.official.includes(100),true);
  assert.equal(s.realtime.includes(120),true);
  assert.deepEqual(s.projected.filter(x=>x!==null),[120,130,444]);
  assert.match(forecastNote(q),/1 个完整日/);
});

test("forecast waiting states distinguish missing samples from missing baseline",()=>{
  assert.match(forecastNote({forecast:null,forecast_status:"insufficient_data"}),/今天不参加日均/);
  assert.match(forecastNote({forecast:null,forecast_status:"baseline_missing"}),/累计基准/);
});
