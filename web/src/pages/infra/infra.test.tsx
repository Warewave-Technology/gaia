import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

afterEach(() => vi.unstubAllGlobals());

const OBJECTS = { body: { commit: "c", objects: [
  { kind: "Site", name: "lab", source: "site.yaml:1", usedBy: 2, contracts: 1, spec: { compute: { provider: "pve-lab", nodes: ["proxmox"], pool: "gaia", storage: { default: "local-lvm" } }, ntp: ["pool.ntp.org"] } },
  { kind: "Network", name: "lab-poc", source: "network.yaml:1", usedBy: 1, contracts: 1, spec: { site: "lab", attachment: { vnet: "gaiapoc" }, ipam: { provider: "ipam", pool: "10.50.0.0/24" }, gateway: "10.50.0.1", allowedEnvironments: ["test"] } },
  { kind: "Image", name: "rocky9", source: "image.yaml:1", usedBy: 1, contracts: 1, spec: { os: "linux", connection: "ssh", templates: [{ provider: "pve-lab", template: { vmid: 9300 }, customization: "cloud-init" }] } },
  { kind: "Size", name: "medium", source: "sizes.yaml:8", usedBy: 0, contracts: 0, spec: { cpu: 4, memoryGiB: 8, diskGiB: 40 } },
  { kind: "Size", name: "small", source: "sizes.yaml:1", usedBy: 1, contracts: 1, spec: { cpu: 2, memoryGiB: 2, diskGiB: 20 } },
] } };

describe("infrastructure", () => {
  it("opens on sites and shows each kind's own columns", async () => {
    fakeAPI({ "GET /me": { body: ME }, "GET /changes?open=true&limit=500": { body: [] }, "GET /catalog?kind=Site,Network,Image,Size&spec=1": OBJECTS });
    renderAt("/infra");
    const lab = (await screen.findByRole("link", { name: "lab" })).closest("tr")!;
    expect(lab).toHaveTextContent("proxmox · gaia · local-lvm");
    expect(lab).toHaveTextContent("pool.ntp.org");
    expect(screen.getByRole("link", { name: /Networks/ })).toHaveTextContent("1");
  });

  it("lists networks with their attachment and pool", async () => {
    fakeAPI({ "GET /me": { body: ME }, "GET /changes?open=true&limit=500": { body: [] }, "GET /catalog?kind=Site,Network,Image,Size&spec=1": OBJECTS });
    renderAt("/infra/Network");
    const row = (await screen.findByRole("link", { name: "lab-poc" })).closest("tr")!;
    expect(row).toHaveTextContent("vnet gaiapoc");
    expect(row).toHaveTextContent("10.50.0.0/24 · ipam");
    expect(row).toHaveTextContent("test");
  });

  it("orders sizes by CPU and shows image templates", async () => {
    fakeAPI({ "GET /me": { body: ME }, "GET /changes?open=true&limit=500": { body: [] }, "GET /catalog?kind=Site,Network,Image,Size&spec=1": OBJECTS });
    renderAt("/infra/Size");
    const rows = await screen.findAllByRole("row");
    expect(rows[1]).toHaveTextContent("small");
    expect(rows[2]).toHaveTextContent("medium48 GiB40 GiB");
  });
});
