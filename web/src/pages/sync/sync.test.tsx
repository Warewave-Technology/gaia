import { fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

afterEach(() => vi.unstubAllGlobals());

describe("sync and discovery", () => {
  it("shows what each source did, with its problems", async () => {
    fakeAPI({
      "GET /me": { body: { ...ME, roles: ["viewer", "operator", "platform-admin"] } },
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /sources": { body: [
        { source: "AnsibleRoleSource/corp-roles", status: "committed", commit: "abc1234def", detail: { added: ["a.yaml", "b.yaml"], problems: ["broken: blob.type raw is not allowed"] }, ranAt: "2026-10-25T08:00:00Z" },
        { source: "TeamSource/corp-ad", status: "failed", error: "ldap: connection refused", ranAt: "2026-10-25T08:00:00Z" },
      ] },
      "POST /sources/sync": { status: 202, body: { status: "syncing" } },
    });
    renderAt("/sync");
    const row = (await screen.findByRole("link", { name: /corp-roles/ })).closest("tr")!;
    expect(row).toHaveTextContent("Commit yazıldı");
    expect(row).toHaveTextContent("+2");
    expect(screen.getByText("1 sorun")).toBeInTheDocument();
    expect(screen.getByText("ldap: connection refused")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Şimdi eşitle/ }));
    expect(await screen.findByText(/Eşitleme başladı/)).toBeInTheDocument();
  });

  it("lists unmanaged VMs first and the missing ones", async () => {
    fakeAPI({
      "GET /me": { body: ME },
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /discovery": { body: {
        takenAt: "2026-10-25T08:00:00Z", counts: {},
        vms: [
          { provider: "pve-lab", id: "9400", name: "labtpoc01", powerState: "running", category: "managed", contract: "poc-web" },
          { provider: "pve-lab", id: "120", name: "old-db", powerState: "running", category: "unmanaged", ips: ["192.168.1.40"] },
        ],
        missing: [{ provider: "pve-lab", id: "9401", uid: "u2", hostname: "labtpoc02", contract: "poc-web" }], hosts: [],
      } },
    });
    renderAt("/discovery");
    expect(await screen.findByText("old-db")).toBeInTheDocument();
    expect(screen.queryByText("labtpoc01")).not.toBeInTheDocument();
    expect(screen.getByText("Kayıp sunucular")).toBeInTheDocument();
  });
});
