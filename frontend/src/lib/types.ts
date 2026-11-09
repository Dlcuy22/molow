// Types shared by the library pages. They mirror the Go library contract in
// docs/plan/library-overhaul.md. The store maps bindings into these shapes, so
// a component never imports a generated model directly.

export interface Track {
  ref: string;
  libraryId: string;
  nativeId: string;
  title: string;
  artist: string;
  album: string;
  durationMs: number;
  coverId: string;
  liked: boolean;
  flags: number;
  /** Track number in its album, when the tags carry one. */
  trackNo: number;
}

export interface Album {
  /** Stable grouping key: library id plus album name plus album artist. */
  id: string;
  libraryId: string;
  name: string;
  artist: string;
  coverId: string;
  songCount: number;
  durationMs: number;
  year: number;
}

export interface Artist {
  id: string;
  libraryId: string;
  name: string;
  coverId: string;
  songCount: number;
  albumCount: number;
}

export interface Playlist {
  id: string;
  libraryId: string;
  name: string;
  description: string;
  trackCount: number;
}

export type ShelfLayout = "list" | "grid";
export type ShelfKind = "tracks" | "albums" | "artists" | "playlists";

export interface Shelf {
  id: string;
  title: string;
  subtitle: string;
  layout: ShelfLayout;
  kind: ShelfKind;
  items: Track[];
  albums: Album[];
  artists: Artist[];
  playlists: Playlist[];
  cursor?: string;
  total?: number;
}

export interface Page {
  shelves: Shelf[];
  cursor: string;
}

export interface LibraryInfo {
  id: string;
  kind: string;
  name: string;
  root: string;
  status: string;
  message: string;
  capabilities: string[];
  trackCount: number;
}

export interface LyricLine {
  timeMs: number;
  text: string;
  translation: string;
}

export interface Lyrics {
  lines: LyricLine[];
  synced: boolean;
  source: string;
}

/** Accent is one ranked colour from the cover, as the album player needs it. */
export interface Accent {
  hex: string;
  fraction: number;
  luma: number;
}

export const FLAG_PLAYABLE = 1 << 0;
export const FLAG_LIKEABLE = 1 << 1;
export const FLAG_PLAYLIST_ADDABLE = 1 << 2;
export const FLAG_SAVED = 1 << 3;
