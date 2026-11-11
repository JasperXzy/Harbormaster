// 等待一个异步任务结束：优先听事件流，同时每 1.5 秒轮询一次兜底。
import { api } from "./client";
import { subscribe } from "./events";
import type { Job } from "./types";

export function waitForJob(jobId: string, timeoutMs = 60_000): Promise<Job> {
  return new Promise((resolve, reject) => {
    let done = false;
    const finish = (job: Job) => {
      if (done || (job.status !== "succeeded" && job.status !== "failed")) return;
      done = true;
      off();
      clearInterval(poll);
      clearTimeout(timer);
      resolve(job);
    };
    const off = subscribe("job.updated", (job) => {
      if (job.id === jobId) finish(job);
    });
    const poll = setInterval(() => {
      api.job(jobId).then(finish, () => {});
    }, 1500);
    const timer = setTimeout(() => {
      if (done) return;
      done = true;
      off();
      clearInterval(poll);
      reject(new Error("job_timeout"));
    }, timeoutMs);
    api.job(jobId).then(finish, () => {});
  });
}
