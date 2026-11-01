import { describe, expect, it } from "vitest";
import { nextRepeatTarget } from "./repeat";

describe("nextRepeatTarget", () => {
  it("turns the active mode off", () => {
    expect(nextRepeatTarget(null, "all", "all")).toBe("off");
    expect(nextRepeatTarget(null, "one", "one")).toBe("off");
  });

  it("turns a mode on from off or the other mode", () => {
    expect(nextRepeatTarget(null, "off", "all")).toBe("all");
    expect(nextRepeatTarget(null, "off", "one")).toBe("one");
    expect(nextRepeatTarget(null, "one", "all")).toBe("all");
    expect(nextRepeatTarget(null, "all", "one")).toBe("one");
  });

  it("counts a pending press as the current mode", () => {
    // Two presses inside one snapshot window: the server still says off, but
    // the second press must turn off the mode the first one just asked for.
    expect(nextRepeatTarget("all", "off", "all")).toBe("off");
    expect(nextRepeatTarget("one", "off", "one")).toBe("off");
    // Pressing the other mode while one is in flight is a switch, not an off.
    expect(nextRepeatTarget("all", "off", "one")).toBe("one");
  });
});
