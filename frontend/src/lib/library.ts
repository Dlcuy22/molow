// Library state: the one place the pages read libraries, feeds and search
// from. It mirrors the Go contract and never reaches across the bridge from a
// component.

import { get, writable } from "svelte/store";
import { Events } from "@wailsio/runtime";
import CheckCircle from "phosphor-svelte/lib/CheckCircle";
import WarningCircle from "phosphor-svelte/lib/WarningCircle";
import { PlayerService } from "../../bindings/github.com/dlcuy22/molow/backend";
import type { YTMResult } from "../../bindings/github.com/dlcuy22/molow/backend/models";
import type {
  Album as GenAlbum,
  Artist as GenArtist,
  Library as GenLibrary,
  Page as GenPage,
  Playlist as GenPlaylist,
  Shelf as GenShelf,
  Track as GenTrack,
} from "../../bindings/github.com/dlcuy22/molow/internal/library/models";
import type { Album, Artist, LibraryInfo, Page, Playlist, Shelf, Track } from "./types";
import { showToast } from "./toast";

export const libraries = writable<LibraryInfo[]>([]);
export const libraryId = writable<string>("");
export const homePage = writable<Page>({ shelves: [], cursor: "" });
export const libraryPage = writable<Page>({ shelves: [], cursor: "" });
export const searchPage = writable<Page>({ shelves: [], cursor: "" });
export const libraryLoading = writable(false);
export const libraryPlaylists = writable<Playlist[]>([]);
export const libraryProgress = writable<{ done: number; total: number; state: string } | null>(
  null,
);
export const activePlaylist = writable<Playlist | null>(null);
export const activePlaylistTracks = writable<Track[]>([]);
export const activePlaylistLoading = writable(false);
export const activePlaylistError = writable("");

let currentLibraryTab = "all";
let playlistSeq = 0;

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
      coverId: p.coverId ?? "",
      customCover: p.customCover ?? false,
      coverIds: p.coverIds ?? [],
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
    description: l.description ?? "",
    root: l.root ?? "",
    roots: l.roots ?? [],
    coverId: l.coverId ?? "",
    customCover: l.customCover ?? false,
    logoCovers: l.logoCovers ?? [],
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
  closePlaylist();
  libraryId.set(id);
  await Promise.all([loadHome(), loadLibrary(currentLibraryTab)]);
}

/** closePlaylist exits the dedicated playlist view without disturbing the library grid. */
export function closePlaylist(): void {
  playlistSeq++;
  activePlaylist.set(null);
  activePlaylistTracks.set([]);
  activePlaylistLoading.set(false);
  activePlaylistError.set("");
}

