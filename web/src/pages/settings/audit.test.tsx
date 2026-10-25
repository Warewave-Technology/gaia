import { fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

afterEach(() => vi.unstubAllGlobals());

const ADMIN = { body: { ...ME, roles: ["viewer", "platform-admin"] } };
const entry = (id: number, action: string, extra: object = {}) => ({ id, at: "2026-10-25T08:00:00Z", actor: "ayse", address: "10.0.0.5", action, result: "ok", ...extra });

describe("audit log", () => {
  it("lists entries with their objects linked, filters by area and pages back", async () => {
    const page1 = Array.from({ length: 100 }, (_, i) => entry(500 - i, "auth.login"));
    page1[0] = entry(500, "change.approve", { object: "chg-abc", detail: { contract: "poc-web" } });
    page1[1] = entry(499, "auth.login", { actor: "can", result: "denied", detail: { why: "credentials" } });
    page1[2] = entry(498, "settings.apply", { object: "settings@4" });
    const calls = fakeAPI({
      "GET /me": ADMIN,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /audit?limit=100&": { body: page1 },
      "GET /audit?limit=100&before=401": { body: [entry(400, "run.cancel", { object: "run-x" })] },
      "GET /audit?limit=100&action=settings.": { body: [entry(498, "settings.apply", { object: "settings@4" })] },
    });
    renderAt("/settings/audit");
    const approve = (await screen.findByText("Change approved")).closest("tr")!;
    expect(approve).toHaveTextContent("ayse");
    expect(approve).toHaveTextContent("10.0.0.5");
    expect(screen.getByRole("link", { name: "chg-abc" })).toHaveAttribute("href", "/changes/chg-abc");
    expect(screen.getByRole("link", { name: "settings@4" })).toHaveAttribute("href", "/settings/versions/4");
    expect(screen.getAllByText("denied", { selector: ".badge" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: /Older/ }));
    expect(await screen.findByRole("link", { name: "run-x" })).toHaveAttribute("href", "/runs/run-x");
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    await vi.waitFor(() => expect(screen.queryByText("Change approved")).not.toBeInTheDocument());
    expect(await screen.findByText("Settings change applied")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /JSON lines/ })).toHaveAttribute("href", "/api/v1/audit?format=jsonl&limit=10000&action=settings.");
    expect(calls.some((c) => c.path === "/audit?limit=100&action=settings.")).toBe(true);
  });

  it("is for platform admins", async () => {
    fakeAPI({ "GET /me": { body: ME }, "GET /changes?open=true&limit=500": { body: [] } });
    renderAt("/settings/audit");
    expect(await screen.findByText(/for platform admins/)).toBeInTheDocument();
  });
});
