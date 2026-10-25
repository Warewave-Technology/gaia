import { describe, expect, it } from "vitest";
import { areas, total, type Plan } from "./plan";

const plan: Plan = {
  contracts: [{
    name: "poc-web",
    monitoring: { provider: "zabbix" },
    servers: [
      { action: "keep", hostname: "labtpoc01", site: "lab", network: "lab-poc", compute: { provider: "pve-lab" }, ipam: { provider: "ipam" } },
      { action: "create", hostname: "labtpoc02", site: "lab", network: "lab-poc", compute: { provider: "pve-lab" }, ipam: { provider: "ipam" } },
    ],
  }],
};

describe("plan", () => {
  it("sums each area", () => {
    const a = areas(plan);
    expect(a.map((x) => x.key)).toEqual(["ipam", "provisioning", "monitoring", "configuration"]);
    expect(a[1]).toMatchObject({ providers: ["pve-lab"], counts: { add: 1, same: 1, change: 0, remove: 0 } });
    expect(a[2]?.providers).toEqual(["zabbix"]);
    expect(a[3]?.counts.same).toBe(2);
    expect(total(plan)).toEqual({ add: 1, change: 0, same: 1, remove: 0 });
  });
});
