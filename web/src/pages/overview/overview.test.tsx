import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

const now = new Date().toISOString();
const CH = { id: "ch1", contract: "poc-web", source: "commit", status: "pending", createdAt: now, updatedAt: now, requestedBy: ["mehmet.y"], commits: [{ repo: "contracts", commit: "3f9c2e1aaa" }], awaitingMe: true };

beforeEach(() => {
  fakeAPI({
    "GET /me": { body: ME },
    "GET /changes?open=true&limit=500": { body: [CH] },
    "GET /changes?awaiting=me&limit=200": { body: [CH] },
    "GET /changes?open=true&limit=200": { body: [CH, { ...CH, id: "ch2", awaitingMe: false }] },
    "GET /contracts": { body: [{ name: "poc-web", liveHosts: 1, lastStatus: "succeeded", sync: "out-of-sync" }, { name: "billing-db", liveHosts: 2, lastStatus: "failed", sync: "synced" }] },
    "GET /drift": { body: [] },
    "GET /runs?limit=200": { body: [{ id: "run-f", contracts: ["billing-db"], commit: "x", planHash: "x", status: "failed", queuedAt: now, finishedAt: now }] },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("overview", () => {
  it("puts what waits for me first, then contracts and runs", async () => {
    renderAt("/");
    expect(await screen.findByRole("region", { name: "Awaiting your approval" })).toHaveTextContent("poc-web");
    expect(await screen.findAllByRole("link", { name: "run-f" })).toHaveLength(2);
    expect(screen.getByText("last failed:", { exact: false })).toBeInTheDocument();
    expect(await screen.findByText("2 contracts")).toBeInTheDocument();
  });
});
