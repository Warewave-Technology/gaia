import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt, type Call } from "../../test-utils";

vi.mock("@simplewebauthn/browser", () => ({
  startAuthentication: vi.fn(async () => ({ id: "cred", type: "public-key", response: {} })),
  startRegistration: vi.fn(async () => ({ id: "new", type: "public-key", response: {} })),
}));

let calls: Call[];
let strong = false;
beforeEach(() => {
  strong = false;
  calls = fakeAPI({
    "GET /me": { body: ME },
    "GET /changes?open=true&limit=500": { body: [] },
    "GET /me/sessions": { body: [{ id: "s1", kind: "ui", current: true, createdAt: "2026-10-25T08:00:00Z", lastSeenAt: "2026-10-25T08:30:00Z", expiresAt: "2026-10-25T16:00:00Z", ip: "10.0.0.5", userAgent: "Firefox" }] },
    "GET /me/passkeys": { body: [{ id: "p1", name: "laptop", createdAt: "2026-10-01T08:00:00Z" }] },
    "GET /me/signing-keys": { body: [] },
    "GET /me/login-keys": { body: [] },
    "GET /me/notifications": { body: { email: "ayse@corp.local", emailEditable: false, triggers: ["change.pending"], available: ["change.pending", "change.failed"] } },
    "PUT /me/notifications": { body: { email: "ayse@corp.local", emailEditable: false, triggers: ["change.pending", "change.failed"], available: ["change.pending", "change.failed"] } },
    "POST /auth/step-up/begin": { body: { flow: "f", options: { publicKey: { challenge: "c" } } } },
    "POST /auth/step-up/finish": () => { strong = true; return { status: 204 }; },
    "POST /me/signing-keys": () => (strong ? { status: 201, body: { fingerprint: "SHA256:x" } } : { status: 403, body: { error: "confirm with your passkey first", stepUp: true } }),
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("profile", () => {
  it("adds a signing key from a modal, confirming with a passkey when Gaia asks", async () => {
    renderAt("/profile");
    const card = (await screen.findByRole("heading", { name: "Signing keys" })).closest("section")!;
    fireEvent.click(within(card).getByRole("button", { name: /Add key/ }));
    const dialog = await screen.findByRole("dialog", { name: "Add a signing key" });
    fireEvent.change(within(dialog).getByPlaceholderText(/ssh-ed25519 AAAA/), { target: { value: "ssh-ed25519 AAAAC3Nza me@laptop" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));
    await vi.waitFor(() => expect(calls.filter((c) => c.path === "/me/signing-keys" && c.method === "POST")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "POST").map((c) => c.path)).toEqual(["/me/signing-keys", "/auth/step-up/begin", "/auth/step-up/finish", "/me/signing-keys"]);
    await vi.waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("closes a modal with Escape and asks before removing", async () => {
    renderAt("/profile");
    const card = (await screen.findByRole("heading", { name: "Passkeys" })).closest("section")!;
    fireEvent.click(within(card).getByRole("button", { name: /Add passkey/ }));
    expect(await screen.findByRole("dialog", { name: "Add a passkey" })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // The only passkey cannot be removed.
    expect(within(card).getByRole("button", { name: /Remove/ })).toBeDisabled();
  });

  it("saves a notification at once when its switch flips", async () => {
    renderAt("/profile");
    const sw = await screen.findByRole("switch", { name: "Failed" });
    expect(sw).toHaveAttribute("aria-checked", "false");
    fireEvent.click(sw);
    await vi.waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({ email: null, triggers: ["change.pending", "change.failed"] });
    await vi.waitFor(() => expect(screen.getByRole("switch", { name: "Failed" })).toHaveAttribute("aria-checked", "true"));
    expect(screen.getByText("this session")).toBeInTheDocument();
  });
});
