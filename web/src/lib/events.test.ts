import { describe, expect, it } from "vitest";
import type { RunEvent } from "../api/types";
import { line, matrix } from "./events";

const ev = (seq: number, type: string, extra: Partial<RunEvent> = {}): RunEvent => ({ seq, stage: "configure", type, createdAt: "2026-10-25T19:21:35Z", ...extra });

describe("events", () => {
  const events = [
    ev(1, "playbook_on_task_start", { task: "facts" }),
    ev(2, "runner_on_ok", { host: "u1", task: "facts" }),
    ev(3, "runner_on_ok", { host: "u2", task: "facts" }),
    ev(4, "runner_on_ok", { host: "u2", task: "timezone", changed: true }),
    ev(5, "runner_on_ok", { host: "u1", task: "timezone" }),
    ev(6, "runner_on_failed", { host: "u1", task: "ssh", data: { res: { msg: "boom" } } }),
    ev(7, "runner_on_skipped", { host: "u2", task: "ssh" }),
    ev(8, "log", { stage: "provision", stdout: "+ vm created\n" }),
  ];

  it("builds the server × task matrix", () => {
    const m = matrix(events);
    expect(m.tasks).toEqual(["facts", "timezone", "ssh"]);
    expect(m.hosts).toEqual(["u1", "u2"]);
    expect(m.cells.u1).toEqual({ facts: "ok", timezone: "ok", ssh: "failed" });
    expect(m.cells.u2).toEqual({ facts: "ok", timezone: "changed", ssh: "skipped" });
    expect(m.summary.u2).toEqual({ ok: 2, changed: 1, failed: 0 });
    expect(m.summary.u1).toEqual({ ok: 2, changed: 0, failed: 1 });
  });

  it("writes log lines with hostnames", () => {
    const hosts = { u1: "labtpoc01", u2: "labtpoc02" };
    expect(line(events[0]!, hosts)?.text).toBe("TASK [facts]");
    expect(line(events[3]!, hosts)).toMatchObject({ kind: "changed", text: "changed: [labtpoc02]" });
    expect(line(events[5]!, hosts)).toMatchObject({ kind: "failed", text: "failed: [labtpoc01] boom" });
    expect(line(events[6]!, hosts)).toBeNull();
    expect(line(events[7]!, hosts)).toMatchObject({ kind: "log", stage: "provision", text: "+ vm created" });
  });
});
