import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import type { Field } from "../api/schema";
import { fieldDiff, pathLabel, prune, setIn, SpecForm, SpecView } from "./spec";

const SITE: Field = { key: "", type: "object", fields: [
  { key: "dnsDomain", type: "string", required: true },
  { key: "ntpServers", type: "list", items: { type: "string" } },
  { key: "compute", type: "object", fields: [{ key: "provider", type: "string", ref: "Provider" }, { key: "vmids", type: "string" }] },
  { key: "zones", type: "list", items: { type: "object", fields: [{ key: "name", type: "string" }, { key: "cidr", type: "string" }] } },
  { key: "enabled", type: "bool" },
] };

function wrap(ui: React.ReactNode) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>);
}

describe("spec", () => {
  it("sets, prunes and diffs values by path", () => {
    expect(setIn({}, ["compute", "provider"], "pve")).toEqual({ compute: { provider: "pve" } });
    expect(setIn({ a: 1, b: 2 }, ["b"], undefined)).toEqual({ a: 1 });
    expect(prune({ a: "", b: [], c: {}, d: { e: undefined }, f: 0, g: false })).toEqual({ f: 0, g: false });
    expect(fieldDiff({ compute: { provider: "a" }, ntpServers: ["x"] }, { compute: { provider: "b" }, ntpServers: ["x", "y"] })).toEqual([
      { path: "compute.provider", before: "a", after: "b" },
      { path: "ntpServers", before: "x", after: "x, y" },
    ]);
    expect(pathLabel("zones[0].cidr")).toBe("Zones 1 › CIDR");
  });

  it("shows a spec as labelled rows, a list of objects as a table, references as links", () => {
    wrap(<SpecView kind="Site" schema={SITE} spec={{ dnsDomain: "lab.local", compute: { provider: "pve" }, zones: [{ name: "a", cidr: "10.0.0.0/24" }], enabled: true }} />);
    expect(screen.getByText("DNS domain").nextSibling).toHaveTextContent("lab.local");
    expect(screen.getByRole("link", { name: "pve" })).toHaveAttribute("href", "/settings/Provider/pve");
    expect(screen.getByRole("columnheader", { name: "CIDR" })).toBeInTheDocument();
    expect(screen.getByText("10.0.0.0/24")).toBeInTheDocument();
    expect(screen.getByText("Yes")).toBeInTheDocument();
  });

  it("edits a spec through fields", () => {
    let last: Record<string, unknown> = {};
    function Form() {
      const [spec, setSpec] = useState<Record<string, unknown>>({});
      last = spec;
      return <SpecForm kind="Site" schema={SITE} spec={spec} onChange={setSpec} />;
    }
    wrap(<Form />);
    fireEvent.change(screen.getByLabelText(/^DNS domain/), { target: { value: "lab.local" } });
    fireEvent.change(screen.getByLabelText(/^VM ID range/), { target: { value: "9000-9999" } });
    const ntp = screen.getByLabelText(/^NTP servers/);
    fireEvent.change(ntp, { target: { value: "ntp1" } });
    fireEvent.keyDown(ntp, { key: "Enter" });
    expect(last).toMatchObject({ dnsDomain: "lab.local", compute: { vmids: "9000-9999" }, ntpServers: ["ntp1"] });
  });
});