/** openPlaylist loads a playlist's metadata and all of its tracks into the dedicated view. */
export async function openPlaylist(target: Playlist | string): Promise<void> {
  const id = typeof target === "string" ? target : target.id;
  const libId = typeof target === "string" ? get(libraryId) : target.libraryId || get(libraryId);
  if (libId === "" || id === "") {
    return;
  }

  let meta: Playlist | undefined = typeof target === "string" ? undefined : target;
  if (meta === undefined) {
    const shelf = get(libraryPage).shelves.find((s) => s.kind === "playlists");
    meta = shelf?.playlists.find((p) => p.id === id);
  }
  if (meta === undefined) {
    try {
      const lists = (await PlayerService.Playlists(libId)) ?? [];
      const found = lists.find((p) => p.id === id);
      if (found) {
        meta = {
          id: found.id,
          libraryId: found.libraryId,
          name: found.name,
          description: found.description ?? "",
          trackCount: found.trackCount,
          coverId: found.coverId ?? "",
          customCover: found.customCover ?? false,
          coverIds: found.coverIds ?? [],
        };
      }
    } catch {
      // Fall through to the minimal placeholder below; the feed call will report any error.
    }
  }
  if (meta === undefined) {
    meta = { id, libraryId: libId, name: "Playlist", description: "", trackCount: 0 };
  }

  const seq = ++playlistSeq;
  activePlaylist.set(meta);
  activePlaylistTracks.set([]);
  activePlaylistError.set("");
  activePlaylistLoading.set(true);

  try {
    const collected: Track[] = [];
    let cursor = "";
    // Paginate until the backend clears the cursor so large playlists load completely.
    for (;;) {
      const page = toPage(await PlayerService.LibraryFeed(libId, "playlist:" + id, cursor, 200));
      if (seq !== playlistSeq) {
        return;
      }
      const shelf = page.shelves[0];
      if (!shelf || shelf.items.length === 0) {
        break;
      }
      collected.push(...shelf.items);
      if (!shelf.cursor) {
        break;
      }
      cursor = shelf.cursor;
    }
    if (seq !== playlistSeq) {
      return;
    }
    activePlaylistTracks.set(collected);
    activePlaylist.update((cur) =>
      cur && cur.id === id ? { ...cur, trackCount: collected.length } : cur,
    );
  } catch (err) {
    if (seq !== playlistSeq) {
      return;
    }
    activePlaylistError.set(libraryErrorText(err, "Could not load this playlist"));
  } finally {
    if (seq === playlistSeq) {
      activePlaylistLoading.set(false);
    }
  }
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

export async function loadLibrary(tab = currentLibraryTab): Promise<void> {
  currentLibraryTab = tab;
  const id = get(libraryId);
  if (id === "") {
    libraryPage.set({ shelves: [], cursor: "" });
    libraryPlaylists.set([]);

    return;
  }
  libraryLoading.set(true);
  try {
    const [page, lists] = await Promise.all([
      PlayerService.LibraryFeed(id, tab, "", 60),
      PlayerService.Playlists(id).catch(() => []),
    ]);
    libraryPage.set(toPage(page));
    libraryPlaylists.set(
      (lists ?? []).map((p) => ({
        id: p.id,
        libraryId: p.libraryId,
        name: p.name,
        description: p.description ?? "",
        trackCount: p.trackCount,
        coverId: p.coverId ?? "",
        customCover: p.customCover ?? false,
        coverIds: p.coverIds ?? [],
      })),
    );
  } catch {
    libraryPage.set({ shelves: [], cursor: "" });
    libraryPlaylists.set([]);
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

/** libraryErrorText unwraps the message a library binding rejects with. The
 *  binding rejects with a plain string, so both shapes are read, and the
 *  "library:" / "ytm:" prefix is the package's, not the user's. */
function libraryErrorText(err: unknown, fallback = "Could not add that folder"): string {
  const raw =
    typeof err === "string" ? err : err instanceof Error ? err.message : "";
  const text = raw.replace(/^(?:library|ytm):\s*/, "").trim();
  if (text === "") {
    return fallback;
  }

  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** addLibrary opens the folder picker and imports the chosen folder into the
 *  active library as a watched root and folder playlist. */
export async function addLibrary(): Promise<LibraryInfo | null> {
  try {
    let target = get(libraryId);
    if (target === "") {
      await refreshLibraries();
      target = get(libraryId);
      if (target === "") {
        showToast({
          key: "library-import",
          kind: "message",
          label: "Create a library before importing.",
          icon: WarningCircle,
        });

        return null;
      }
    }
    const path = await PlayerService.PickLibraryFolder();
    if (!path) {
      return null;
    }
    const info = toInfo(await PlayerService.ImportFolder(target, path));
    await refreshLibraries();
    libraryId.set(info.id);
    await Promise.all([loadHome(), loadLibrary()]);

    return info;
  } catch (err) {
    showToast({
      key: "library-import",
      kind: "message",
      label: libraryErrorText(err),
      icon: WarningCircle,
    });

    return null;
  }
}

/** createLibrary creates a new virtual local library and switches to it. */
export async function createLibrary(
  name: string,
  description: string,
): Promise<{ info: LibraryInfo | null; error: string }> {
  try {
    const info = toInfo(await PlayerService.CreateLibrary(name, description));
    await refreshLibraries();
    closePlaylist();
    libraryId.set(info.id);
    await Promise.all([loadHome(), loadLibrary("all")]);

    return { info, error: "" };
  } catch (err) {
    return { info: null, error: libraryErrorText(err, "Could not create that library") };
  }
}

/** updateLibrary saves a new name and optional description for a library. */
export async function updateLibrary(
  id: string,
  name: string,
  description: string,
): Promise<{ info: LibraryInfo | null; error: string }> {
  try {
    const info = toInfo(await PlayerService.UpdateLibrary(id, name, description));
    await refreshLibraries();

    return { info, error: "" };
  } catch (err) {
    return { info: null, error: libraryErrorText(err, "Could not update that library") };
  }
}

/** importYTM copies a YouTube Music hit into the open library: one track for a
 *  song, its tracks for an album, a playlist row with its entries for a
 *  playlist. The feeds reload before the palette closes, so the rows are on
 *  screen by the time it is gone. A failure keeps the palette open and says
 *  why; the hit is still there to try again. */
export async function importYTM(hit: YTMResult): Promise<boolean> {
  const id = get(libraryId);
  if (id === "") {
    showToast({
      key: "library-import",
      kind: "message",
      label: "Select a library before importing.",
      icon: WarningCircle,
    });

    return false;
  }
  try {
    const res = await PlayerService.ImportYTM(id, hit);
    const name = get(libraries).find((l) => l.id === id)?.name ?? "the library";
    const songs = res.tracks === 1 ? "1 song" : `${res.tracks} songs`;
    showToast({
      key: "library-import",
      kind: "message",
      label: res.playlistId ? `${res.name}: ${songs}` : `${songs} added to ${name}`,
      icon: CheckCircle,
    });
    await Promise.all([refreshLibraries(), loadHome(), loadLibrary()]);

    return true;
  } catch (err) {
    showToast({
      key: "library-import",
      kind: "message",
      label: libraryErrorText(err, "Could not import that track"),
      icon: WarningCircle,
    });

    return false;
  }
}

export async function removeLibrary(id: string): Promise<void> {
  try {
    await PlayerService.RemoveLibrary(id);
  } finally {
    if (get(libraryId) === id) {
      closePlaylist();
    }
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

export async function albumRefs(album: Album): Promise<string[]> {
  const id = get(libraryId);
  if (id === "") {
    return [];
  }
  try {
    const tracks = (await PlayerService.LibraryAlbumTracks(id, album.name, album.artist)) ?? [];

    return tracks.map((t) => t.ref).filter((r) => r !== "");
  } catch {
    // Surfaced by the snapshot error line.
    return [];
  }
}

export async function playRefs(refs: string[]): Promise<void> {
  if (refs.length === 0) {
    return;
  }
  try {
    await PlayerService.PlayRefs(refs);
  } catch {
    // Surfaced by the snapshot error line.
  }
}

/** playAlbum starts an album in track order. */
export async function playAlbum(album: Album): Promise<void> {
  const refs = await albumRefs(album);
  if (refs.length === 0) {
    return;
  }
  try {
    await PlayerService.PlayRefs(refs);
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

export async function queueNext(refs: string[]): Promise<void> {
  const usable = refs.filter((r) => r !== "");
  if (usable.length === 0) {
    return;
  }
  try {
    await PlayerService.QueueRefsNext(usable);
  } catch {
    // Surfaced by the snapshot error line.
  }
}

export async function queueLast(refs: string[]): Promise<void> {
  const usable = refs.filter((r) => r !== "");
  if (usable.length === 0) {
    return;
  }
  try {
    await PlayerService.QueueRefsLast(usable);
  } catch {
    // Surfaced by the snapshot error line.
  }
}

/** Writes the explicit target state instead of toggling against stale store state. */
export async function setLiked(track: Track, liked: boolean): Promise<boolean> {
  if (track.ref === "") {
    return false;
  }
  const previous = track.liked;
  activePlaylistTracks.update((rows) =>
    rows.map((row) => (row.ref === track.ref ? { ...row, liked } : row)),
  );
  try {
    await PlayerService.SetLiked(track.libraryId, track.ref, liked);
    await Promise.all([loadHome(), loadLibrary()]);

    return true;
  } catch {
    activePlaylistTracks.update((rows) =>
      rows.map((row) => (row.ref === track.ref ? { ...row, liked: previous } : row)),
    );

    return false;
  }
}

export async function addToPlaylist(playlistID: string, refs: string[]): Promise<void> {
  const id = get(libraryId);
  if (id === "" || refs.length === 0) {
    return;
  }
  try {
    await PlayerService.AddToPlaylist(id, playlistID, refs);
    await loadLibrary();
    const open = get(activePlaylist);
    if (open && open.id === playlistID) {
      await openPlaylist(open);
    }
  } catch {
    // Leave the playlist unchanged on a failed write.
  }
}

export async function renamePlaylist(playlistID: string, name: string): Promise<void> {
  const id = get(libraryId);
  const trimmed = name.trim();
  if (id === "" || trimmed === "") {
    return;
  }
  try {
    await PlayerService.RenamePlaylist(id, playlistID, trimmed);
    activePlaylist.update((cur) =>
      cur && cur.id === playlistID ? { ...cur, name: trimmed } : cur,
    );
    await Promise.all([loadHome(), loadLibrary()]);
  } catch (err) {
    showToast({
      key: "playlist-rename",
      kind: "message",
      label: libraryErrorText(err, "Could not rename that playlist"),
      icon: WarningCircle,
    });
  }
}

export async function deletePlaylist(playlistID: string): Promise<void> {
  const id = get(libraryId);
  if (id === "") {
    return;
  }
  try {
    await PlayerService.DeletePlaylist(id, playlistID);
    if (get(activePlaylist)?.id === playlistID) {
      closePlaylist();
    }
    await Promise.all([loadHome(), loadLibrary()]);
  } catch (err) {
    showToast({
      key: "playlist-delete",
      kind: "message",
      label: libraryErrorText(err, "Could not delete that playlist"),
      icon: WarningCircle,
    });
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

const ACCENT_CACHE_MAX = 128;
const accentCache = new Map<string, string[]>();
const accentPending = new Map<string, Promise<string[]>>();

/** cachedAccents returns previously resolved stops for coverId without an IPC call. */
export function cachedAccents(coverId: string): string[] | undefined {
  return coverId === "" ? undefined : accentCache.get(coverId);
}

/** loadAccents returns the cover's ranked accents, cached by coverId hash. */
export async function loadAccents(coverId: string): Promise<string[]> {
  if (coverId === "") {
    return [];
  }
  const hit = accentCache.get(coverId);
  if (hit) {
    return hit;
  }
  const inflight = accentPending.get(coverId);
  if (inflight) {
    return inflight;
  }

  const p = (async () => {
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
      const stops = ordered.map(hex);
      if (accentCache.size >= ACCENT_CACHE_MAX) {
        const oldest = accentCache.keys().next().value;
        if (oldest !== undefined) {
          accentCache.delete(oldest);
        }
      }
      accentCache.set(coverId, stops);
      return stops;
    } catch {
      return [];
    } finally {
      accentPending.delete(coverId);
    }
  })();

  accentPending.set(coverId, p);
  return p;
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
