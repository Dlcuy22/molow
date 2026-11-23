import { get } from "svelte/store";
import { beforeEach, describe, expect, it, vi } from "vitest";

const libraryFeedCall = vi.fn();
const playlistsCall = vi.fn();

vi.mock("../../bindings/github.com/dlcuy22/molow/backend", () => ({
  PlayerService: {
    LibraryFeed: (id: string, tab: string, cursor: string, limit: number) =>
      libraryFeedCall(id, tab, cursor, limit),
    Playlists: (id: string) => playlistsCall(id),
  },
}));

import {
  invalidateLibraryCache,
  libraryId,
  libraryLoading,
  libraryPage,
  loadHome,
  loadLibrary,
} from "./library";

function makePage(shelfId: string) {
  return {
    shelves: [
      {
        id: shelfId,
        title: shelfId,
        subtitle: "",
        layout: "grid",
        kind: "albums",
        items: [],
        albums: [],
        artists: [],
        playlists: [],
        cursor: "",
        total: 0,
      },
    ],
    cursor: "",
  };
}

describe("loadLibrary tab cache", () => {
  beforeEach(() => {
    libraryFeedCall.mockReset();
    playlistsCall.mockReset();
    invalidateLibraryCache();
    libraryId.set("lib-1");
    libraryPage.set({ shelves: [], cursor: "" });
    libraryLoading.set(false);
  });

  it("caches each tab per library and skips reloading on repeated switches", async () => {
    libraryFeedCall.mockImplementation((_id: string, tab: string) =>
      Promise.resolve(makePage(`shelf-${tab}`)),
    );
    playlistsCall.mockResolvedValue([]);

    await loadLibrary("all");
    expect(get(libraryPage).shelves[0]?.id).toBe("shelf-all");
    expect(libraryFeedCall).toHaveBeenCalledTimes(1);

    await loadLibrary("albums");
    expect(get(libraryPage).shelves[0]?.id).toBe("shelf-albums");
    expect(libraryFeedCall).toHaveBeenCalledTimes(2);

    const loadingStates: boolean[] = [];
    const unsub = libraryLoading.subscribe((v) => loadingStates.push(v));
    await loadLibrary("all");
    await loadLibrary("albums");
    unsub();

    expect(libraryFeedCall).toHaveBeenCalledTimes(2);
    expect(playlistsCall).toHaveBeenCalledTimes(2);
    expect(get(libraryPage).shelves[0]?.id).toBe("shelf-albums");
    expect(loadingStates.every((v) => v === false)).toBe(true);
  });

  it("drops stale responses on rapid tab switching while still caching them", async () => {
    let resolveAlbums!: (v: ReturnType<typeof makePage>) => void;
    let resolveArtists!: (v: ReturnType<typeof makePage>) => void;
    libraryFeedCall.mockImplementation((_id: string, tab: string) => {
      if (tab === "albums") {
        return new Promise((r) => {
          resolveAlbums = r;
        });
      }

      return new Promise((r) => {
        resolveArtists = r;
      });
    });
    playlistsCall.mockResolvedValue([]);

    const pAlbums = loadLibrary("albums");
    const pArtists = loadLibrary("artists");

    resolveArtists(makePage("shelf-artists"));
    await pArtists;
    expect(get(libraryPage).shelves[0]?.id).toBe("shelf-artists");
    expect(get(libraryLoading)).toBe(false);

    // Late albums response must not overwrite the active artists tab.
    resolveAlbums(makePage("shelf-albums"));
    await pAlbums;
    expect(get(libraryPage).shelves[0]?.id).toBe("shelf-artists");

    // Switching back to albums now hits the cache with no extra IPC call.
    await loadLibrary("albums");
    expect(get(libraryPage).shelves[0]?.id).toBe("shelf-albums");
    expect(libraryFeedCall).toHaveBeenCalledTimes(2);
  });

  it("reloads after invalidateLibraryCache", async () => {
    libraryFeedCall
      .mockResolvedValueOnce(makePage("v1"))
      .mockResolvedValueOnce(makePage("v2"));
    playlistsCall.mockResolvedValue([]);

    await loadLibrary("all");
    expect(get(libraryPage).shelves[0]?.id).toBe("v1");

    invalidateLibraryCache("lib-1");
    await loadLibrary("all");
    expect(get(libraryPage).shelves[0]?.id).toBe("v2");
    expect(libraryFeedCall).toHaveBeenCalledTimes(2);
  });

  it("does not let loadHome clobber libraryLoading", async () => {
    let resolveHome!: (v: ReturnType<typeof makePage>) => void;
    libraryFeedCall.mockReturnValueOnce(
      new Promise((r) => {
        resolveHome = r;
      }),
    );

    libraryLoading.set(true);
    const p = loadHome();
    resolveHome(makePage("home"));
    await p;

    expect(get(libraryLoading)).toBe(true);
  });
});
