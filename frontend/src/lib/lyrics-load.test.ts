import { beforeEach, describe, expect, it, vi } from "vitest";

const lyricsCall = vi.fn();

vi.mock("../../bindings/github.com/dlcuy22/molow/backend", () => ({
  PlayerService: {
    Lyrics: (path: string) => lyricsCall(path),
  },
}));

import { loadLyrics } from "./library";

// sheet builds the document shape the binding returns, with the fields the
// loader reads.
function sheet(synced: boolean) {
  return {
    lines: [{ timeMs: synced ? 1200 : 0, text: "a line", translation: "" }],
    synced,
    source: "ytmusic",
  };
}

describe("loadLyrics", () => {
  beforeEach(() => {
    lyricsCall.mockReset();
  });

  it("keeps a timed sheet", async () => {
    lyricsCall.mockResolvedValue(sheet(true));

    await expect(loadLyrics("ytm:abc123")).resolves.toEqual(sheet(true));
  });

  it("rejects an untimed sheet as no lyrics", async () => {
    // A catalogue video has lyrics but no cue ranges, and the player follows a
    // line by its timestamp, so showing them would leave the column stuck.
    lyricsCall.mockResolvedValue(sheet(false));

    await expect(loadLyrics("ytm:abc123")).resolves.toBeNull();
  });

  it("rejects a local sheet that carries no timestamps", async () => {
    lyricsCall.mockResolvedValue(sheet(false));

    await expect(loadLyrics("/music/01. Song.flac")).resolves.toBeNull();
  });

  it("treats a missing document and a failed request as no lyrics", async () => {
    lyricsCall.mockResolvedValueOnce(null);
    await expect(loadLyrics("ytm:abc123")).resolves.toBeNull();

    lyricsCall.mockRejectedValueOnce(new Error("network down"));
    await expect(loadLyrics("ytm:abc123")).resolves.toBeNull();
  });

  it("does not call the backend without a track", async () => {
    await expect(loadLyrics("")).resolves.toBeNull();
    expect(lyricsCall).not.toHaveBeenCalled();
  });
});
