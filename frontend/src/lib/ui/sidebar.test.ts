import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import type { Playlist } from "../types";
import Sidebar, { type LibraryCard } from "./Sidebar.svelte";

function makeLibrary(id: string, name: string, trackCount = 12): LibraryCard {
  return {
    id,
    kind: "local",
    name,
    coverId: "",
    trackCount,
  };
}

function makePlaylist(id: string, name: string, trackCount = 5): Playlist {
  return {
    id,
    libraryId: "lib-1",
    name,
    description: "",
    trackCount,
    coverId: "",
    coverIds: [],
  };
}

describe("Sidebar", () => {
  it("renders the top library switcher with a circular avatar and playlists below", () => {
    const { body } = render(Sidebar, {
      props: {
        active: "home",
        libraries: [makeLibrary("lib-1", "Main Archive", 42), makeLibrary("lib-2", "Lossless", 8)],
        activeLibrary: "lib-1",
        playlists: [makePlaylist("pl-1", "Late Night"), makePlaylist("pl-2", "Coding Mix")],
      },
    });

    expect(body).toContain("Main Archive");
    expect(body).toContain("42 songs");
    expect(body).toContain("rounded-full");
    expect(body).toContain('aria-haspopup="menu"');
    expect(body).toContain("Playlists");
    expect(body).toContain("Late Night");
    expect(body).toContain("Coding Mix");
    expect(body).toContain("rounded-[4px]");
  });

  it("moves active page highlight from top nav to the open playlist", () => {
    const { body } = render(Sidebar, {
      props: {
        active: "library",
        libraries: [makeLibrary("lib-1", "Main Archive", 10)],
        activeLibrary: "lib-1",
        playlists: [makePlaylist("pl-1", "Late Night")],
        activePlaylistId: "pl-1",
      },
    });

    const currentMatches = body.match(/aria-current="page"/g) ?? [];
    expect(currentMatches).toHaveLength(1);
    expect(body).toContain('title="Late Night" aria-current="page"');
  });

  it("renders create library card when no libraries exist", () => {
    const { body } = render(Sidebar, {
      props: {
        libraries: [],
        playlists: [],
      },
    });

    expect(body).toContain("Create library");
    expect(body).not.toContain('aria-haspopup="menu"');
    expect(body).not.toContain("No playlists yet.");
  });

  it("renders honest loading and empty states for playlists", () => {
    const loading = render(Sidebar, {
      props: {
        libraries: [makeLibrary("lib-1", "Main Archive", 0)],
        activeLibrary: "lib-1",
        playlists: [],
        playlistsLoading: true,
      },
    });
    expect(loading.body).toContain("Loading playlists...");

    const empty = render(Sidebar, {
      props: {
        libraries: [makeLibrary("lib-1", "Main Archive", 0)],
        activeLibrary: "lib-1",
        playlists: [],
        playlistsLoading: false,
      },
    });
    expect(empty.body).toContain("No playlists yet.");
    expect(empty.body).toContain("Empty library");
  });

  it("collapses labels while preserving accessible titles and switcher button", () => {
    const { body } = render(Sidebar, {
      props: {
        width: 56,
        libraries: [makeLibrary("lib-1", "Main Archive", 42)],
        activeLibrary: "lib-1",
        playlists: [makePlaylist("pl-1", "Late Night")],
      },
    });

    expect(body).toContain('aria-label="Switch library (Main Archive)"');
    expect(body).toContain('title="Late Night"');
    expect(body).not.toContain("42 songs");
    expect(body).not.toContain(">Playlists<");
  });
});
