import { describe, expect, it } from "vitest";
import { clampBackdropSize } from "./backdrop-gl";

describe("clampBackdropSize", () => {
  it("caps 1080p at 960px on the longest side", () => {
    const res = clampBackdropSize(1920, 1080, 1);
    expect(res.width).toBe(960);
    expect(res.height).toBe(540);
  });

  it("caps 4K high-DPI at 960px", () => {
    const res = clampBackdropSize(2560, 1440, 2);
    expect(res.width).toBe(960);
    expect(res.height).toBe(540);
  });

  it("enforces the 320px floor on small viewports", () => {
    const res = clampBackdropSize(400, 300, 1);
    expect(res.width).toBe(320);
    expect(res.height).toBe(240);
  });

  it("preserves portrait aspect ratio when height is the longest side", () => {
    const res = clampBackdropSize(600, 1200, 1);
    expect(res.height).toBe(600);
    expect(res.width).toBe(300);
  });
});
