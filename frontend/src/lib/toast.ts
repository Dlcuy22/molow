// Status cards for actions that leave no visible result: a shortcut, or a
// settings button that clears a cache. Cards stack oldest first, and showing a
// key again updates that card in place, so a held volume key moves one bar
// instead of stacking one per press.

import { get, writable, type Readable } from "svelte/store";
import type { Component } from "svelte";

/** ToastKind names the card layout. */
export type ToastKind = "bar" | "tile" | "art" | "message";

/** Toast is one card to show. */
export interface Toast {
  /** Groups repeated shows: a second show with this key updates the card in
   *  place. With stack set, every show gets a card of its own. */
  key: string;
  kind: ToastKind;
  label: string;
  /** The component itself, not a name, so the host keeps no lookup table. */
  icon?: Component;
  /** Fill fraction 0..1, for kind "bar" only. */
  value?: number;
  /** The percentage or title, when a kind shows one. */
  text?: string;
  /** Icon-over-text layout, for message cards. */
  layout?: "inline" | "stacked";
  /** Shows a new card even when the key is already up, for a press that is its
   *  own event rather than a repeat of the last one. */
  stack?: boolean;
}

/** ToastCard is a card in the stack: the toast plus the id that tells two cards
 *  with the same key apart. */
export interface ToastCard extends Toast {
  id: number;
}

/** TOAST_TTL is how long each kind stays up: a bar is a glance, an art card
 *  carries text to read. A message card runs its own clock, so the store sets
 *  no timer for it. */
export const TOAST_TTL: Record<ToastKind, number> = {
  bar: 900,
  tile: 1200,
  art: 1500,
  message: 3000,
};

const store = writable<ToastCard[]>([]);

/** toasts is the stack, oldest first. */
export const toasts: Readable<ToastCard[]> = { subscribe: store.subscribe };

// One timer per card, so an expiring card cannot take another with it. Plain
// setTimeout keeps the store usable under vitest fake timers.
const timers = new Map<number, ReturnType<typeof setTimeout>>();
let nextId = 0;

function clearTimer(id: number): void {
  const timer = timers.get(id);
  if (timer === undefined) {
    return;
  }
  clearTimeout(timer);
  timers.delete(id);
}

function arm(card: ToastCard): void {
  if (card.kind === "message") {
    return;
  }
  const { id } = card;
  timers.set(
    id,
    setTimeout(() => clearToast(id), TOAST_TTL[card.kind]),
  );
}

/** showToast shows a card. A known key is updated in place, keeping its
 *  position, and its clock restarts with the new content; a new key, or stack,
 *  appends to the end. */
export function showToast(t: Toast): void {
  const list = get(store);
  const at = t.stack ? -1 : list.findIndex((cur) => cur.key === t.key);
  const card: ToastCard = { ...t, id: at === -1 ? ++nextId : list[at].id };

  store.set(at === -1 ? [...list, card] : list.map((cur, i) => (i === at ? card : cur)));
  clearTimer(card.id);
  arm(card);
}

/** clearToast removes the card with that id, if it is still up. */
export function clearToast(id: number): void {
  clearTimer(id);
  store.update((list) => (list.some((t) => t.id === id) ? list.filter((t) => t.id !== id) : list));
}
