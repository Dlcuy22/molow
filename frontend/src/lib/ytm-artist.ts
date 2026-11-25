import { get, writable } from "svelte/store";
import { PlayerService } from "../../bindings/github.com/dlcuy22/molow/backend";
import type {
  YTMArtistAlbum,
  YTMArtistRef,
  YTMArtistTrack,
  YTMArtistVideo,
  YTMArtistViewData,
} from "../../bindings/github.com/dlcuy22/molow/backend/models";
import {
  activePlaylist,
  activePlaylistError,
  activePlaylistLoading,
  activePlaylistTracks,
  playRefs,
} from "./library";
import { FLAG_PLAYABLE, type Album, type Artist, type Playlist, type Track, type YtmArtist, type YtmVideo } from "./types";

export const activeYtmArtistOpen = writable(false);
export const activeYtmArtist = writable<YtmArtist | null>(null);
export const activeYtmArtistLoading = writable(false);
export const activeYtmArtistError = writable("");
export const ytmArtistStack = writable<YtmArtist[]>([]);
export const ytmArtistReturn = writable<YtmArtist | null>(null);

type YtmArtistTarget =
  | { kind: "id"; artistId: string }
  | { kind: "track"; ref: string; artistName: string };

let artistSeq = 0;
let lastTarget: YtmArtistTarget | null = null;

export function isYtmRef(ref: string): boolean {
  return ref.startsWith("ytm:");
}

function mapTrack(t: YTMArtistTrack): Track {
  return {
    ref: t.ref,
    libraryId: "ytm",
    nativeId: t.nativeId,
    title: t.title,
    artist: t.artist,
    album: t.album,
    durationMs: t.durationMs,
    coverId: "",
    imageUrl: t.imageUrl || undefined,
    liked: false,
    flags: t.ref !== "" ? FLAG_PLAYABLE : 0,
    trackNo: 0,
  };
}

function mapAlbum(a: YTMArtistAlbum): Album {
  return {
    id: a.id,
    libraryId: "ytm",
    name: a.name,
    artist: a.artist,
    coverId: "",
    imageUrl: a.imageUrl || undefined,
    songCount: 0,
    durationMs: 0,
    year: a.year,
  };
}

function mapVideo(v: YTMArtistVideo): YtmVideo {
  return {
    id: v.id,
    title: v.title,
    subtitle: v.subtitle || undefined,
    durationMs: v.durationMs || undefined,
    thumbnailUrl: v.thumbnailUrl || undefined,
    track: {
      ref: v.ref,
      libraryId: "ytm",
      nativeId: v.id,
      title: v.title,
      artist: v.subtitle,
      album: "",
      durationMs: v.durationMs,
      coverId: "",
      imageUrl: v.thumbnailUrl || undefined,
      liked: false,
      flags: v.ref !== "" ? FLAG_PLAYABLE : 0,
      trackNo: 0,
    },
  };
}

function mapRelatedArtist(a: YTMArtistRef): Artist {
  return {
    id: a.id,
    libraryId: "ytm",
    name: a.name,
    coverId: "",
    imageUrl: a.imageUrl || undefined,
    subtitle: a.subtitle || undefined,
    songCount: 0,
    albumCount: 0,
  };
}

function mapArtistData(data: YTMArtistViewData): YtmArtist {
  return {
    id: data.id,
    name: data.name,
    description: data.description || undefined,
    bannerUrl: data.bannerUrl || undefined,
    subscriberText: data.subscriberText || undefined,
    topSongs: (data.topSongs ?? []).map(mapTrack),
    albums: (data.albums ?? []).map(mapAlbum),
    singles: (data.singles ?? []).map(mapAlbum),
    videos: (data.videos ?? []).map(mapVideo),
    relatedArtists: (data.relatedArtists ?? []).map(mapRelatedArtist),
  };
}

function errorMessage(err: unknown, fallback: string): string {
  if (err instanceof Error && err.message.trim() !== "") {
    return err.message;
  }
  if (typeof err === "string" && err.trim() !== "") {
    return err;
  }

  return fallback;
}

async function loadTarget(target: YtmArtistTarget): Promise<void> {
  lastTarget = target;
  const seq = ++artistSeq;
  activeYtmArtistOpen.set(true);
  activeYtmArtist.set(null);
  activeYtmArtistError.set("");
  activeYtmArtistLoading.set(true);

  try {
    const raw =
      target.kind === "id"
        ? await PlayerService.YTMArtist(target.artistId)
        : await PlayerService.YTMArtistForTrack(target.ref, target.artistName);
    if (seq !== artistSeq) {
      return;
    }
    activeYtmArtist.set(mapArtistData(raw));
  } catch (err) {
    if (seq !== artistSeq) {
      return;
    }
    activeYtmArtistError.set(errorMessage(err, "Could not load this artist"));
  } finally {
    if (seq === artistSeq) {
      activeYtmArtistLoading.set(false);
    }
  }
}

