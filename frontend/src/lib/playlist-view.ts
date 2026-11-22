import type { Track } from "./types";

export type PlaylistSort = "order" | "title" | "artist" | "duration" | "liked";

export interface PlaylistSortOption {
  id: PlaylistSort;
  label: string;
}

export interface PlaylistPlayback {
  playlistId: string;
  refs: readonly string[];
}

function sameRefMultiset(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) {
    return false;
  }
  const counts = new Map<string, number>();
  for (const ref of a) {
    counts.set(ref, (counts.get(ref) ?? 0) + 1);
  }
  for (const ref of b) {
    const n = counts.get(ref) ?? 0;
    if (n === 0) {
      return false;
    }
    if (n === 1) {
      counts.delete(ref);
    } else {
      counts.set(ref, n - 1);
    }
  }

  return counts.size === 0;
}

/**
 * isPlaylistQueueActive reports whether the live player queue still matches
 * this playlist's unmodified playback session (surviving shuffles and reorders,
 * and returning false as soon as any track is added to or removed from the queue).
 */
export function isPlaylistQueueActive(
  playlistId: string,
  playlistTracks: readonly Track[],
  queuePaths: readonly string[],
  playbackState: string,
  launched: PlaylistPlayback | null,
): boolean {
  if (
    playlistId === "" ||
    playlistTracks.length === 0 ||
    queuePaths.length === 0 ||
    (playbackState !== "playing" && playbackState !== "paused")
  ) {
    return false;
  }
  if (launched) {
    if (launched.playlistId !== playlistId) {
      return false;
    }

    return sameRefMultiset(queuePaths, launched.refs);
  }
  const refs = playlistTracks.map((t) => t.ref).filter((r) => r !== "");
  if (refs.length === 0) {
    return false;
  }

  return sameRefMultiset(queuePaths, refs);
}

export const PLAYLIST_SORTS: PlaylistSortOption[] = [
  { id: "order", label: "Playlist order" },
  { id: "title", label: "Title A-Z" },
  { id: "artist", label: "Artist A-Z" },
  { id: "duration", label: "Duration" },
  { id: "liked", label: "Liked first" },
];

/** sortPlaylistTracks returns a sorted copy while preserving playlist order as a tie-breaker. */
export function sortPlaylistTracks(tracks: Track[], sort: PlaylistSort): Track[] {
  if (sort === "order") {
    return tracks;
  }

  return tracks
    .map((track, index) => ({ track, index }))
    .sort((a, b) => {
      switch (sort) {
        case "title":
          return a.track.title.localeCompare(b.track.title) || a.index - b.index;
        case "artist":
          return (
            a.track.artist.localeCompare(b.track.artist) ||
            a.track.title.localeCompare(b.track.title) ||
            a.index - b.index
          );
        case "duration":
          return a.track.durationMs - b.track.durationMs || a.index - b.index;
        case "liked":
          if (a.track.liked !== b.track.liked) {
            return a.track.liked ? -1 : 1;
          }
          return a.index - b.index;
      }
    })
    .map((entry) => entry.track);
}

/** playlistCovers collects up to four distinct non-empty cover ids in track order. */
export function playlistCovers(tracks: Track[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const track of tracks) {
    if (track.coverId === "" || seen.has(track.coverId)) {
      continue;
    }
    seen.add(track.coverId);
    out.push(track.coverId);
    if (out.length === 4) {
      break;
    }
  }

  return out;
}

/** totalDurationMs sums positive track durations in milliseconds. */
export function totalDurationMs(tracks: Track[]): number {
  let sum = 0;
  for (const track of tracks) {
    if (Number.isFinite(track.durationMs) && track.durationMs > 0) {
      sum += track.durationMs;
    }
  }

  return sum;
}

/** formatTotalDuration renders a playlist's total length as "3 hr 42 min" or "42 min". */
export function formatTotalDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) {
    return "";
  }
  const totalMinutes = Math.floor(ms / 60_000);
  if (totalMinutes < 1) {
    return "< 1 min";
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours > 0 && minutes > 0) {
    return `${hours} hr ${minutes} min`;
  }
  if (hours > 0) {
    return `${hours} hr`;
  }

  return `${minutes} min`;
}

/** formatTrackCount renders a singular or plural song count. */
export function formatTrackCount(count: number): string {
  return count === 1 ? "1 song" : `${count} songs`;
}
