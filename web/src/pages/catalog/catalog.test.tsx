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
    "GET /providers": { body: [{ name: "pve-lab", type: "proxmox", endpoint: "https://pve:8006", credentials: "bao://kv/platform/pve", usedBy: [{ kind: "Site", name: "lab", field: "spec.compute" }], error: "connection refused" }] },
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

  it("lists providers with their last discovery's error", async () => {
    renderAt("/providers");
    expect(await screen.findByRole("link", { name: "pve-lab" })).toBeInTheDocument();
    expect(screen.getByText("bao://kv/platform/pve")).toBeInTheDocument();
    expect(screen.getByText("connection refused")).toBeInTheDocument();
  });
});
