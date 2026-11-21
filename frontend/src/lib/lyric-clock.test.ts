import { describe, expect, it } from "vitest";
import {
  activeLineAt,
  JITTER_HOLD_MS,
  lyricOffsetFor,
  lyricProviderInfo,
  lyricSyncLabel,
  nextClockWakeMs,
  seekTargetMs,
  wordOpacity,
} from "./lyric-clock";

const lines = [
  { timeMs: 1130 },
  { timeMs: 3980 },
  { timeMs: 5690 },
];

describe("lyricOffsetFor", () => {
  it("applies the delay only to petitlyrics sheets", () => {
    expect(lyricOffsetFor("petitlyrics", -700)).toBe(-700);
    expect(lyricOffsetFor("petitlyrics", 500)).toBe(500);
    expect(lyricOffsetFor("betterlyrics", -700)).toBe(0);
    expect(lyricOffsetFor(undefined, -700)).toBe(0);
  });
});

describe("activeLineAt", () => {
  it("switches on the exact millisecond boundary", () => {
    expect(activeLineAt(lines, 0)).toBe(0);
    expect(activeLineAt(lines, 3979)).toBe(0);
    expect(activeLineAt(lines, 3980)).toBe(1);
    expect(activeLineAt(lines, 5690)).toBe(2);
  });

  it("shifts activation earlier with negative delay and later with positive delay", () => {
    // Late lyrics shifted by -700ms activate 700ms earlier in track time.
    const lateOffset = lyricOffsetFor("petitlyrics", -700);
    expect(activeLineAt(lines, 3279 - lateOffset)).toBe(0);
    expect(activeLineAt(lines, 3280 - lateOffset)).toBe(1);

    // Early lyrics shifted by +500ms activate 500ms later in track time.
    const earlyOffset = lyricOffsetFor("petitlyrics", 500);
    expect(activeLineAt(lines, 4479 - earlyOffset)).toBe(0);
    expect(activeLineAt(lines, 4480 - earlyOffset)).toBe(1);
  });

  it("holds the active line across small snapshot re-anchor jitter but follows real seeks", () => {
    expect(activeLineAt(lines, 3960, 1, JITTER_HOLD_MS)).toBe(1);
    expect(activeLineAt(lines, 3980 - JITTER_HOLD_MS, 1, JITTER_HOLD_MS)).toBe(0);
    expect(activeLineAt(lines, 3960, 1, 0)).toBe(0);
  });
});

describe("seekTargetMs", () => {
  it("offsets click-to-seek targets and clamps at zero", () => {
    expect(seekTargetMs(19710, -700)).toBe(19010);
    expect(seekTargetMs(19710, 500)).toBe(20210);
    expect(seekTargetMs(500, -1000)).toBe(0);
  });
});

describe("lyricProviderInfo", () => {
  it("maps known providers to display name and website", () => {
    expect(lyricProviderInfo("petitlyrics")).toEqual({
      name: "PetitLyrics",
      url: "https://petitlyrics.com",
    });
    expect(lyricProviderInfo("BetterLyrics")).toEqual({
      name: "BetterLyrics",
      url: "https://betterlyrics.org",
    });
    expect(lyricProviderInfo("ytmusic")?.name).toBe("YouTube Music");
    expect(lyricProviderInfo("unknown")).toBeNull();
    expect(lyricProviderInfo(undefined)).toBeNull();
  });
});

describe("lyricSyncLabel", () => {
  it("distinguishes word synced from line synced sheets", () => {
    expect(lyricSyncLabel([{ timeMs: 1000, words: [{ text: "a" }] }], true)).toBe("word synced");
    expect(lyricSyncLabel([{ timeMs: 1000 }], true)).toBe("line synced");
    expect(lyricSyncLabel([{ timeMs: -1 }], false)).toBeNull();
  });
});

describe("wordOpacity", () => {
  it("interpolates from 0.3 to 1 and snaps zero-length words", () => {
    expect(wordOpacity(900, 1000, 1200)).toBe("0.3");
    expect(wordOpacity(1000, 1000, 1200)).toBe("0.3");
    expect(wordOpacity(1100, 1000, 1200)).toBe("0.65");
    expect(wordOpacity(1200, 1000, 1200)).toBe("1");
    expect(wordOpacity(1001, 1000, 1000)).toBe("1");
  });
});

describe("nextClockWakeMs", () => {
  const sheet = [
    {
      timeMs: 1000,
      words: [
        { startMs: 1000, endMs: 1200 },
        { startMs: 1500, endMs: 1800 },
      ],
    },
    { timeMs: 3000 },
  ];

  it("returns 0 while a word is actively sweeping", () => {
    expect(nextClockWakeMs(sheet, 0, 1000)).toBe(0);
    expect(nextClockWakeMs(sheet, 0, 1150)).toBe(0);
  });

  it("returns exact ms until the next word start or next line boundary", () => {
    expect(nextClockWakeMs(sheet, 0, 1250)).toBe(250);
    expect(nextClockWakeMs(sheet, 0, 1800)).toBe(1200);
  });

  it("returns null once no further words or timed lines remain", () => {
    expect(nextClockWakeMs(sheet, 1, 3500)).toBeNull();
  });
});
