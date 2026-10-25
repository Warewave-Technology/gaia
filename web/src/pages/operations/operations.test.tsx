import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fakeAPI, ME, renderAt } from "../../test-utils";
import { recurrence } from "./Operations";

afterEach(() => vi.unstubAllGlobals());

describe("operations", () => {
  it("reads the schedules' recurrences", () => {
    expect(recurrence("FREQ=WEEKLY;BYDAY=SA")).toBe("Her hafta Cumartesi");
    expect(recurrence("FREQ=MONTHLY;BYDAY=2TU")).toBe("Her ay ayın 2. Salı");
    expect(recurrence("FREQ=DAILY;INTERVAL=2")).toBe("Her 2 gün");
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
    const sched = (await screen.findByText(/Her hafta Cumartesi/)).closest("tr")!;
    expect(sched).toHaveTextContent("Her hafta Cumartesi · 03:00 UTC");
    expect(sched).toHaveTextContent("yeniden başlatma");
    const patch = screen.getByText("patch-1").closest("tr")!;
    expect(patch).toHaveTextContent("Kısmen");
    expect(screen.getByRole("link", { name: "run-1" })).toHaveAttribute("href", "/runs/run-1");
    expect(screen.getByText("weekly-reboot-20261031").closest("tr")).toHaveTextContent("Bekliyor");
  });

  it("is for operators", async () => {
    fakeAPI({ "GET /me": { body: { ...ME, roles: ["viewer"] } }, "GET /changes?open=true&limit=500": { body: [] } });
    renderAt("/operations");
    expect(await screen.findByText(/operator ve platform-admin/)).toBeInTheDocument();
  });
});
