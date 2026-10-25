import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../test-utils";

afterEach(() => vi.unstubAllGlobals());

describe("shell", () => {
  it("shows the menu, the person and the pending count", async () => {
    fakeAPI({
      "GET /me": { body: ME },
      "GET /changes?open=true&limit=500": { body: [{ id: "c1", status: "pending" }, { id: "c2", status: "running" }] },
    });
    renderAt("/runs");
    expect(await screen.findByText("ayse.k")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Runs/ })).toHaveClass("active");
    expect(await screen.findByTitle("Changes awaiting approval")).toHaveTextContent("1");
  });

  it("runs without a person on an open server", async () => {
    fakeAPI({ "GET /changes?open=true&limit=500": { body: [] } });
    renderAt("/");
    expect(await screen.findByText("sign-in off")).toBeInTheDocument();
  });

  it("sends a signed-out browser to the sign-in page", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, pathname: "/changes/c1", search: "" });
    fakeAPI({ "GET /me": { status: 401, body: { error: "sign in first" } } });
    renderAt("/changes/c1");
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith("/login?next=%2Fchanges%2Fc1"));
  });
});
