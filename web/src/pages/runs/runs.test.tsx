import { fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

// A stand-in EventSource the test feeds by hand.
class FakeES {
  static last: FakeES | null = null;
  static CLOSED = 2;
  url: string;
  readyState = 1;
  onerror: (() => void) | null = null;
  private handlers: Record<string, ((m: MessageEvent<string>) => void)[]> = {};
  constructor(url: string) {
    this.url = url;
    FakeES.last = this;
  }
  addEventListener(name: string, h: (m: MessageEvent<string>) => void) {
    (this.handlers[name] ??= []).push(h);
  }
  emit(name: string, data: unknown) {
    for (const h of this.handlers[name] ?? []) h({ data: JSON.stringify(data) } as MessageEvent<string>);
  }
  close() {
    this.readyState = 2;
  }
}

const RUN = {
  id: "run-s62", contracts: ["poc-web"], commit: "3f9c2e1aaaa", planHash: "sha256:668ada7b5c0e4f1a", status: "running", requestedBy: "mehmet.y",
  queuedAt: "2026-10-25T19:17:58Z", startedAt: "2026-10-25T19:18:00Z", eeImage: "ee-default@sha256:92d2ac37", configCommit: "3f9c2e1bbbb",
  stages: [
    { contract: "poc-web", stage: "ipam", status: "succeeded", startedAt: "2026-10-25T19:18:00Z", finishedAt: "2026-10-25T19:18:01Z" },
    { contract: "poc-web", stage: "configure", status: "running", startedAt: "2026-10-25T19:21:35Z" },
  ],
  change: "ch1", hosts: { u1: "labtpoc01", u2: "labtpoc02" },
};

beforeEach(() => {
  vi.stubGlobal("EventSource", FakeES);
  fakeAPI({
    "GET /me": { body: ME },
    "GET /changes?open=true&limit=500": { body: [] },
    "GET /runs/run-s62": { body: { ...RUN, record: { seq: 4, recordHash: "sha256:c844a409", prevHash: "sha256:b2e3faf6" } } },
    "GET /runs?limit=200": { body: [{ ...RUN, status: "succeeded", finishedAt: "2026-10-25T19:22:12Z" }, { id: "run-x", contracts: ["billing-db"], status: "failed", queuedAt: RUN.queuedAt, restricted: true }] },
    "GET /runs/run-s62/record": { body: { seq: 4, body: { approvedBy: ["ayse.k"], changes: [
      { contract: "poc-web", uid: "u2", hostname: "labtpoc02", stage: "plan", action: "create" },
      { contract: "poc-web", stage: "provision", action: "pulumi up", detail: "poc-web-lab: create 3" },
    ] } } },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("runs", () => {
  it("follows a run live: log lines with hostnames and the matrix", async () => {
    renderAt("/runs/run-s62");
    expect(await screen.findByRole("heading", { name: "run-s62" })).toBeInTheDocument();
    expect(FakeES.last?.url).toBe("/api/v1/runs/run-s62/events/stream");
    expect(screen.getByRole("link", { name: "Change" })).toHaveAttribute("href", "/changes/ch1");
    FakeES.last!.emit("ansible", { seq: 1, stage: "configure", type: "playbook_on_task_start", task: "gaia_linux_base : Saat dilimi", createdAt: RUN.startedAt });
    FakeES.last!.emit("ansible", { seq: 2, stage: "configure", type: "runner_on_ok", host: "u2", task: "gaia_linux_base : Saat dilimi", changed: true, createdAt: RUN.startedAt });
    FakeES.last!.emit("ansible", { seq: 2, stage: "configure", type: "runner_on_ok", host: "u2", task: "gaia_linux_base : Saat dilimi", changed: true, createdAt: RUN.startedAt });
    expect(await screen.findByText("changed: [labtpoc02]")).toBeInTheDocument();
    expect(screen.getAllByText("changed: [labtpoc02]")).toHaveLength(1);
    expect(screen.getByTitle("gaia_linux_base : Saat dilimi: changed")).toBeInTheDocument();
    expect(screen.getByText("labtpoc02", { selector: "td" })).toBeInTheDocument();
    expect(await screen.findByText(/^1 servers?$/)).toBeInTheDocument();
    expect(screen.getByText("ayse.k")).toBeInTheDocument();
    FakeES.last!.emit("end", { status: "succeeded" });
    expect(FakeES.last!.readyState).toBe(2);
  });

  it("lists runs with the record's shield on finished ones", async () => {
    renderAt("/runs");
    expect(await screen.findByRole("link", { name: "run-s62" })).toBeInTheDocument();
    expect(screen.getAllByRole("img", { name: "Run record" })).toHaveLength(1);
    expect(screen.getByText("(no access)")).toBeInTheDocument();
    fireEvent.mouseEnter(screen.getByRole("img", { name: "Run record" }).parentElement!.parentElement!);
    // The tooltip reads the run only when it opens.
    expect(await screen.findByText("chain #4")).toBeInTheDocument();
  });
});
