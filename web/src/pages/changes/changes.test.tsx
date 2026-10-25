import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt, type Call } from "../../test-utils";

vi.mock("@simplewebauthn/browser", () => ({
  startAuthentication: vi.fn(async () => ({ id: "cred", rawId: "cred", type: "public-key", response: {} })),
}));

const PLAN = {
  contracts: [{
    name: "poc-web", size: { name: "small", cpu: 2, memoryGiB: 2 }, monitoring: { provider: "zabbix" }, baseline: "linux-base",
    access: [{ via: "team/team-web-ops", teams: ["team-web-ops"], sudoCommands: ["/usr/bin/systemctl restart nginx"] }],
    servers: [
      { action: "keep", uid: "xfaoec2tq4ekespr", hostname: "labtpoc01", site: "lab", network: "lab-poc", compute: { provider: "pve-lab" }, ipam: { provider: "ipam", pool: "10.50.0.0/24" } },
      { action: "create", hostname: "labtpoc02", site: "lab", network: "lab-poc", compute: { provider: "pve-lab" }, ipam: { provider: "ipam", pool: "10.50.0.0/24" } },
    ],
  }],
};

const CHANGE = {
  id: "ch1", contract: "poc-web", source: "commit", status: "pending", label: "contracts@3f9c2e1", planHash: "sha256:668ada7b0123456789abcdef",
  requestedBy: ["mehmet.y"], commits: [{ repo: "contracts", commit: "3f9c2e1aaaaaaaa", signer: "mehmet.y" }],
  gates: [
    { gate: "validation", status: "ok", detail: {} },
    { gate: "signature", status: "ok", detail: { signers: ["mehmet.y"] } },
    { gate: "approval", status: "waiting", detail: { groups: [{ name: "owner", teams: ["team-platform"], role: "member", candidates: ["ayse.k", "can.d"], why: "" }], excluded: ["mehmet.y"] } },
  ],
  approvals: [], plan: PLAN, createdAt: "2026-10-25T19:02:00Z", updatedAt: "2026-10-25T19:02:00Z", awaitingMe: true,
};

let calls: Call[];
beforeEach(() => {
  calls = fakeAPI({
    "GET /me": { body: ME },
    "GET /changes?open=true&limit=500": { body: [CHANGE] },
    "GET /changes?open=true&limit=200": { body: [CHANGE, { id: "ch2", contract: "billing-db", status: "pending", createdAt: CHANGE.createdAt, updatedAt: CHANGE.updatedAt, restricted: true }] },
    "GET /changes/ch1": { body: CHANGE },
    "GET /changes/ch1/notifications": { body: [] },
    "POST /auth/step-up/begin": { body: { flow: "f1", options: { publicKey: { challenge: "abc", allowCredentials: [] } } } },
    "POST /auth/step-up/finish": { status: 204 },
    "POST /changes/ch1/approve": { body: { ...CHANGE, status: "approved" } },
    "GET /changes/ch1/diff": { body: { from: "2eeece1aaaaaaaaaaaaaaa", to: "3f9c2e1aaaaaaaaaaaaaaa", objects: [
      { kind: "Server", name: "poc-web", file: "contracts/poc-web.yaml", status: "modified", diff: "--- contracts/poc-web.yaml\n+++ contracts/poc-web.yaml\n@@ -3,3 +3,3 @@\n spec:\n-  replicas: 1\n+  replicas: 2\n" },
    ] } },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("changes", () => {
  it("lists open changes and marks the ones that wait for me", async () => {
    renderAt("/changes");
    const row = (await screen.findByRole("link", { name: "poc-web" })).closest("tr")!;
    expect(within(row).getByText("sizi bekliyor")).toBeInTheDocument();
    expect(screen.getByText("(yetkiniz yok)")).toBeInTheDocument();
  });

  it("shows the plan per area and per server", async () => {
    renderAt("/changes/ch1");
    expect(await screen.findByRole("heading", { name: "poc-web" })).toBeInTheDocument();
    expect(screen.getByText("labtpoc02").closest("tr")).toHaveClass("added");
    expect(screen.getByText(/team-platform üyelerinden biri/)).toBeInTheDocument();
    expect(screen.getByText(/mehmet.y bu değişikliği onaylayamaz/)).toBeInTheDocument();
    const access = screen.getByText("team/team-web-ops").closest("tr")!;
    expect(access).toHaveTextContent("sınırlı sudo");
    expect(access).toHaveTextContent("/usr/bin/systemctl restart nginx");
  });

  it("shows the YAML diff since the contract was applied", async () => {
    renderAt("/changes/ch1");
    fireEvent.click(await screen.findByRole("tab", { name: /YAML diff/ }));
    const line = (t: string) => (_: string, el: Element | null) => el?.tagName === "SPAN" && el.textContent === t;
    expect(await screen.findByText(line("-  replicas: 1"))).toHaveStyle({ color: "var(--red)" });
    expect(screen.getByText(line("+  replicas: 2"))).toHaveStyle({ color: "var(--green)" });
    expect(screen.queryByText(/^\+\+\+/)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Server\/poc-web/ })).toHaveAttribute("href", "/contracts/poc-web");
    expect(screen.getByText("2eeece1")).toBeInTheDocument();
  });

  it("confirms with a passkey, then approves the plan it saw", async () => {
    renderAt("/changes/ch1");
    fireEvent.click(await screen.findByRole("button", { name: /Onayla/ }));
    fireEvent.click(screen.getByRole("button", { name: /Passkey ile onayla/ }));
    await vi.waitFor(() => expect(calls.some((c) => c.path === "/changes/ch1/approve")).toBe(true));
    const order = calls.filter((c) => c.method === "POST").map((c) => c.path);
    expect(order).toEqual(["/auth/step-up/begin", "/auth/step-up/finish", "/changes/ch1/approve"]);
    const approve = calls.find((c) => c.path === "/changes/ch1/approve")!;
    expect(approve.body).toEqual({ planHash: CHANGE.planHash, comment: "" });
    expect(approve.headers["X-Gaia-Request"]).toBe("1");
    expect(approve.headers["Content-Type"]).toBe("application/json");
  });

  it("asks for a TOTP code when there is no passkey, and a reason to reject", async () => {
    calls = fakeAPI({
      "GET /me": { body: ME },
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /changes/ch1": { body: CHANGE },
      "GET /changes/ch1/notifications": { body: [] },
      "POST /auth/step-up/begin": { status: 409, body: { error: "register a passkey first" } },
      "POST /auth/step-up/totp": { status: 204 },
      "POST /changes/ch1/reject": { body: { ...CHANGE, status: "rejected" } },
    });
    renderAt("/changes/ch1");
    fireEvent.click(await screen.findByRole("button", { name: /Reddet/ }));
    fireEvent.click(screen.getByRole("button", { name: /Passkey ile reddet/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("gerekçe");
    fireEvent.change(screen.getByLabelText("Red gerekçesi"), { target: { value: "not now" } });
    fireEvent.click(screen.getByRole("button", { name: /Passkey ile reddet/ }));
    const code = await screen.findByLabelText(/kodu girin/);
    fireEvent.change(code, { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: /Kodla reddet/ }));
    await vi.waitFor(() => expect(calls.some((c) => c.path === "/changes/ch1/reject")).toBe(true));
    expect(calls.find((c) => c.path === "/auth/step-up/totp")!.body).toEqual({ code: "123456" });
    expect(calls.find((c) => c.path === "/changes/ch1/reject")!.body).toEqual({ planHash: CHANGE.planHash, comment: "not now" });
  });
});
