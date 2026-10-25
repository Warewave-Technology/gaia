import { fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

beforeEach(() => {
  fakeAPI({
    "GET /me": { body: { ...ME, roles: ["viewer", "operator", "platform-admin"] } },
    "GET /changes?open=true&limit=500": { body: [] },
    "GET /catalog": { body: { commit: "9a1b0c4aaaa", objects: [
      { kind: "Network", name: "lab-poc", labels: { site: "lab" }, source: "catalog/networks.yaml:1", usedBy: 2 },
      { kind: "Network", name: "dc1-prod", labels: { site: "dc1" }, source: "", generatedBy: "NetworkSource/ipam", patches: 1, usedBy: 0 },
      { kind: "Image", name: "rocky9", source: "catalog/images.yaml:1", usedBy: 1 },
    ] } },
    "GET /catalog/Network/lab-poc": { body: {
      kind: "Network", name: "lab-poc", source: "catalog/networks.yaml:1", usedBy: 1, yaml: "kind: Network\nmetadata: { name: lab-poc }\n",
      refs: [{ kind: "Provider", name: "ipam", field: "spec.ipam.provider" }], usedByRefs: [{ kind: "Server", name: "poc-web", field: "spec.network" }],
    } },
    "GET /providers": { body: [
      { name: "pve-lab", type: "proxmox", endpoint: "https://pve:8006", credentials: "bao://kv/platform/pve", usedBy: [{ kind: "Site", name: "lab", field: "spec.compute" }],
        check: { status: "failed", error: "dial tcp: connection refused", latencyMs: 3, checkedAt: "2026-10-25T08:00:00Z", lastOkAt: "2026-10-25T07:00:00Z" } },
      { name: "zabbix", type: "zabbix", endpoint: "http://zbx/api_jsonrpc.php", usedBy: [], check: { status: "ok", detail: "Zabbix 7.0.32 · 12 host", latencyMs: 41, checkedAt: "2026-10-25T08:00:00Z" } },
      { name: "team-slack", type: "slack", usedBy: [], check: { status: "skipped", detail: "denetim bir mesaj göndermek olurdu", latencyMs: 0, checkedAt: "2026-10-25T08:00:00Z" } },
    ] },
    "POST /providers/check": { status: 202, body: { status: "checking" } },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("catalog", () => {
  it("filters by kind and by label", async () => {
    renderAt("/catalog");
    expect(await screen.findByRole("link", { name: /rocky9/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Network/ }));
    expect(screen.queryByRole("link", { name: /rocky9/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText(/Ad ya da etiket/), { target: { value: "site=dc1" } });
    expect(screen.getByRole("link", { name: /dc1-prod/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /lab-poc/ })).not.toBeInTheDocument();
    expect(screen.getByText(/NetworkSource\/ipam \+ 1 patch/)).toBeInTheDocument();
  });

  it("shows an object's YAML, its references and the contracts that use it", async () => {
    renderAt("/catalog/Network/lab-poc");
    expect(await screen.findByText(/kind: Network/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Provider\/ipam/ })).toHaveAttribute("href", "/catalog/Provider/ipam");
    expect(screen.getByRole("link", { name: /Server\/poc-web/ })).toHaveAttribute("href", "/contracts/poc-web");
  });

  it("lists providers with their connection check", async () => {
    renderAt("/providers");
    const pve = (await screen.findByRole("link", { name: "pve-lab" })).closest("tr")!;
    expect(pve).toHaveTextContent("bao://kv/platform/pve");
    expect(pve).toHaveTextContent("Ulaşılamıyor");
    expect(screen.getByText("dial tcp: connection refused")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "zabbix" }).closest("tr")).toHaveTextContent("Zabbix 7.0.32 · 12 host · 41 ms");
    expect(screen.getByRole("link", { name: "team-slack" }).closest("tr")).toHaveTextContent("Denetlenmiyor");
    fireEvent.click(screen.getByRole("button", { name: /Şimdi denetle/ }));
    expect(await screen.findByText(/Denetim başladı/)).toBeInTheDocument();
  });
});
