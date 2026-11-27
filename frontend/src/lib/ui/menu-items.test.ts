import { describe, expect, it } from "vitest";
import { FLAG_LIKEABLE, FLAG_PLAYABLE, type Album, type Playlist, type Track } from "../types";
import { menuItemsFor, type MenuDeps } from "./menu-items";
import type { MenuTarget } from "./context-menu.svelte";

function track(over: Partial<Track> = {}): Track {
  return {
    ref: "/music/a.flac",
    libraryId: "local:test",
    nativeId: "a.flac",
    title: "A",
    artist: "Artist",
    album: "Album",
    durationMs: 1000,
    coverId: "",
    liked: false,
    flags: FLAG_PLAYABLE | FLAG_LIKEABLE,
    trackNo: 1,
    ...over,
  };
}

function playlist(over: Partial<Playlist> = {}): Playlist {
  return {
    id: "playlist:1",
    libraryId: "local:test",
    name: "Mix",
    description: "",
    trackCount: 3,
    ...over,
  };
}

const album: Album = {
  id: "album:1",
  libraryId: "local:test",
  name: "Album",
  artist: "Artist",
  coverId: "",
  songCount: 2,
  durationMs: 2000,
  year: 2020,
};

function deps(over: Partial<MenuDeps> = {}): MenuDeps {
  return {
    playlists: [],
    next: () => {},
    last: () => {},
    like: () => {},
    addToPlaylist: () => {},
    albumRefs: async () => [],
    play: () => {},
    openAlbum: () => {},
    openPlaylist: () => {},
    renamePlaylist: () => {},
    deletePlaylist: () => {},
    openLibrary: (_id: string) => {},
    editLibrary: (_id: string) => {},
    rescanLibrary: (_id: string) => {},
    removeLibrary: (_id: string) => {},
    ...over,
  };
}

function labels(items: ReturnType<typeof menuItemsFor>): string[] {
  return items.filter((i) => i.kind === "item").map((i) => i.label ?? "");
}

describe("menuItemsFor track", () => {
  const target = (t: Track): MenuTarget => ({
    kind: "track",
    id: t.ref,
    label: t.title,
    libraryId: t.libraryId,
    track: t,
  });

  it("offers the two queue rows and a like row", () => {
    expect(labels(menuItemsFor(target(track()), deps()))).toEqual([
      "Add to next",
      "Add to queue",
      "Like",
    ]);
  });

  it("names the outcome of the like row from the row's state", () => {
    const items = menuItemsFor(target(track({ liked: true })), deps());

    expect(labels(items)).toContain("Remove from Liked");
    expect(labels(items)).not.toContain("Like");
  });

  it("drops the like row when the library cannot like", () => {
    const items = menuItemsFor(target(track({ flags: FLAG_PLAYABLE })), deps());

    expect(labels(items)).not.toContain("Like");
  });

  it("disables the queue rows for a track with no playable ref", () => {
    const items = menuItemsFor(target(track({ flags: 0 })), deps());
    const queue = items.filter((i) => i.kind === "item" && i.label !== "Like");

    expect(queue.every((i) => i.disabled)).toBe(true);
  });

  it("names the single playlist instead of opening a submenu", () => {
    const items = menuItemsFor(target(track()), deps({ playlists: [playlist()] }));

    expect(labels(items)).toContain("Add to Mix");
  });

  it("lists every playlist when there is more than one", () => {
    const items = menuItemsFor(
      target(track()),
      deps({ playlists: [playlist(), playlist({ id: "playlist:2", name: "Drive" })] }),
    );

    expect(labels(items)).toContain("Mix");
    expect(labels(items)).toContain("Drive");
  });

  it("hides the playlist rows when the library has none", () => {
    const items = menuItemsFor(target(track()), deps());
    const queue = new Set(["Add to next", "Add to queue"]);

    expect(labels(items).filter((l) => l.startsWith("Add to") && !queue.has(l))).toEqual([]);
  });

  it("omits foreign and Liked Music playlists from track add targets", () => {
    const items = menuItemsFor(
      target(track()),
      deps({
        playlists: [
          playlist({ id: "pl-lm", name: "Liked Music", remoteId: "LM", owned: false }),
          playlist({ id: "pl-for", name: "Foreign Mix", remoteId: "PLfor", owned: false }),
          playlist({ id: "pl-own", name: "My Mix", remoteId: "PLown", owned: true }),
        ],
      }),
    );

    expect(labels(items)).toContain("Add to My Mix");
    expect(labels(items)).not.toContain("Liked Music");
    expect(labels(items)).not.toContain("Foreign Mix");
  });
});

