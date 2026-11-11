import { describe, expect, it } from "vitest";
import type { LyricWord } from "./types";
import { sweepSegments, type SweepSegment } from "./word-sweep";

const word = (text: string, startMs: number, endMs: number): LyricWord => ({
  text,
  startMs,
  endMs,
});

const joined = (segments: SweepSegment[] | null): string =>
  (segments ?? []).map((segment) => segment.text).join("");

describe("sweepSegments", () => {
  it("keeps the line text and leaves the timed spaces unhighlighted", () => {
    // The shape the Indonesian sheet carries: one segment per character and an
    // empty segment where the provider timed the space.
    const words = [
      word("H", 36228, 36635),
      word("a", 36635, 36685),
      word("r", 36685, 36735),
      word("u", 36735, 36902),
      word("", 36902, 37234),
      word("a", 37234, 37367),
      word("i", 37367, 37467),
      word("r", 37467, 37634),
    ];

    const segments = sweepSegments("Haru air", words);
    expect(joined(segments)).toBe("Haru air");
    expect(segments?.filter((s) => s.word).map((s) => s.text).join("")).toBe(
      "Haruair",
    );
    expect(segments?.find((s) => s.text === " ")?.word).toBeUndefined();
  });

  it("does not invent a space when a word is split in two", () => {
    const segments = sweepSegments("bothering me", [
      word("bothe", 0, 400),
      word("ring", 400, 800),
      word("me", 800, 1000),
    ]);

    // "bothe" and "ring" are one word, so the seam between them is not a gap.
    expect(joined(segments)).toBe("bothering me");
    expect(segments?.map((s) => s.text)).toEqual(["bothe", "ring", " ", "me"]);
  });

  it("renders a line with no spaces as plain word spans", () => {
    const segments = sweepSegments("あのね", [
      word("あの", 0, 500),
      word("ね", 500, 1000),
    ]);

    expect(segments?.map((s) => ({ text: s.text, timed: !!s.word }))).toEqual([
      { text: "あの", timed: true },
      { text: "ね", timed: true },
    ]);
  });

  it("falls back when a word does not belong to the line", () => {
    expect(sweepSegments("Haru", [word("x", 0, 100)])).toBeNull();
  });

  it("returns the whole line as one gap when nothing is timed", () => {
    expect(sweepSegments("a plain line", [])).toEqual([{ text: "a plain line" }]);
  });
});
