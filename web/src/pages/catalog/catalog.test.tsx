import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

beforeEach(() => {
  fakeAPI({
    "GET /me": { body: { ...ME, roles: ["viewer", "operator", "platform-admin"] } },
    "GET /changes?open=true&limit=500": { body: [] },
    "GET /catalog?kind=AnsibleRole,App,Agent,Baseline&spec=1": { body: { commit: "9a1b0c4aaaa", objects: [
      { kind: "AnsibleRole", name: "nginx", labels: { category: "web" }, source: "", generatedBy: "AnsibleRoleSource/lab-roles", usedBy: 2, contracts: 2, params: 4,
        spec: { description: "nginx'i kurar ve tek bir siteyi sunar", os: ["el"] } },
      { kind: "AnsibleRole", name: "podman", source: "", generatedBy: "AnsibleRoleSource/lab-roles", usedBy: 0, contracts: 0, params: 1, spec: { os: ["el"] } },
      { kind: "Agent", name: "zabbix-agent", source: "catalog/agents.yaml:1", usedBy: 0, contracts: 0, spec: { mandatory: true, ansibleRole: "zabbix" } },
      { kind: "Baseline", name: "linux-base", source: "catalog/baseline.yaml:1", usedBy: 1, contracts: 1, spec: { os: "linux", playbook: "baseline/linux.yml" } },
    ] } },
    "GET /catalog/Network/lab-poc": { body: {
      kind: "Network", name: "lab-poc", source: "catalog/networks.yaml:1", usedBy: 1, contracts: 1, yaml: "kind: Network\nmetadata: { name: lab-poc }\n",
      refs: [{ kind: "Provider", name: "ipam", field: "spec.ipam.provider" }], usedByRefs: [{ kind: "Server", name: "poc-web", field: "spec.network" }],
    } },
    "GET /catalog/AnsibleRole/nginx": { body: {
      kind: "AnsibleRole", name: "nginx", source: "generated/ansible-roles/nginx.yaml:1", generatedBy: "AnsibleRoleSource/lab-roles", usedBy: 0, contracts: 0, yaml: "kind: AnsibleRole\n",
      refs: [], usedByRefs: [],
      role: { provider: "config-repo", role: "nginx", os: ["el"] },
      params: [
        { name: "listen_port", type: "int", default: 80, min: 1, max: 65535 },
        { name: "server_name", type: "str", required: true, description: "Sitenin adı" },
        { name: "tls", type: "dict" },
        { name: "tls.key", type: "str", secret: true },
      ],
      rules: [{ expression: "params.listen_port != 22", message: "22 SSH'ın" }],
    } },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("catalog", () => {
  it("shows what can be installed as cards, filtered by kind and text", async () => {
    renderAt("/catalog");
    const nginx = await screen.findByRole("link", { name: "AnsibleRole nginx" });
    expect(nginx).toHaveAttribute("href", "/catalog/AnsibleRole/nginx");
    expect(nginx).toHaveTextContent("nginx'i kurar");
    expect(nginx).toHaveTextContent("web");
    expect(within(nginx).getByTitle("Kullanan kontrat")).toHaveTextContent("2");
    expect(screen.getByRole("link", { name: "Agent zabbix-agent" })).toHaveTextContent("zorunlu");
    fireEvent.click(screen.getByRole("button", { name: /Baseline'lar/ }));
    expect(screen.queryByRole("link", { name: "AnsibleRole nginx" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Baseline linux-base" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Hepsi/ }));
    fireEvent.change(screen.getByPlaceholderText(/Ad, açıklama/), { target: { value: "siteyi" } });
    expect(screen.getByRole("link", { name: "AnsibleRole nginx" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "AnsibleRole podman" })).not.toBeInTheDocument();
  });

  it("shows an object's YAML, its references and the contracts that use it", async () => {
    renderAt("/infra/Network/lab-poc");
    expect(await screen.findByText(/kind: Network/)).toBeInTheDocument();
    expect(screen.getByRole("banner")).toHaveTextContent("Altyapı / Network / lab-poc");
    expect(within(screen.getByRole("navigation", { name: "Ana menü" })).getByRole("link", { name: /Altyapı/ })).toHaveClass("active");
    expect(screen.getByRole("link", { name: /Provider\/ipam/ })).toHaveAttribute("href", "/settings/Provider/ipam");
    expect(screen.getByRole("link", { name: /Server\/poc-web/ })).toHaveAttribute("href", "/contracts/poc-web");
  });

  it("shows a role's params, rules and how a contract uses it", async () => {
    renderAt("/catalog/AnsibleRole/nginx");
    const row = (await screen.findByText("server_name")).closest("tr")!;
    expect(row).toHaveTextContent("zorunlu");
    expect(row).toHaveTextContent("Sitenin adı");
    expect(screen.getByText("listen_port").closest("tr")).toHaveTextContent("1 – 65535");
    expect(screen.getByText("key").closest("tr")).toHaveTextContent("gizli");
    expect(screen.getByText("params.listen_port != 22")).toBeInTheDocument();
    expect(screen.getByText(/ansibleRoles:\s+- name: nginx\s+params:\s+server_name: …/)).toBeInTheDocument();
  });
});
