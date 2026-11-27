import { get } from "svelte/store";
import { beforeEach, describe, expect, it, vi } from "vitest";

const importYTM = vi.fn();
const importYTMSong = vi.fn();
const ytmAddTargets = vi.fn();
const librariesFn = vi.fn(async () => [
  { id: "ytm:UC1", kind: "ytm", name: "YouTube Music", status: "ready", capabilities: [] },
]);
const libraryFeed = vi.fn(async () => ({ shelves: [], cursor: "" }));
const playlistsFn = vi.fn(async () => []);
const showToast = vi.fn();

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(() => () => {}) },
}));

vi.mock("../../bindings/github.com/dlcuy22/molow/backend", () => ({
  PlayerService: {
    ImportYTM: (...a: unknown[]) => importYTM(...a),
    ImportYTMSong: (...a: unknown[]) => importYTMSong(...a),
    YTMAddTargets: (...a: unknown[]) => ytmAddTargets(...a),
    Libraries: () => librariesFn(),
    LibraryFeed: () => libraryFeed(),
    Playlists: () => playlistsFn(),
  },
}));

vi.mock("./toast", () => ({
  showToast: (...a: unknown[]) => showToast(...a),
}));

import {
  closeImportSongModal,
  importSongModal,
  importYTM as runImportYTM,
  importYTMSong as runImportYTMSong,
  libraryId,
} from "./library";
import { activityStore, resetActivities } from "./activity";

describe("YTM song import flow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetActivities();
    libraryId.set("ytm:UC1");
    closeImportSongModal();
  });

  it("opens importSongModal when backend returns needsTarget", async () => {
    importYTM.mockResolvedValue({ name: "Track", tracks: 0, needsTarget: true });
    const hit = {
      videoId: "v1",
      title: "Track",
      artist: "Artist",
      album: "",
      duration: "",
      durationMs: 0,
      thumbnail: "",
      kind: "Song",
      playable: true,
    };

    const ok = await runImportYTM(hit as never);
    expect(ok).toBe(true);
    expect(get(importSongModal)).toEqual({ libraryId: "ytm:UC1", hit });
    expect(showToast).not.toHaveBeenCalled();
  });

  it("returns duplicate without refreshing so the modal can prompt", async () => {
    importYTMSong.mockResolvedValue({ playlistId: "pl1", name: "Mix", tracks: 0, duplicate: true });
    const res = await runImportYTMSong("ytm:UC1", { videoId: "v1", title: "Track" } as never, {
      playlistId: "pl1",
    });

    expect(res.duplicate).toBe(true);
    expect(importYTMSong).toHaveBeenCalledWith(
      "ytm:UC1",
      expect.objectContaining({ videoId: "v1" }),
      expect.objectContaining({ playlistId: "pl1", allowDuplicate: false }),
    );
    expect(showToast).not.toHaveBeenCalled();
  });

  it("shows toast and refreshes once AllowDuplicate or Liked succeeds", async () => {
    importYTMSong.mockResolvedValue({ playlistId: "", name: "Liked Music", tracks: 1 });
    const res = await runImportYTMSong("ytm:UC1", { videoId: "v1", title: "Track" } as never, {
      liked: true,
    });

    expect(res.tracks).toBe(1);
    expect(showToast).toHaveBeenCalledWith(
      expect.objectContaining({ label: "Saved to Liked Music" }),
    );
  });

  it("records retryable LibraryActivity error when playlist import fails and retries via onRetry", async () => {
    importYTM.mockRejectedValueOnce(
      new Error('Post "https://music.youtube.com/youtubei/v1/browse": context deadline exceeded'),
    );
    const playlistHit = {
      videoId: "PLRVoS5aPlCux4WnrldDgS2Z0LyYhBChth",
      title: "Add YouTube Music playlist",
      artist: "",
      album: "",
      duration: "",
      durationMs: 0,
      thumbnail: "",
      kind: "Playlist",
      playable: false,
    };

    const ok = await runImportYTM(playlistHit as never);
    expect(ok).toBe(false);

    const item = get(activityStore)["ytm:UC1"]?.items[0];
    expect(item?.state).toBe("error");
    expect(item?.detail).toBe("Uh oh, failed to import the ytm playlist. wanna try again?");
    expect(item?.retryLabel).toBe("again");
    expect(item?.rawError).toContain("context deadline exceeded");
    expect(typeof item?.onRetry).toBe("function");

    importYTM.mockResolvedValueOnce({ playlistId: "pl-1", name: "capek :(", tracks: 12 });
    item?.onRetry?.();
    await vi.waitFor(() => expect(importYTM).toHaveBeenCalledTimes(2));
    expect(importYTM).toHaveBeenLastCalledWith("ytm:UC1", playlistHit);
  });
});