describe("menuItemsFor playlist", () => {
  it("offers open, rename and delete", () => {
    const list = playlist();
    const items = menuItemsFor(
      { kind: "playlist", id: list.id, label: list.name, libraryId: list.libraryId, playlist: list },
      deps(),
    );

    expect(labels(items)).toEqual(["Open", "Rename", "Delete"]);
    expect(items.find((i) => i.label === "Delete")?.danger).toBe(true);
  });

  it("makes foreign playlists read-only with Remove from library", () => {
    const list = playlist({ remoteId: "PLforeign", owned: false });
    const items = menuItemsFor(
      { kind: "playlist", id: list.id, label: list.name, libraryId: list.libraryId, playlist: list },
      deps(),
    );

    expect(labels(items)).toEqual(["Open", "Remove from library"]);
    expect(items.find((i) => i.label === "Remove from library")?.danger).toBe(true);
  });

  it("only offers Open on Liked Music", () => {
    const list = playlist({ remoteId: "LM", owned: false });
    const items = menuItemsFor(
      { kind: "playlist", id: list.id, label: list.name, libraryId: list.libraryId, playlist: list },
      deps(),
    );

    expect(labels(items)).toEqual(["Open"]);
  });

  it("offers Watch folder toggle on folder-backed playlists in local libraries", () => {
    const toggled: boolean[] = [];
    const list = playlist({ sourcePath: "/music/rock", watched: true });
    const items = menuItemsFor(
      { kind: "playlist", id: list.id, label: list.name, libraryId: list.libraryId, playlist: list },
      deps({
        libraryKindFor: () => "local",
        togglePlaylistWatch: (_pl, next) => toggled.push(next),
      }),
    );

    const watch = items.find((i) => i.label === "Watch folder");
    expect(watch?.checked).toBe(true);
    watch?.run?.();
    expect(toggled).toEqual([false]);
  });

  it("offers Watch remote playlist toggle on YTM playlists imported into local libraries", () => {
    const list = playlist({ remoteId: "PLytm", watched: false });
    const items = menuItemsFor(
      { kind: "playlist", id: list.id, label: list.name, libraryId: list.libraryId, playlist: list },
      deps({ libraryKindFor: () => "local" }),
    );

    const watch = items.find((i) => i.label === "Watch remote playlist");
    expect(watch?.checked).toBe(false);
  });

  it("omits watch toggles on plain local playlists and KindYTM libraries", () => {
    const plain = playlist();
    const plainItems = menuItemsFor(
      { kind: "playlist", id: plain.id, label: plain.name, libraryId: plain.libraryId, playlist: plain },
      deps({ libraryKindFor: () => "local" }),
    );
    expect(labels(plainItems)).toEqual(["Open", "Rename", "Delete"]);

    const ytmList = playlist({ libraryId: "ytm:UC1", remoteId: "PLytm", watched: false });
    const ytmItems = menuItemsFor(
      { kind: "playlist", id: ytmList.id, label: ytmList.name, libraryId: ytmList.libraryId, playlist: ytmList },
      deps({ libraryKindFor: () => "ytm" }),
    );
    expect(labels(ytmItems)).toEqual(["Open", "Rename", "Delete"]);
  });
});

describe("menuItemsFor catalogue", () => {
  it("offers open, play and the two queue rows for an album", () => {
    const items = menuItemsFor(
      { kind: "catalogue", id: album.id, label: album.name, libraryId: album.libraryId, album },
      deps(),
    );

    expect(labels(items)).toEqual(["Open", "Play", "Add to next", "Add to queue"]);
  });
});

describe("menuItemsFor library", () => {
  it("offers open, edit, rescan and remove", () => {
    const items = menuItemsFor(
      { kind: "library", id: "local:test", label: "Test", libraryId: "local:test" },
      deps(),
    );

    expect(labels(items)).toEqual(["Open", "Edit", "Rescan files", "Remove from Molow"]);
  });

  it("uses Refresh library for ytm libraries", () => {
    const items = menuItemsFor(
      { kind: "library", id: "ytm:UC1", label: "YTM", libraryId: "ytm:UC1" },
      deps({ libraryKindFor: () => "ytm" }),
    );

    expect(labels(items)).toEqual(["Open", "Edit", "Refresh library", "Remove from Molow"]);
  });
});
