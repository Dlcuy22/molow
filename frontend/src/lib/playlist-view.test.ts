import { describe, expect, it } from "vitest";
import {
  formatTotalDuration,
  formatTrackCount,
  isPlaylistQueueActive,
  playlistCovers,
  sortPlaylistTracks,
  totalDurationMs,
} from "./playlist-view";
import type { Track } from "./types";

function track(over: Partial<Track>): Track {
  return {
    ref: "ytm:id",
    libraryId: "local:test",
    nativeId: "id",
    title: "Song",
    artist: "Artist",
    album: "",
    durationMs: 180_000,
    coverId: "",
    liked: false,
    flags: 1,
    trackNo: 0,
    ...over,
  };
}

describe("playlist-view helpers", () => {
  it("keeps original order for order sort and sorts deterministically otherwise", () => {
    const a = track({ ref: "1", title: "Zeta", artist: "Beta", durationMs: 240_000, liked: false });
    const b = track({ ref: "2", title: "Alpha", artist: "Gamma", durationMs: 120_000, liked: true });
    const c = track({ ref: "3", title: "Middle", artist: "Alpha", durationMs: 180_000, liked: false });
    const list = [a, b, c];

    expect(sortPlaylistTracks(list, "order")).toBe(list);
    expect(sortPlaylistTracks(list, "title").map((t) => t.ref)).toEqual(["2", "3", "1"]);
    expect(sortPlaylistTracks(list, "artist").map((t) => t.ref)).toEqual(["3", "1", "2"]);
    expect(sortPlaylistTracks(list, "duration").map((t) => t.ref)).toEqual(["2", "3", "1"]);
    expect(sortPlaylistTracks(list, "liked").map((t) => t.ref)).toEqual(["2", "1", "3"]);
  });

  it("collects at most four distinct non-empty cover ids", () => {
    const list = [
      track({ coverId: "" }),
      track({ coverId: "c1" }),
      track({ coverId: "c1" }),
      track({ coverId: "c2" }),
      track({ coverId: "c3" }),
      track({ coverId: "c4" }),
      track({ coverId: "c5" }),
    ];

    expect(playlistCovers(list)).toEqual(["c1", "c2", "c3", "c4"]);
  });

  it("sums positive durations and formats total length", () => {
    const list = [
      track({ durationMs: 3_600_000 }),
      track({ durationMs: 2 * 60_000 }),
      track({ durationMs: -1 }),
    ];

    expect(totalDurationMs(list)).toBe(3_720_000);
    expect(formatTotalDuration(3_720_000)).toBe("1 hr 2 min");
    expect(formatTotalDuration(3_600_000)).toBe("1 hr");
    expect(formatTotalDuration(45 * 60_000)).toBe("45 min");
    expect(formatTotalDuration(30_000)).toBe("< 1 min");
    expect(formatTotalDuration(0)).toBe("");
    expect(formatTrackCount(1)).toBe("1 song");
    expect(formatTrackCount(66)).toBe("66 songs");
  });

  it("tracks whether the live queue matches the selected playlist", () => {
    const tracks = [track({ ref: "a" }), track({ ref: "b" }), track({ ref: "c" })];
    const fullLaunch = { playlistId: "pl:1", refs: ["a", "b", "c"] };

    // Playing or paused on the playlist queue stays active, even when shuffled.
    expect(isPlaylistQueueActive("pl:1", tracks, ["a", "b", "c"], "playing", fullLaunch)).toBe(true);
    expect(isPlaylistQueueActive("pl:1", tracks, ["c", "a", "b"], "paused", fullLaunch)).toBe(true);
    expect(isPlaylistQueueActive("pl:1", tracks, ["a", "b", "c"], "stopped", fullLaunch)).toBe(false);

    // Restored session queue matching the full playlist without an in-memory launch record.
    expect(isPlaylistQueueActive("pl:1", tracks, ["a", "b", "c"], "playing", null)).toBe(true);

    // Mid-playlist start stays active for its launched slice.
    const sliceLaunch = { playlistId: "pl:1", refs: ["b", "c"] };
    expect(isPlaylistQueueActive("pl:1", tracks, ["b", "c"], "playing", sliceLaunch)).toBe(true);

    // Adding an external track or an extra copy of a playlist track invalidates active state.
    expect(isPlaylistQueueActive("pl:1", tracks, ["a", "x", "b", "c"], "playing", fullLaunch)).toBe(false);
    expect(isPlaylistQueueActive("pl:1", tracks, ["a", "a", "b", "c"], "playing", fullLaunch)).toBe(false);
    expect(isPlaylistQueueActive("pl:1", tracks, ["b", "a", "c"], "playing", sliceLaunch)).toBe(false);

    // Playing a single unrelated/isolated track without launching this playlist is not active.
    expect(isPlaylistQueueActive("pl:1", tracks, ["a"], "playing", null)).toBe(false);

    // A different playlist with identical tracks is not active when another playlist was launched.
    expect(isPlaylistQueueActive("pl:2", tracks, ["a", "b", "c"], "playing", fullLaunch)).toBe(false);
  });
});
