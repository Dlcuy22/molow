import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import { clearToast, showToast, TOAST_TTL, toasts, type ToastKind } from "./toast";

// The store times cards with plain setTimeout, so fake timers drive the TTLs.

beforeEach(() => {
  // The store is module state and outlives a test, so drain it first.
  for (const t of get(toasts)) {
    clearToast(t.id);
  }
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

function keys(): string[] {
  return get(toasts).map((t) => t.key);
}

describe("toast stack", () => {
  it("appends a new key below the cards already up", () => {
    showToast({ key: "volume", kind: "bar", label: "Volume", value: 0.2, text: "20%" });
    showToast({ key: "mute", kind: "tile", label: "Muted" });

    // The host renders in order, so the newest card sits at the bottom.
    expect(keys()).toEqual(["volume", "mute"]);
  });

  it("updates a repeated key in place instead of stacking a copy", () => {
    // A held volume key shows one bar that keeps updating, not twenty.
    showToast({ key: "volume", kind: "bar", label: "Volume", value: 0.2, text: "20%" });
    showToast({ key: "mute", kind: "tile", label: "Muted" });
    showToast({ key: "volume", kind: "bar", label: "Volume", value: 0.6, text: "60%" });

    expect(keys()).toEqual(["volume", "mute"]);
    expect(get(toasts)[0].text).toBe("60%");
  });

  it("stacks a repeated key when stack is set", () => {
    // A single key can cover several cards, so the id is what tells them apart.
    showToast({ key: "clear", kind: "message", stack: true, label: "21 KB freed" });
    showToast({ key: "clear", kind: "message", stack: true, label: "already empty" });

    expect(keys()).toEqual(["clear", "clear"]);
    expect(new Set(get(toasts).map((t) => t.id)).size).toBe(2);
  });

  it("closes only the stacked card that expires or is named", () => {
    showToast({ key: "clear", kind: "tile", stack: true, label: "first" });
    showToast({ key: "clear", kind: "message", stack: true, label: "second" });

    const [first, second] = get(toasts);
    clearToast(second.id);
    expect(keys()).toEqual(["clear"]);
    expect(get(toasts)[0].label).toBe("first");

    vi.advanceTimersByTime(TOAST_TTL.tile);
    expect(keys()).toEqual([]);
  });

  it("expires each card on its own clock", () => {
    showToast({ key: "volume", kind: "bar", label: "Volume", value: 0.4 });
    vi.advanceTimersByTime(300);
    showToast({ key: "mute", kind: "tile", label: "Muted" });

    // The bar reaching zero must not take the younger tile with it.
    vi.advanceTimersByTime(600);
    expect(keys()).toEqual(["mute"]);

    vi.advanceTimersByTime(600);
    expect(keys()).toEqual([]);
  });

  it("drops only the card the id names", () => {
    showToast({ key: "a", kind: "message", label: "a" });
    showToast({ key: "b", kind: "message", label: "b" });
    clearToast(get(toasts)[0].id);

    expect(keys()).toEqual(["b"]);
  });
});

describe("toast expiry", () => {
  const cases: [ToastKind, number][] = [
    ["bar", TOAST_TTL.bar],
    ["tile", TOAST_TTL.tile],
    ["art", TOAST_TTL.art],
  ];

  for (const [kind, ttl] of cases) {
    it(`expires a ${kind} on its own TTL`, () => {
      showToast({ key: kind, kind, label: kind });
      expect(keys()).toEqual([kind]);

      vi.advanceTimersByTime(ttl - 1);
      expect(keys()).toEqual([kind]);

      vi.advanceTimersByTime(1);
      expect(keys()).toEqual([]);
    });
  }

  it("resets the clock when the same key is shown again", () => {
    showToast({ key: "volume", kind: "bar", label: "Volume", value: 0.2, text: "20%" });
    vi.advanceTimersByTime(TOAST_TTL.bar - 1);
    // A second press under the TTL replaces the card and restarts the clock.
    showToast({ key: "volume", kind: "bar", label: "Volume", value: 0.4, text: "40%" });

    vi.advanceTimersByTime(TOAST_TTL.bar - 1);
    expect(get(toasts)[0].text).toBe("40%");

    vi.advanceTimersByTime(1);
    expect(keys()).toEqual([]);
  });

  it("leaves a message card to its own clock", () => {
    // The card keeps its own clock, so the store must not remove it early.
    showToast({ key: "lyrics-cache", kind: "message", label: "Cache cleared" });
    vi.advanceTimersByTime(TOAST_TTL.message * 2);
    expect(keys()).toEqual(["lyrics-cache"]);

    clearToast(get(toasts)[0].id);
    expect(keys()).toEqual([]);
  });

  it("clears an old card without cancelling a newer one", () => {
    // A card's slide finishes after a newer card may already be up, so the old
    // card must not take the new card's expiry with it.
    showToast({ key: "message", kind: "message", label: "old" });
    showToast({ key: "volume", kind: "bar", label: "Volume", value: 0.4 });
    const old = get(toasts)[0];
    clearToast(old.id);
    expect(keys()).toEqual(["volume"]);

    vi.advanceTimersByTime(TOAST_TTL.bar);
    expect(keys()).toEqual([]);
  });
});
