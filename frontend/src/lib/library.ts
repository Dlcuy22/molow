// Library state: the one place the pages read libraries, feeds and search
// from. It mirrors the Go contract and never reaches across the bridge from a
// component.

import { get, writable } from "svelte/store";
import { Events } from "@wailsio/runtime";
import { PlayerService } from "../../bindings/github.com/dlcuy22/molow/backend";
import type {
  Album as GenAlbum,
  Artist as GenArtist,
  Library as GenLibrary,
  Page as GenPage,
  Playlist as GenPlaylist,
  Shelf as GenShelf,
  Track as GenTrack,
} from "../../bindings/github.com/dlcuy22/molow/internal/library/models";
import type { Album, Artist, LibraryInfo, Page, Shelf, Track } from "./types";

export const libraries = writable<LibraryInfo[]>([]);
export const libraryId = writable<string>("");
export const homePage = writable<Page>({ shelves: [], cursor: "" });
export const libraryPage = writable<Page>({ shelves: [], cursor: "" });
export const searchPage = writable<Page>({ shelves: [], cursor: "" });
export const libraryLoading = writable(false);
export const playlistRows = writable<{ id: string; name: string; coverId?: string }[]>([]);
export const libraryProgress = writable<{ done: number; total: number; state: string } | null>(
  null,
);

function toTrack(t: GenTrack): Track {
  return {
    ref: t.ref,
    libraryId: t.libraryId,
    nativeId: t.nativeId,
    title: t.title,
    artist: t.artist,
    album: t.album,
    durationMs: t.durationMs,
    coverId: t.coverId ?? "",
    liked: t.liked,
    flags: Number(t.flags ?? 0),
    trackNo: t.trackNo ?? 0,
  };
}

function toAlbum(a: GenAlbum): Album {
  return {
    id: a.id,
    libraryId: a.libraryId,
    name: a.name,
    artist: a.artist,
    coverId: a.coverId ?? "",
    songCount: a.songCount,
    durationMs: a.durationMs,
    year: a.year ?? 0,
  };
}

function toArtist(a: GenArtist): Artist {
  return {
    id: a.id,
    libraryId: a.libraryId,
    name: a.name,
    coverId: a.coverId ?? "",
    songCount: a.songCount,
    albumCount: a.albumCount,
  };
}

function toShelf(s: GenShelf): Shelf {
  return {
    id: s.id,
    title: s.title,
    subtitle: s.subtitle ?? "",
    layout: s.layout === "list" ? "list" : "grid",
    kind: (s.kind || "tracks") as Shelf["kind"],
    items: (s.items ?? []).map(toTrack),
    albums: (s.albums ?? []).map(toAlbum),
    artists: (s.artists ?? []).map(toArtist),
    playlists: (s.playlists ?? []).map((p: GenPlaylist) => ({
      id: p.id,
      libraryId: p.libraryId,
      name: p.name,
      description: p.description ?? "",
      trackCount: p.trackCount,
    })),
    cursor: s.cursor ?? "",
    total: s.total ?? 0,
  };
}

function toPage(p: GenPage): Page {
  return { shelves: (p.shelves ?? []).map(toShelf), cursor: p.cursor ?? "" };
}

function toInfo(l: GenLibrary): LibraryInfo {
  return {
    id: l.id,
    kind: String(l.kind),
    name: l.name,
    root: l.root ?? "",
    status: String(l.status),
    message: l.message ?? "",
    capabilities: (l.capabilities ?? []).map(String),
    trackCount: l.trackCount,
  };
}

/** refreshLibraries reloads the registry and keeps the selection valid. */export async function refreshLibraries(): Promise<void> {
  try {
    const rows = (await PlayerService.Libraries()) ?? [];
    const mapped = rows.map(toInfo);
    libraries.set(mapped);
    const current = get(libraryId);
    if (current === "" || !mapped.some((l) => l.id === current)) {
      libraryId.set(mapped[0]?.id ?? "");
    }
  } catch {
    libraries.set([]);
  }
}

/** selectLibrary switches the open library and reloads both pages. */
export async function selectLibrary(id: string): Promise<void> {
  if (get(libraryId) === id) {
    return;
  }
  libraryId.set(id);
  await Promise.all([loadHome(), loadLibrary()]);
}

export async function loadHome(): Promise<void> {
  const id = get(libraryId);
  if (id === "") {
    homePage.set({ shelves: [], cursor: "" });

    return;
  }
  libraryLoading.set(true);
  try {
    homePage.set(toPage(await PlayerService.LibraryFeed(id, "home", "", 9)));
  } catch {
    homePage.set({ shelves: [], cursor: "" });
  } finally {
    libraryLoading.set(false);
  }
}

export async function loadLibrary(tab = "all"): Promise<void> {
  const id = get(libraryId);
  if (id === "") {
    libraryPage.set({ shelves: [], cursor: "" });
    playlistRows.set([]);

    return;
  }
  libraryLoading.set(true);
  try {
    const [page, lists] = await Promise.all([
      PlayerService.LibraryFeed(id, tab, "", 60),
      PlayerService.Playlists(id).catch(() => []),
    ]);
    libraryPage.set(toPage(page));
    playlistRows.set(
      (lists ?? []).map((p) => ({ id: p.id, name: p.name, coverId: undefined })),
    );
  } catch {
    libraryPage.set({ shelves: [], cursor: "" });
    playlistRows.set([]);
  } finally {
    libraryLoading.set(false);
  }
}

