import { describe, expect, it } from "vitest";
import { buildTree } from "./tree";

describe("tree", () => {
  it("hangs each server's resources under it, and planned servers dashed", () => {
    const t = buildTree(
      {
        name: "poc-web", runs: [],
        hosts: [{ uid: "u1", hostname: "labtpoc01", site: "lab", network: "lab-poc", status: "active", externalIds: { "pve-lab/vm": "9400", "pve-lab/smbios": "x", "ipam/ip": "10.50.0.100", "zabbix/host": "10684" } }],
      },
      { label: "OutOfSync", icon: "arrow-up-circle", tone: "yellow" },
      [{ action: "keep", hostname: "labtpoc01", site: "lab", network: "lab-poc" }, { action: "create", hostname: "labtpoc02", site: "lab", network: "lab-poc" }],
    );
    expect(t.nodes.map((n) => n.name)).toEqual(["poc-web", "labtpoc01", "10.50.0.100", "9400", "10684", "labtpoc02"]);
    expect(t.nodes.find((n) => n.name === "9400")?.kind).toBe("VM · pve-lab");
    expect(t.nodes.find((n) => n.name === "labtpoc02")?.dashed).toBe(true);
    expect(t.edges.filter((e) => e.source === "host:u1")).toHaveLength(3);
    expect(t.edges.find((e) => e.target === "planned:labtpoc02")?.dashed).toBe(true);
  });
});
