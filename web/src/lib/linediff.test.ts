import { describe, expect, it } from "vitest";
import { lineDiff } from "./linediff";

describe("lineDiff", () => {
  it("keeps, removes and adds lines", () => {
    expect(lineDiff("a\nb\nc\n", "a\nx\nc\n")).toEqual([
      { op: "same", text: "a" }, { op: "del", text: "b" }, { op: "add", text: "x" }, { op: "same", text: "c" },
    ]);
    expect(lineDiff("", "a\n")).toEqual([{ op: "add", text: "a" }]);
    expect(lineDiff("a\n", "")).toEqual([{ op: "del", text: "a" }]);
  });
});