export function openYtmArtistById(artistId: string): Promise<void> {
  if (artistId.trim() === "") {
    return Promise.resolve();
  }
  const cur = get(activeYtmArtist);
  if (get(activeYtmArtistOpen) && cur && cur.id !== artistId) {
    ytmArtistStack.update((s) => [...s, cur]);
  }
  ytmArtistReturn.set(null);

  return loadTarget({ kind: "id", artistId });
}

export function openYtmArtistForTrack(
  ref: string,
  artistName = "",
  artistId = "",
): Promise<void> {
  if (!isYtmRef(ref)) {
    return Promise.resolve();
  }
  ytmArtistStack.set([]);
  ytmArtistReturn.set(null);
  if (artistId.trim() !== "") {
    return loadTarget({ kind: "id", artistId });
  }

  return loadTarget({ kind: "track", ref, artistName });
}

export function backFromYtmArtist(): void {
  const stack = get(ytmArtistStack);
  if (stack.length > 0) {
    const prev = stack[stack.length - 1];
    artistSeq++;
    ytmArtistStack.set(stack.slice(0, -1));
    activeYtmArtist.set(prev);
    activeYtmArtistError.set("");
    activeYtmArtistLoading.set(false);
    activeYtmArtistOpen.set(true);

    return;
  }
  closeYtmArtist();
}

export function closeYtmArtist(): void {
  artistSeq++;
  activeYtmArtistOpen.set(false);
  activeYtmArtist.set(null);
  activeYtmArtistError.set("");
  activeYtmArtistLoading.set(false);
  ytmArtistStack.set([]);
  ytmArtistReturn.set(null);
}

export function restoreYtmArtistFromAlbum(): boolean {
  const prev = get(ytmArtistReturn);
  if (!prev) {
    return false;
  }
  artistSeq++;
  ytmArtistReturn.set(null);
  activeYtmArtist.set(prev);
  activeYtmArtistError.set("");
  activeYtmArtistLoading.set(false);
  activeYtmArtistOpen.set(true);

  return true;
}

export function retryYtmArtist(): Promise<void> {
  if (!lastTarget) {
    return Promise.resolve();
  }

  return loadTarget(lastTarget);
}

export async function playYtmAlbum(album: Album): Promise<void> {
  if (album.id === "") {
    return;
  }
  try {
    const rows = (await PlayerService.YTMPlaylistTracks(album.id)) ?? [];
    const refs = rows.map((r) => r.ref).filter((r) => r !== "");
    if (refs.length > 0) {
      await playRefs(refs);
    }
  } catch {
    // Playback error surfaces through the player snapshot.
  }
}

export async function openYtmAlbumView(album: Album): Promise<void> {
  if (album.id === "") {
    return;
  }
  const curArtist = get(activeYtmArtist);
  if (curArtist) {
    ytmArtistReturn.set(curArtist);
  }
  artistSeq++;
  activeYtmArtistOpen.set(false);

  const meta: Playlist = {
    id: album.id,
    libraryId: "ytm",
    name: album.name,
    description: "",
    creator: album.artist,
    trackCount: album.songCount,
    coverId: album.coverId,
    album,
  };

  activePlaylist.set(meta);
  activePlaylistTracks.set([]);
  activePlaylistError.set("");
  activePlaylistLoading.set(true);

  try {
    const rows = (await PlayerService.YTMPlaylistTracks(album.id)) ?? [];
    const tracks = rows.map(mapTrack);
    activePlaylistTracks.set(tracks);
    activePlaylist.update((cur) =>
      cur && cur.id === album.id ? { ...cur, trackCount: tracks.length } : cur,
    );
  } catch (err) {
    activePlaylistError.set(errorMessage(err, "Could not load this album"));
  } finally {
    activePlaylistLoading.set(false);
  }
}

export function shuffleYtmArtistTracks(tracks: Track[]): Promise<void> {
  const refs = tracks.map((t) => t.ref).filter((r) => r !== "");
  if (refs.length === 0) {
    return Promise.resolve();
  }
  const shuffled = [...refs];
  for (let i = shuffled.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [shuffled[i], shuffled[j]] = [shuffled[j], shuffled[i]];
  }

  return playRefs(shuffled);
}
