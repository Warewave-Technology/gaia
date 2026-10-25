import { screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";
import { recurrence } from "./Operations";

afterEach(() => vi.unstubAllGlobals());

describe("operations", () => {
  it("reads the schedules' recurrences", () => {
    expect(recurrence("FREQ=WEEKLY;BYDAY=SA")).toBe("Every week on Saturday");
    expect(recurrence("FREQ=MONTHLY;BYDAY=2TU")).toBe("Every month on the 2nd Tuesday");
    expect(recurrence("FREQ=DAILY;INTERVAL=2")).toBe("Every 2 days");
    expect(recurrence("FREQ=YEARLY")).toBe("FREQ=YEARLY");
  });

  it("lists schedules and operations with their state", async () => {
    fakeAPI({
      "GET /me": { body: ME },
      "GET /changes?open=true&limit=500": { body: [] },
      "GET /schedules": { body: [{ name: "weekly-reboot", recurrence: "FREQ=WEEKLY;BYDAY=SA", start: "03:00", duration: "2h", approval: "once", action: "reboot", selector: 'labels.env == "test"', operations: ["weekly-reboot-20261031"] }] },
      "GET /operations": { body: [
        { name: "patch-1", status: "partial", change: "ch_1", run: "run-1", action: "patch", selector: "labels.app == \"poc\"", updatedAt: "2026-10-25T08:00:00Z" },
        { name: "weekly-reboot-20261031", action: "reboot", selector: 'labels.env == "test"', window: { start: "2026-10-31T03:00:00Z", end: "2026-10-31T05:00:00Z" }, schedule: "OperationSchedule/weekly-reboot" },
      ] },
    });
    renderAt("/operations");
    const sched = (await screen.findByText(/Every week on Saturday/)).closest("tr")!;
    expect(sched).toHaveTextContent("Every week on Saturday · 03:00 UTC");
    expect(within(sched).getByText("reboot", { selector: "td" })).toBeInTheDocument();
    const patch = screen.getByText("patch-1").closest("tr")!;
    expect(patch).toHaveTextContent("Partial");
    expect(screen.getByRole("link", { name: "run-1" })).toHaveAttribute("href", "/runs/run-1");
    expect(screen.getByText("weekly-reboot-20261031").closest("tr")).toHaveTextContent("Waiting");
  });

  it("is for operators", async () => {
    fakeAPI({ "GET /me": { body: { ...ME, roles: ["viewer"] } }, "GET /changes?open=true&limit=500": { body: [] } });
    renderAt("/operations");
    expect(await screen.findByText(/for operators and platform admins/)).toBeInTheDocument();
  });
});
