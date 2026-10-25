import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

vi.mock("@simplewebauthn/browser", () => ({
  startAuthentication: vi.fn(async () => ({ id: "cred", rawId: "cred", type: "public-key", response: {} })),
}));

afterEach(() => vi.unstubAllGlobals());

const ADMIN = { body: { ...ME, roles: ["viewer", "platform-admin"] } };
const USERS = { body: [
  { id: "u1", username: "can", displayName: "Can Demir", provider: "lab-ad", enabled: true, teams: ["team-web"], directory: true },
  { id: "u2", username: "ali", provider: "lab-ad", enabled: false, teams: ["team-web"], directory: true },
  { id: "u3", username: "breakglass", provider: "local", enabled: true, teams: ["team-platform"], directory: false, breakGlass: true },
] };

describe("users", () => {
  it("lists directory and break-glass accounts, and opens a break-glass account", async () => {
    const calls = fakeAPI({
      "GET /me": ADMIN,
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /users": USERS,
      "GET /auth/config": { body: { require: "mfa", providers: [{ name: "corp", type: "ldap" }, { name: "local", type: "local" }] } },
      "POST /auth/step-up/begin": { body: { flow: "f1", options: { publicKey: { challenge: "abc", allowCredentials: [] } } } },
      "POST /auth/step-up/finish": { status: 204 },
      "POST /users": { status: 201, body: { enrollUrl: "http://localhost:8080/enroll#tok" } },
    });
    renderAt("/settings/users");
    const can = (await screen.findByText("can")).closest("tr")!;
    expect(can).toHaveTextContent("dizin · lab-ad");
    expect(can).toHaveTextContent("açık");
    expect(within(can).queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.getByText("ali").closest("tr")).toHaveTextContent("dizinde kapalı");
    const bg = screen.getByText("breakglass").closest("tr")!;
    expect(bg).toHaveTextContent("break-glass");
    expect(within(bg).getByRole("switch")).toBeInTheDocument();
    expect(within(bg).getByRole("button", { name: /Yeniden kayıt/ })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Break-glass hesabı/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Kullanıcı adı"), { target: { value: "emergency" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Aç/ }));
    expect(await screen.findByText("http://localhost:8080/enroll#tok")).toBeInTheDocument();
    expect(calls.find((c) => c.path === "/users" && c.method === "POST")!.body).toEqual({ username: "emergency", displayName: "", teams: ["team-platform"], breakGlass: true });
  });
});
