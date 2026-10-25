import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

vi.mock("@simplewebauthn/browser", () => ({
  startAuthentication: vi.fn(async () => ({ id: "cred", rawId: "cred", type: "public-key", response: {} })),
}));

afterEach(() => vi.unstubAllGlobals());

const OPERATOR = { body: { ...ME, roles: ["viewer", "operator"] } };
const SETTINGS = { body: { store: true, current: { version: 3, author: "gaia", reason: "r", contentHash: "h", createdAt: "2026-10-25T08:00:00Z" }, ignored: [] } };
const INFRA = { body: { commit: "c", objects: [{ kind: "Size", name: "small", source: "settings:Size/small.yaml:1", usedBy: 1, contracts: 1, spec: { cpu: 2, memoryGiB: 2, diskGiB: 20 } }] } };
const PROPOSAL = {
  id: "set-1", title: "Size/tiny: ekle", status: "pending", baseVersion: 3, contentHash: "sha256:c1", requestedBy: "mehmet", critical: false,
  impact: [{ contract: "poc-web" }], paths: ["settings:Size/tiny.yaml"], createdAt: "2026-10-25T08:00:00Z", mayDecide: true,
  objects: [{ kind: "Size", name: "small", path: "settings:Size/small.yaml", op: "change", before: "spec:\n  cpu: 2\n", after: "spec:\n  cpu: 3\n" }],
};

describe("settings changes", () => {
  it("adds an object through the form, checks it and proposes it", async () => {
    const calls = fakeAPI({
      "GET /me": OPERATOR,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /settings": SETTINGS,
      "GET /catalog?kind=Site,Network,Image,Size&spec=1": INFRA,
      "POST /settings/check": { body: { base: 3, contentHash: "sha256:c1", objects: [], errors: [], impact: [{ contract: "poc-web" }], critical: false } },
      "POST /settings/proposals": { status: 201, body: PROPOSAL },
      "GET /settings/proposals/set-1": { body: PROPOSAL },
    });
    renderAt("/infra/Size");
    fireEvent.click(await screen.findByRole("button", { name: /Size ekle/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Ad"), { target: { value: "tiny" } });
    fireEvent.change(within(dialog).getByLabelText("vCPU"), { target: { value: "8" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Kontrol et/ }));
    expect(await within(dialog).findByText(/Planı değişecek kontratlar \(1\)/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("tab", { name: /YAML/ }));
    const yaml = (within(dialog).getByLabelText("YAML") as HTMLTextAreaElement).value;
    expect(yaml, yaml).toMatch(/name: tiny[\s\S]*cpu: 8/);
    fireEvent.click(within(dialog).getByRole("button", { name: /Öner/ }));
    expect(await screen.findByRole("heading", { name: "Size/tiny: ekle" })).toBeInTheDocument();
    const body = calls.find((c) => c.path === "/settings/proposals")!.body as { title: string; changes: { yaml: string }[] };
    expect(body.title).toBe("Size/tiny: ekle");
    expect(body.changes[0]!.yaml).toMatch(/kind: Size/);
  });

  it("shows why a change cannot be proposed", async () => {
    fakeAPI({
      "GET /me": OPERATOR,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /settings": SETTINGS,
      "GET /catalog?kind=Site,Network,Image,Size&spec=1": INFRA,
      "POST /settings/proposals": { status: 422, body: { error: "the settings would not load", check: { base: 3, contentHash: "x", objects: [], errors: ["Size/tiny: cpu, memoryGiB and diskGiB must be at least 1"], impact: [], critical: false } } },
    });
    renderAt("/infra/Size");
    fireEvent.click(await screen.findByRole("button", { name: /Size ekle/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Ad"), { target: { value: "tiny" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Öner/ }));
    expect(await within(dialog).findByText(/must be at least 1/)).toBeInTheDocument();
  });

  it("does not offer editing to viewers", async () => {
    fakeAPI({ "GET /me": { body: ME }, "GET /changes?open=true&limit=500": { body: [] }, "GET /settings": SETTINGS, "GET /catalog?kind=Site,Network,Image,Size&spec=1": INFRA });
    renderAt("/infra/Size");
    expect(await screen.findByRole("link", { name: "small" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Size ekle/ })).not.toBeInTheDocument();
  });

  it("shows the change line by line and approves it with a passkey", async () => {
    const calls = fakeAPI({
      "GET /me": { body: { ...ME, roles: ["viewer", "platform-admin"] } },
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /settings/proposals?status=pending": { body: [PROPOSAL] },
      "GET /settings/proposals/set-1": { body: PROPOSAL },
      "POST /auth/step-up/begin": { body: { flow: "f1", options: { publicKey: { challenge: "abc", allowCredentials: [] } } } },
      "POST /auth/step-up/finish": { status: 204 },
      "POST /settings/proposals/set-1/approve": (() => {
        let n = 0;
        return () => (n++ === 0 ? { status: 403, body: { error: "confirm it is you", stepUp: true } } : { body: { ...PROPOSAL, status: "applied", appliedVersion: 4 } });
      })(),
    });
    renderAt("/changes/settings/set-1");
    expect(await screen.findByRole("heading", { name: "Size/tiny: ekle" })).toBeInTheDocument();
    expect(document.querySelector(".diff-line.del")).toHaveTextContent("cpu: 2");
    expect(document.querySelector(".diff-line.add")).toHaveTextContent("cpu: 3");
    expect(screen.getByRole("link", { name: "poc-web" })).toHaveAttribute("href", "/contracts/poc-web");
    expect(screen.getByTitle("Onay bekleyen değişiklik")).toHaveTextContent("1");
    fireEvent.click(screen.getByRole("button", { name: "Onayla" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Onayla" }));
    await vi.waitFor(() => expect(calls.filter((c) => c.path === "/settings/proposals/set-1/approve")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "POST").map((c) => c.path)).toEqual(["/settings/proposals/set-1/approve", "/auth/step-up/begin", "/auth/step-up/finish", "/settings/proposals/set-1/approve"]);
    expect(calls.find((c) => c.path === "/settings/proposals/set-1/approve")!.body).toEqual({ contentHash: "sha256:c1", comment: "" });
  });

  it("lists settings changes with contract changes", async () => {
    fakeAPI({
      "GET /me": OPERATOR,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /changes?open=true&limit=200": { body: [] },
      "GET /settings/proposals?status=pending": { body: [{ ...PROPOSAL, mayDecide: false }] },
    });
    renderAt("/changes");
    const row = (await screen.findByRole("link", { name: /Size\/tiny: ekle/ })).closest("tr")!;
    expect(row).toHaveTextContent("Onay bekliyor");
    expect(row).toHaveTextContent("ayar");
    expect(row).toHaveTextContent("v3");
  });
});
