import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";

afterEach(() => vi.unstubAllGlobals());

const ROUTES = {
  "GET /me": { body: ME },
  "GET /changes?open=true&limit=500": { body: [] },
  "GET /catalog?kind=Group,Team&spec=1": { body: { commit: "c", objects: [
    { kind: "Group", name: "web-admins", source: "groups.yaml:1", usedBy: 1, contracts: 1, spec: { description: "web admins", teams: ["team-web"], owner: "team-web", privileged: true } },
    { kind: "Team", name: "team-web", source: "", generatedBy: "TeamSource/corp-ad", usedBy: 2, contracts: 1, spec: { displayName: "Web", adGroup: { dn: "CN=web,OU=Groups,DC=corp" } } },
  ] } },
  "GET /access": { body: [
    { contract: "web", group: "web-admins", sudo: true },
    { contract: "web", team: "team-web", commands: ["/usr/bin/systemctl restart nginx"] },
    { contract: "db", team: "team-web" },
  ] },
};

describe("access", () => {
  it("shows who may log in to each contract and with what sudo", async () => {
    fakeAPI(ROUTES);
    renderAt("/access");
    const admins = (await screen.findByRole("link", { name: "web-admins" })).closest("tr")!;
    expect(admins).toHaveTextContent("full sudo");
    expect(screen.getByText("/usr/bin/systemctl restart nginx")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "db" }).closest("tr")).toHaveTextContent("no sudo");
    expect(screen.getAllByRole("link", { name: "web" })).toHaveLength(1);
  });

  it("lists groups and teams", async () => {
    fakeAPI(ROUTES);
    renderAt("/access/Group");
    expect((await screen.findByRole("link", { name: "web-admins" })).closest("tr")).toHaveTextContent("privileged");
    expect(screen.getByRole("link", { name: /Teams/ })).toHaveTextContent("1");
  });
});
