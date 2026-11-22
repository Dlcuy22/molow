import type { Track } from "./types";

export type PlaylistSort = "order" | "title" | "artist" | "duration" | "liked";

export interface PlaylistSortOption {
  id: PlaylistSort;
  label: string;
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
