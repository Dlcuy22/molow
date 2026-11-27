// Catalogue rows omit Rename and Delete because neither is reversible without
// modifying or removing audio files on disk.

import { FLAG_LIKEABLE, FLAG_PLAYABLE, type Album, type Playlist, type Track } from "../types";
import type { LibraryAuthStatus } from "../auth";
import type { MenuItem, MenuTarget } from "./context-menu.svelte";

/** Injected app callbacks to keep this module free of store import cycles. */
export interface MenuDeps {
  playlists: Playlist[];
  next: (refs: string[]) => void;
  last: (refs: string[]) => void;
  like: (track: Track, liked: boolean) => void;
  addToPlaylist: (playlistID: string, refs: string[]) => void;
  albumRefs: (album: Album) => Promise<string[]>;
  play: (refs: string[]) => void;
  openAlbum: (album: Album) => void;
  openPlaylist: (playlistID: string) => void;
  renamePlaylist: (playlist: Playlist) => void;
  deletePlaylist: (playlist: Playlist) => void;
  /** Library actions take libraryID directly so right-clicking a rail row does
   *  not switch the active library first. */
  openLibrary: (libraryID: string) => void;
  editLibrary: (libraryID: string) => void;
  rescanLibrary: (libraryID: string) => void;
  removeLibrary: (libraryID: string) => void;
  libraryKindFor?: (libraryID: string) => string;
  libraryStatusFor?: (libraryID: string) => LibraryAuthStatus | undefined;
  reSignInLibrary?: (libraryID: string) => void;
  signOutLibrary?: (libraryID: string) => void;
  togglePlaylistWatch?: (playlist: Playlist, watched: boolean) => void;
}

function isBuiltInLiked(list: Playlist): boolean {
  return list.remoteId === "LM" || list.remoteId === "VLLM";
}

function isEditablePlaylist(list: Playlist): boolean {
  return list.owned !== false && !isBuiltInLiked(list);
}

function trackItems(target: MenuTarget, deps: MenuDeps): MenuItem[] {
  const track = target.track;
  const refs = [target.id];
  const unplayable = track !== undefined && (track.flags & FLAG_PLAYABLE) === 0;
  const rows: MenuItem[] = [
    { kind: "item", label: "Add to next", disabled: unplayable, run: () => deps.next(refs) },
    { kind: "item", label: "Add to queue", disabled: unplayable, run: () => deps.last(refs) },
  ];

  if (track === undefined) {
    return rows;
  }

  if ((track.flags & FLAG_LIKEABLE) !== 0 || track.liked) {
    rows.push({
      kind: "item",
      label: track.liked ? "Remove from Liked" : "Like",
      run: () => deps.like(track, !track.liked),
    });
  }

  const writablePlaylists = deps.playlists.filter(isEditablePlaylist);
  // Name a single playlist inline instead of showing a one-item list.
  if (writablePlaylists.length === 1) {
    const only = writablePlaylists[0];
    rows.push({
      kind: "item",
      label: `Add to ${only.name}`,
      run: () => deps.addToPlaylist(only.id, refs),
    });
  } else if (writablePlaylists.length > 1) {
    rows.push({ kind: "separator" });
    for (const list of writablePlaylists) {
      rows.push({
        kind: "item",
        label: list.name,
        hint: String(list.trackCount),
        run: () => deps.addToPlaylist(list.id, refs),
      });
    }
  }

  return rows;
}

function catalogueItems(target: MenuTarget, deps: MenuDeps): MenuItem[] {
  const album = target.album;
  if (album === undefined) {
    return [];
  }

  return [
    { kind: "item", label: "Open", run: () => deps.openAlbum(album) },
    { kind: "item", label: "Play", run: () => void deps.albumRefs(album).then(deps.play) },
    { kind: "separator" },
    {
      kind: "item",
      label: "Add to next",
      run: () => void deps.albumRefs(album).then(deps.next),
    },
    {
      kind: "item",
      label: "Add to queue",
      run: () => void deps.albumRefs(album).then(deps.last),
    },
  ];
}

function playlistItems(target: MenuTarget, deps: MenuDeps): MenuItem[] {
  const list = target.playlist;
  if (list === undefined) {
    return [{ kind: "item", label: "Open", run: () => deps.openPlaylist(target.id) }];
  }
  if (isBuiltInLiked(list)) {
    return [{ kind: "item", label: "Open", run: () => deps.openPlaylist(list.id) }];
  }

  const isLocal = deps.libraryKindFor?.(list.libraryId) === "local";
  const hasWatchedSource = isLocal && (Boolean(list.sourcePath) || Boolean(list.remoteId));
  const watchRow: MenuItem[] = hasWatchedSource
    ? [
        {
          kind: "item",
          label: list.sourcePath ? "Watch folder" : "Watch remote playlist",
          checked: Boolean(list.watched),
          run: () => deps.togglePlaylistWatch?.(list, !list.watched),
        },
      ]
    : [];

  if (list.owned === false) {
    return [
      { kind: "item", label: "Open", run: () => deps.openPlaylist(list.id) },
      ...watchRow,
      { kind: "separator" },
      { kind: "item", label: "Remove from library", danger: true, run: () => deps.deletePlaylist(list) },
    ];
  }

  return [
    { kind: "item", label: "Open", run: () => deps.openPlaylist(list.id) },
    ...watchRow,
    { kind: "item", label: "Rename", run: () => deps.renamePlaylist(list) },
    { kind: "separator" },
    { kind: "item", label: "Delete", danger: true, run: () => deps.deletePlaylist(list) },
  ];
}

function libraryItems(target: MenuTarget, deps: MenuDeps): MenuItem[] {
  const id = target.libraryId;
  const kind = deps.libraryKindFor?.(id) ?? "local";
  const isRemote = kind !== "" && kind !== "local";
  const refreshLabel = isRemote ? "Refresh library" : "Rescan files";
  // Auth (Sign in / Sign out) is only relevant for YTM libraries; Navidrome
  // uses CredentialStore and has no OAuth session flow.
  const isYTM = kind === "ytm";
  const auth = isYTM ? deps.libraryStatusFor?.(id) : undefined;

  const items: MenuItem[] = [
    { kind: "item", label: "Open", run: () => deps.openLibrary(id) },
    { kind: "item", label: "Edit", run: () => deps.editLibrary(id) },
    { kind: "item", label: refreshLabel, run: () => deps.rescanLibrary(id) },
  ];

  if (isYTM && auth) {
    if ((auth.expired || !auth.signedIn) && deps.reSignInLibrary) {
      items.push({
        kind: "item",
        label: auth.expired ? "Re-sign in" : "Sign in",
        run: () => deps.reSignInLibrary?.(id),
      });
    }
    if ((auth.signedIn || auth.expired) && deps.signOutLibrary) {
      items.push({
        kind: "item",
        label: "Sign out",
        run: () => deps.signOutLibrary?.(id),
      });
    }
  }

  items.push(
    { kind: "separator" },
    { kind: "item", label: "Remove from Molow", danger: true, run: () => deps.removeLibrary(id) },
  );
  return items;
}

export function menuItemsFor(target: MenuTarget, deps: MenuDeps): MenuItem[] {
  switch (target.kind) {
    case "track":
      return trackItems(target, deps);
    case "catalogue":
      return catalogueItems(target, deps);
    case "playlist":
      return playlistItems(target, deps);
    case "library":
      return libraryItems(target, deps);
  }
}