export async function searchLibrary(query: string): Promise<void> {
  const id = get(libraryId);
  if (id === "" || query.trim() === "") {
    searchPage.set({ shelves: [], cursor: "" });

    return;
  }
  try {
    searchPage.set(toPage(await PlayerService.LibrarySearch(id, query.trim(), 40)));
  } catch {
    searchPage.set({ shelves: [], cursor: "" });
  }
}

/** addLibrary opens the folder picker and registers the choice. */
export async function addLibrary(): Promise<LibraryInfo | null> {
  try {
    const path = await PlayerService.PickLibraryFolder();
    if (!path) {
      return null;
    }
    const info = toInfo(await PlayerService.AddLocalLibrary(path));
    await refreshLibraries();
    libraryId.set(info.id);
    await Promise.all([loadHome(), loadLibrary()]);

    return info;
  } catch {
    return null;
  }
}

export async function removeLibrary(id: string): Promise<void> {
  try {
    await PlayerService.RemoveLibrary(id);
  } finally {
    await refreshLibraries();
    await Promise.all([loadHome(), loadLibrary()]);
  }
}

export async function rescanLibrary(id = get(libraryId)): Promise<void> {
  if (id === "") {
    return;
  }
  try {
    await PlayerService.RescanLibrary(id);
  } catch {
    // The progress event reports the outcome; a rejection here only means the
    // scan could not be started.
  }
}

/** playTrack starts one catalogue row. */
export async function playTrack(track: Track): Promise<void> {
  if (track.ref === "") {
    return;
  }
  try {
    await PlayerService.PlayRefs([track.ref]);
  } catch {
    // Surfaced by the snapshot error line.
  }
}

/** playAlbum starts an album in track order. */
export async function playAlbum(album: Album): Promise<void> {
  const id = get(libraryId);
  if (id === "") {
    return;
  }
  try {
    const tracks = (await PlayerService.LibraryAlbumTracks(id, album.name, album.artist)) ?? [];
    const refs = tracks.map((t) => t.ref).filter((r) => r !== "");
    if (refs.length > 0) {
      await PlayerService.PlayRefs(refs);
    }
  } catch {
    // Surfaced by the snapshot error line.
  }
}

export async function toggleLike(track: Track): Promise<void> {
  const id = get(libraryId);
  if (id === "" || track.ref === "") {
    return;
  }
  try {
    await PlayerService.SetLiked(id, track.ref, !track.liked);
    await Promise.all([loadHome(), loadLibrary()]);
  } catch {
    // The row keeps its old state; a failed write is the honest result.
  }
}

/** likeRef toggles the like on the playing track. */
export async function likeRef(path: string): Promise<boolean> {
  if (path === "") {
    return false;
  }
  try {
    return await PlayerService.ToggleLike(path);
  } catch {
    // A remote ref or a file outside the catalogue has no like; the button
    // stays as it was.
    return false;
  }
}

/** isLiked reads the like state for the dock's heart. */
export async function isLiked(path: string): Promise<boolean> {
  if (path === "") {
    return false;
  }
  try {
    return await PlayerService.Liked(path);
  } catch {
    return false;
  }
}

/** connectLibrary loads the registry, the first feed and the scan event. */
export function connectLibrary(): () => void {
  const off = Events.On("molo:library", (ev) => {
    const p = ev.data as { libraryId: string; done: number; total: number; state: string };
    if (p.libraryId !== get(libraryId)) {
      return;
    }
    libraryProgress.set({ done: p.done, total: p.total, state: p.state });
    if (p.state === "ready") {
      libraryProgress.set(null);
      void Promise.all([loadHome(), loadLibrary(), refreshLibraries()]);
    }
  });

  void refreshLibraries().then(() => Promise.all([loadHome(), loadLibrary()]));

  return off;
}

// ---- artwork accent and lyrics -------------------------------------------------

/** loadAccents returns the cover's ranked accents, light end first. */
export async function loadAccents(coverId: string): Promise<string[]> {
  if (coverId === "") {
    return [];
  }
  try {
    const cs = (await PlayerService.CoverAccents(coverId)) ?? [];
    if (cs.length === 0) {
      return [];
    }
    const luma = (c: { r: number; g: number; b: number }) =>
      (0.2126 * c.r + 0.7152 * c.g + 0.0722 * c.b) / 255;
    const hex = (c: { r: number; g: number; b: number }) =>
      "#" +
      [c.r, c.g, c.b]
        .map((v) => Math.round(v).toString(16).padStart(2, "0"))
        .join("");
    const dark = cs[0];
    let light = dark;
    for (const c of cs.slice(1)) {
      if (Math.abs(luma(c) - luma(dark)) >= 0.15) {
        light = c;
        break;
      }
    }
    const ordered = luma(light) >= luma(dark) ? [light, dark] : [dark, light];
    return ordered.map(hex);
  } catch {
    return [];
  }
}

/**
 * loadLyrics reads the lyric sheet of a track: the sidecar file for a local
 * track, the catalogue sheet for a streamed one.
 *
 * An untimed sheet is rejected as no lyrics. The player follows a line by its
 * timestamp, so every line of a plain sheet would sit at the same position and
 * the column would look stuck rather than read as text.
 */
export async function loadLyrics(path: string) {
  if (path === "") {
    return null;
  }
  try {
    const doc = await PlayerService.Lyrics(path);
    return doc?.synced ? doc : null;
  } catch {
    return null;
  }
}
