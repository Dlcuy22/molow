// Single root-mounted context menu so scrolling shelves cannot clip it and
// only one menu can be open at a time.

import type { Album, Playlist, Track } from "../types";

export type MenuKind = "track" | "library" | "catalogue" | "playlist";

export interface MenuTarget {
  kind: MenuKind;
  /** Domain identifier: track ref, library id, catalogue key, or playlist id. */
  id: string;
  label: string;
  libraryId: string;
  /** Attached when the opener already holds the row, avoiding a re-lookup. */
  track?: Track;
  playlist?: Playlist;
  album?: Album;
}

export interface MenuItem {
  kind: "item" | "separator";
  label?: string;
  /** Right-aligned secondary text such as a shortcut or track count. */
  hint?: string;
  checked?: boolean;
  disabled?: boolean;
  danger?: boolean;
  run?: () => void;
}

export const contextMenu = $state({
  open: false,
  x: 0,
  y: 0,
  target: null as MenuTarget | null,
  /** Element that opened the menu, for restoring focus on pick or Escape. */
  anchor: null as HTMLElement | null,
});

export function openContextMenu(e: MouseEvent, target: MenuTarget): void {
  // Block the webview menu and stop inner rows from opening an outer card's menu.
  e.preventDefault();
  e.stopPropagation();
  contextMenu.open = true;
  contextMenu.x = e.clientX;
  contextMenu.y = e.clientY;
  contextMenu.target = target;
  contextMenu.anchor = e.currentTarget as HTMLElement | null;
}

/** Restore focus only on pick or Escape; outside clicks already moved focus. */
export function closeContextMenu(restoreFocus = false): void {
  const anchor = contextMenu.anchor;
  contextMenu.open = false;
  contextMenu.target = null;
  contextMenu.anchor = null;
  if (restoreFocus && anchor?.isConnected) {
    anchor.focus();
  }
}
