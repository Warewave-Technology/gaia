import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

const HOST = { uid: "xfaoec2tq4ekespr", hostname: "labtpoc01", site: "lab", network: "lab-poc", status: "active", externalIds: { "pve-lab/vm": "9400", "ipam/ip": "10.50.0.100", "zabbix/host": "10684" } };
const PLAN = (servers: unknown[]) => ({ contracts: [{ name: "poc-web", image: "rocky9", size: { name: "small", cpu: 2, memoryGiB: 2, diskGiB: 20 }, baseline: { name: "linux-base" }, monitoring: { provider: "zabbix" }, servers }] });
const KEEP = { action: "keep", uid: HOST.uid, hostname: "labtpoc01", site: "lab", network: "lab-poc", compute: { provider: "pve-lab" }, ipam: { provider: "ipam" } };

beforeEach(() => {
  fakeAPI({
    "GET /me": { body: ME },
    "GET /changes?open=true&limit=500": { body: [] },
    "GET /contracts": { body: [
      { name: "poc-web", liveHosts: 1, lastRun: "run-1", lastStatus: "succeeded", sync: "out-of-sync", openChange: "ch1", labels: { owner: "team-platform", env: "test", app: "poc" }, appliedLabel: "2eeece1aaaa", appliedAt: "2026-10-25T07:00:00Z" },
      { name: "billing-db", liveHosts: 0, restricted: true },
      { name: "portal-api", liveHosts: 3, lastStatus: "succeeded", sync: "synced", labels: { owner: "team-portal" } },
    ] },
    "GET /drift": { body: [{ contract: "portal-api", status: "open", firstSeen: "2026-10-25T07:00:00Z", lastSeen: "2026-10-25T07:00:00Z" }] },
    "GET /contracts/poc-web": { body: {
      name: "poc-web", labels: { owner: "team-platform" }, openChange: "ch1", hosts: [HOST],
      state: { sync: "out-of-sync", appliedLabel: "2eeece1aaaa", appliedPlanHash: "sha256:41c0", appliedAt: "2026-10-25T07:00:00Z", appliedPlan: PLAN([KEEP]) },
      runs: [{ id: "run-1", contracts: ["poc-web"], commit: "2eeece1", planHash: "x", status: "succeeded", queuedAt: "2026-10-25T07:00:00Z" }],
    } },
    "GET /drift?contract=poc-web": { body: [] },
    "GET /changes?contract=poc-web&limit=20": { body: [{ id: "ch1", contract: "poc-web", source: "commit", status: "pending", createdAt: "2026-10-25T08:00:00Z", updatedAt: "2026-10-25T08:00:00Z", commits: [{ repo: "contracts", commit: "3f9c2e1aaa" }] }] },
    "GET /changes/ch1": { body: { id: "ch1", contract: "poc-web", source: "commit", status: "pending", createdAt: "x", updatedAt: "x", plan: PLAN([KEEP, { ...KEEP, action: "create", uid: undefined, hostname: "labtpoc02" }]) } },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("contracts", () => {
  it("shows each contract as a card with its health and sync", async () => {
    renderAt("/contracts");
    const poc = (await screen.findByRole("link", { name: "poc-web" })).closest(".card") as HTMLElement;
    expect(within(poc).getByText("OutOfSync")).toBeInTheDocument();
    expect(within(poc).getByText("team-platform")).toBeInTheDocument();
    const portal = screen.getByRole("link", { name: "portal-api" }).closest(".card") as HTMLElement;
    expect(within(portal).getByText("Drifted")).toBeInTheDocument();
    expect(within(portal).getByText("Warning")).toBeInTheDocument();
    expect(screen.getByText("You may see this contract's name only.")).toBeInTheDocument();
  });

  it("shows the contract's state, catalog, servers and history", async () => {
    renderAt("/contracts/poc-web");
    expect(await screen.findByRole("heading", { name: "poc-web" })).toBeInTheDocument();
    expect(screen.getByText("Git has a change not applied yet")).toBeInTheDocument();
    expect(await screen.findByText("rocky9")).toBeInTheDocument();
    expect(screen.getByText("linux-base")).toBeInTheDocument();
    const row = screen.getByText("xfaoec2tq4ekespr").closest("tr")!;
    expect(within(row).getByText("10.50.0.100")).toBeInTheDocument();
    expect(within(row).getByText("pve-lab 9400")).toBeInTheDocument();
    expect(await screen.findByText("3f9c2e1")).toBeInTheDocument();
  });
});
