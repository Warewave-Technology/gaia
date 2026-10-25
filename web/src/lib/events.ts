// Reading a run's events (ansible-runner's JSON events and Gaia's logs):
// the server × task matrix and the log lines.
import type { RunEvent } from "../api/types";

export type Mark = "ok" | "changed" | "failed" | "skipped" | "unreachable";

export type Matrix = {
  tasks: string[];
  hosts: string[];
  cells: Record<string, Record<string, Mark>>; // host → task → mark
  summary: Record<string, { ok: number; changed: number; failed: number }>;
};

const RESULT: Record<string, Mark> = {
  runner_on_ok: "ok",
  runner_on_failed: "failed",
  runner_on_skipped: "skipped",
  runner_on_unreachable: "unreachable",
};

// The worst mark wins when a task ran more than once on a host (loops).
const RANK: Record<Mark, number> = { skipped: 0, ok: 1, changed: 2, failed: 3, unreachable: 4 };

export function matrix(events: RunEvent[]): Matrix {
  const m: Matrix = { tasks: [], hosts: [], cells: {}, summary: {} };
  for (const e of events) {
    let mark = RESULT[e.type];
    if (!mark || !e.host || !e.task) continue;
    if (mark === "ok" && e.changed) mark = "changed";
    if (!m.tasks.includes(e.task)) m.tasks.push(e.task);
    if (!m.hosts.includes(e.host)) {
      m.hosts.push(e.host);
      m.cells[e.host] = {};
      m.summary[e.host] = { ok: 0, changed: 0, failed: 0 };
    }
    const row = m.cells[e.host]!;
    const prev = row[e.task];
    if (!prev || RANK[mark] > RANK[prev]) row[e.task] = mark;
  }
  for (const h of m.hosts) {
    const s = m.summary[h]!;
    for (const mark of Object.values(m.cells[h]!)) {
      if (mark === "ok") s.ok++;
      else if (mark === "changed") {
        s.ok++;
        s.changed++;
      } else if (mark === "failed" || mark === "unreachable") s.failed++;
    }
  }
  return m;
}

export type Line = { seq: number; at: string; stage: string; kind: "log" | "task" | "ok" | "changed" | "failed" | "skipped" | "other"; text: string };

function msg(e: RunEvent): string {
  const res = (e.data?.res ?? {}) as { msg?: unknown };
  return typeof res.msg === "string" ? res.msg : "";
}

// line turns an event into one log line; events with nothing to say are
// left out.
export function line(e: RunEvent, hosts: Record<string, string> = {}): Line | null {
  const host = e.host ? hosts[e.host] ?? e.host : "";
  const base = { seq: e.seq, at: e.createdAt, stage: e.stage };
  switch (e.type) {
    case "log":
      return { ...base, kind: "log", text: (e.stdout ?? "").trimEnd() };
    case "playbook_on_task_start":
      return { ...base, kind: "task", text: `TASK [${e.task ?? ""}]` };
    case "playbook_on_handler_task_start":
      return { ...base, kind: "task", text: `RUNNING HANDLER [${e.task ?? ""}]` };
    case "runner_on_ok":
      return { ...base, kind: e.changed ? "changed" : "ok", text: `${e.changed ? "changed" : "ok"}: [${host}]` };
    case "runner_on_failed":
    case "runner_on_unreachable": {
      const m = msg(e);
      return { ...base, kind: "failed", text: `${e.type === "runner_on_unreachable" ? "unreachable" : "failed"}: [${host}]${m ? " " + m : ""}` };
    }
    case "runner_on_skipped":
      return null;
    default: {
      const out = (e.stdout ?? "").trim();
      return out ? { ...base, kind: "other", text: out } : null;
    }
  }
}
