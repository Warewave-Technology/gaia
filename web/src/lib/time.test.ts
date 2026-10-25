import { describe, expect, it } from "vitest";
import { ago, duration } from "./time";

describe("time", () => {
  const now = Date.parse("2026-10-25T12:00:00Z");
  it("says how long ago", () => {
    expect(ago("2026-10-25T11:59:20Z", now)).toBe("40 sn önce");
    expect(ago("2026-10-25T11:48:00Z", now)).toBe("12 dk önce");
    expect(ago("2026-10-25T09:00:00Z", now)).toBe("3 sa önce");
    expect(ago("2026-10-20T12:00:00Z", now)).toBe("5 gün önce");
  });
  it("says how long it took", () => {
    expect(duration("2026-10-25T11:58:00Z", "2026-10-25T12:02:12Z")).toBe("4 dk 12 sn");
    expect(duration("2026-10-25T11:59:40Z", "2026-10-25T12:00:00Z")).toBe("20 sn");
  });
});
