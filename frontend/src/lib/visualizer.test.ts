import { describe, expect, it } from "vitest";
import {
  DEFAULT_VISUALIZER_BARS,
  FULL_VISUALIZER_BARS,
  visualizerBars,
} from "./visualizer";

describe("visualizerBars", () => {
  it("gives the full sheet the maximum point count", () => {
    expect(visualizerBars("full")).toBe(256);
    expect(FULL_VISUALIZER_BARS).toBe(256);
  });

  it("keeps the compact strip at the shipped default", () => {
    expect(visualizerBars("minimal")).toBe(150);
    expect(visualizerBars(undefined)).toBe(150);
    expect(visualizerBars("sideways")).toBe(150);
    expect(DEFAULT_VISUALIZER_BARS).toBe(150);
  });
});
