import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

afterEach(() => vi.unstubAllGlobals());

const ADMIN = { body: { ...ME, roles: ["viewer", "operator", "platform-admin"] } };
const PROVIDERS = { body: [
  { name: "pve-lab", type: "proxmox", endpoint: "https://pve:8006", credentials: "bao://kv/platform/pve", usedBy: [{ kind: "Site", name: "lab", field: "spec.compute" }],
    check: { status: "failed", error: "dial tcp: connection refused", latencyMs: 3, checkedAt: "2026-10-25T08:00:00Z", lastOkAt: "2026-10-25T07:00:00Z" } },
  { name: "zabbix", type: "zabbix", endpoint: "http://zbx/api_jsonrpc.php", usedBy: [], check: { status: "ok", detail: "Zabbix 7.0.32 · 12 host", latencyMs: 41, checkedAt: "2026-10-25T08:00:00Z" } },
  { name: "team-slack", type: "slack", usedBy: [], check: { status: "skipped", detail: "checking would send a message", latencyMs: 0, checkedAt: "2026-10-25T08:00:00Z" } },
] };

describe("settings", () => {
  it("opens on the providers tab, without the notification channels", async () => {
    fakeAPI({ "GET /me": ADMIN, "GET /changes?open=true&limit=500": { body: [] }, "GET /providers": PROVIDERS, "POST /providers/check": { status: 202, body: { status: "checking" } } });
    renderAt("/settings");
    const pve = (await screen.findByRole("link", { name: "pve-lab" })).closest("tr")!;
    expect(screen.getByRole("link", { name: /Providers/ })).toHaveClass("active");
    expect(pve).toHaveTextContent("bao://kv/platform/pve");
    expect(pve).toHaveTextContent("Unreachable");
    expect(pve).toHaveTextContent("Site/lab");
    expect(screen.getByText("dial tcp: connection refused")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "zabbix" }).closest("tr")).toHaveTextContent("Zabbix 7.0.32 · 12 host · 41 ms");
    expect(screen.queryByRole("link", { name: "team-slack" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Check now/ }));
    expect(await screen.findByText(/Checking; the results/)).toBeInTheDocument();
  });

  it("lists the notification channels on their own tab", async () => {
    fakeAPI({ "GET /me": ADMIN, "GET /changes?open=true&limit=500": { body: [] }, "GET /providers": PROVIDERS });
    renderAt("/settings/channels");
    expect((await screen.findByRole("link", { name: "team-slack" })).closest("tr")).toHaveTextContent("Not checked");
    expect(screen.queryByRole("link", { name: "pve-lab" })).not.toBeInTheDocument();
  });

  it("sends the old screens to their tabs", async () => {
    fakeAPI({ "GET /me": ADMIN, "GET /changes?open=true&limit=500": { body: [] }, "GET /providers": PROVIDERS });
    renderAt("/providers");
    expect(await screen.findByRole("link", { name: "pve-lab" })).toBeInTheDocument();
  });

  it("shows what each source did, with its problems, and the sources themselves", async () => {
    fakeAPI({
      "GET /me": ADMIN,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /sources": { body: [
        { source: "AnsibleRoleSource/corp-roles", status: "committed", commit: "abc1234def", detail: { added: ["a.yaml", "b.yaml"], problems: ["broken: blob.type raw is not allowed"] }, ranAt: "2026-10-25T08:00:00Z" },
        { source: "TeamSource/corp-ad", status: "failed", error: "ldap: connection refused", ranAt: "2026-10-25T08:00:00Z" },
      ] },
      "GET /catalog?kind=NetworkSource,TeamSource,AnsibleRoleSource&spec=1": { body: { commit: "c", objects: [
        { kind: "AnsibleRoleSource", name: "corp-roles", source: "sources.yaml:1", usedBy: 0, contracts: 0, spec: { provider: "config-repo", output: "platform/generated/ansible-roles" } },
      ] } },
      "POST /sources/sync": { status: 202, body: { status: "syncing" } },
    });
    renderAt("/sync");
    const row = (await screen.findByRole("link", { name: "corp-roles" })).closest("tr")!;
    expect(row).toHaveTextContent("AnsibleRoleSource");
    expect(row).toHaveTextContent("Committed");
    expect(row).toHaveTextContent("abc1234");
    expect(row).toHaveTextContent("+2");
    expect(row).toHaveTextContent("platform/generated/ansible-roles");
    expect(within(row).getByRole("link", { name: "config-repo" })).toHaveAttribute("href", "/settings/Provider/config-repo");
    expect(within(row).getByText("1 left out")).toBeInTheDocument();
    // A report whose Source is gone still shows.
    const team = screen.getByRole("link", { name: "corp-ad" }).closest("tr")!;
    expect(team).toHaveTextContent("Failed");
    expect(team).toHaveTextContent("ldap: connection refused");
    fireEvent.click(screen.getByRole("button", { name: /Sync now/ }));
    expect(await screen.findByText(/Sync started/)).toBeInTheDocument();
  });

  it("shows directory syncs apart, linked to their provider", async () => {
    fakeAPI({
      "GET /me": ADMIN,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /sources": { body: [
        { source: "Directory/lab-ad", status: "synced", detail: { joined: ["team-web/member: ali"], left: [], disabled: ["veli"] }, ranAt: "2026-10-25T08:00:00Z" },
      ] },
      "GET /catalog?kind=NetworkSource,TeamSource,AnsibleRoleSource&spec=1": { body: { commit: "c", objects: [] } },
    });
    renderAt("/settings/sources");
    const row = (await screen.findByRole("link", { name: "lab-ad" })).closest("tr")!;
    expect(screen.getByRole("link", { name: "lab-ad" })).toHaveAttribute("href", "/settings/Provider/lab-ad");
    expect(row).toHaveTextContent("Synced");
    expect(screen.queryByRole("link", { name: /Directory\/lab-ad/ })).not.toBeInTheDocument();
    expect(screen.getByText("No sources. Add one to make networks, teams or roles from another system.")).toBeInTheDocument();
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
    renderAt("/settings/discovery");
    expect(await screen.findByText("old-db")).toBeInTheDocument();
    expect(screen.queryByText("labtpoc01")).not.toBeInTheDocument();
    expect(screen.getByText("Missing servers")).toBeInTheDocument();
  });

  it("shows the Policy as fields, and its YAML on request", async () => {
    const yaml = "kind: Policy\nspec:\n  naming:\n    pattern: lab-{role}\n";
    fakeAPI({
      "GET /me": ADMIN,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /catalog?kind=Policy&spec=1": { body: { commit: "c", objects: [{ kind: "Policy", name: "default", source: "settings:Policy/default.yaml:1", usedBy: 0, contracts: 0, spec: {} }] } },
      "GET /catalog/Policy/default": { body: { kind: "Policy", name: "default", source: "settings:Policy/default.yaml:1", yaml, spec: { naming: { pattern: "lab-{role}" } }, refs: [], usedByRefs: [] } },
      "GET /schema/Policy": { body: { type: "object", fields: [{ key: "naming", type: "object", fields: [{ key: "pattern", type: "string" }] }] } },
    });
    renderAt("/settings/policy");
    expect(await screen.findByText("lab-{role}")).toBeInTheDocument();
    expect(screen.getByText("Pattern")).toBeInTheDocument();
    expect(screen.queryByText(/pattern: lab/)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "default" })).toHaveAttribute("href", "/settings/Policy/default");
    fireEvent.click(screen.getByRole("button", { name: /View YAML/ }));
    expect(await screen.findByText(/pattern: lab-\{role\}/)).toBeInTheDocument();
  });

  it("lists the settings versions and shows one", async () => {
    fakeAPI({
      "GET /me": ADMIN,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /settings": { body: { store: true, current: { version: 2, author: "source/ipam-lab", reason: "r", contentHash: "sha256:ab", createdAt: "2026-10-25T08:00:00Z" }, ignored: [] } },
      "GET /settings/versions": { body: [
        { version: 2, author: "source/ipam-lab", reason: "NetworkSource/ipam-lab: 1 added, 0 changed, 0 removed", contentHash: "sha256:ab", createdAt: "2026-10-25T08:00:00Z", objects: 21 },
        { version: 1, author: "gaia", reason: "Imported from Git: platform@66fdf74", contentHash: "sha256:cd", createdAt: "2026-10-25T07:00:00Z", objects: 20 },
      ] },
      "GET /settings/versions/1": { body: { version: 1, author: "gaia", reason: "Imported from Git", contentHash: "sha256:cd", createdAt: "2026-10-25T07:00:00Z",
        files: [{ path: "settings:Size/small.yaml", kind: "Size", yaml: "kind: Size\nmetadata: { name: small }\n" }] } },
    });
    renderAt("/settings/versions");
    expect(await screen.findByRole("link", { name: /settings v2/ })).toHaveAttribute("href", "/settings/versions/2");
    const v2 = (await screen.findByRole("link", { name: "v2" })).closest("tr")!;
    expect(v2).toHaveTextContent("current");
    expect(v2).toHaveTextContent("ipam-lab");
    expect(screen.getByRole("link", { name: "v1" }).closest("tr")).toHaveTextContent("Gaia");
    fireEvent.click(screen.getByRole("link", { name: "v1" }));
    expect(await screen.findByRole("heading", { name: "Settings v1" })).toBeInTheDocument();
    const obj = (await screen.findByText("small")).closest("tr")!;
    expect(obj).toHaveTextContent("Size");
    fireEvent.click(within(obj).getByRole("button", { name: /YAML/ }));
    expect(await screen.findByText(/metadata: \{ name: small \}/)).toBeInTheDocument();
  });

  it("warns about the settings Git still has", async () => {
    fakeAPI({
      "GET /me": ADMIN,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /sources": { body: [{ source: "NetworkSource/ipam-lab", status: "stored", detail: { added: ["settings:Network/x.yaml"], version: 2 }, ranAt: "2026-10-25T08:00:00Z" }] },
      "GET /settings": { body: { store: true, current: { version: 2, author: "gaia", reason: "r", contentHash: "h", createdAt: "2026-10-25T08:00:00Z" }, ignored: [
        { kind: "Size", name: "small", source: "platform/catalog/sizes.yaml:1" }, { kind: "Size", name: "medium", source: "platform/catalog/sizes.yaml:8" },
      ] } },
    });
    renderAt("/settings/sources");
    expect(await screen.findByText("2 old copies of settings are still in Git.")).toBeInTheDocument();
    expect(screen.getByText("platform/catalog/sizes.yaml").closest("tr")).toHaveTextContent("Size smallSize medium");
    const row = (await screen.findByRole("link", { name: "ipam-lab" })).closest("tr")!;
    expect(row).toHaveTextContent("Saved");
    expect(screen.getByRole("link", { name: "v2" })).toHaveAttribute("href", "/settings/versions/2");
  });
});
