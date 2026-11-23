import { get } from "svelte/store";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const setPausedCall = vi.fn();
const togglePauseCall = vi.fn();

vi.mock("../../bindings/github.com/dlcuy22/molow/backend", () => ({
  PlayerService: {
    SetPaused: (paused: boolean) => setPausedCall(paused),
    TogglePause: () => togglePauseCall(),
  },
}));

import type { Snapshot } from "../../bindings/github.com/dlcuy22/molow/backend/models";
import { EMPTY, applySnapshot, commands, player } from "./store";

function makeSnap(state: string): Snapshot {
  return {
    ...EMPTY,
    state,
    path: "/music/track.flac",
    queue: [
      {
        path: "/music/track.flac",
        title: "Track",
        artist: "Artist",
        album: "Album",
        durationMs: 180000,
        coverId: "",
      },
    ],
    queueIdx: 0,
  };
}

describe("optimistic togglePause", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setPausedCall.mockReset();
    togglePauseCall.mockReset();
    applySnapshot(makeSnap("playing"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("updates player state immediately before the engine responds", async () => {
    let resolveToggle!: () => void;
    setPausedCall.mockReturnValue(
      new Promise<void>((r) => {
        resolveToggle = r;
      }),
    );

    const p = commands.togglePause();
    expect(get(player).state).toBe("paused");
    expect(setPausedCall).toHaveBeenCalledWith(true);

    applySnapshot(makeSnap("paused"));
    resolveToggle();
    await p;

    expect(get(player).state).toBe("paused");
  });

  it("prevents flashing when toggling rapidly while an intermediate snapshot arrives", async () => {
    let resolveFirst!: () => void;
    let resolveSecond!: () => void;
    setPausedCall
      .mockReturnValueOnce(
        new Promise<void>((r) => {
          resolveFirst = r;
        }),
      )
      .mockReturnValueOnce(
        new Promise<void>((r) => {
          resolveSecond = r;
        }),
      );

    const observed: string[] = [];
    const unsub = player.subscribe((s) => observed.push(s.state));

    const p1 = commands.togglePause();
    expect(get(player).state).toBe("paused");

    const p2 = commands.togglePause();
    expect(get(player).state).toBe("playing");
    expect(setPausedCall).toHaveBeenNthCalledWith(1, true);
    expect(setPausedCall).toHaveBeenNthCalledWith(2, false);

    // Stale snapshot from the first toggle must not flash "paused".
    applySnapshot(makeSnap("paused"));
    expect(get(player).state).toBe("playing");

    resolveFirst();
    await p1;
    expect(get(player).state).toBe("playing");

    applySnapshot(makeSnap("playing"));
    resolveSecond();
    await p2;
    unsub();

    expect(observed).toEqual(["playing", "paused", "playing", "playing", "playing"]);
  });

  it("corrects the optimistic state when SetPaused fails", async () => {
    setPausedCall.mockRejectedValueOnce(new Error("engine unavailable"));

    await commands.togglePause();

    expect(get(player).state).toBe("playing");
    expect(get(player).error).toBe("engine unavailable");
  });

  it("holds optimistic state past 250ms and corrects after the settle window if the engine never transitions", async () => {
    setPausedCall.mockResolvedValueOnce(undefined);

    await commands.togglePause();
    expect(get(player).state).toBe("paused");

    // Stale pre-command snapshot at 300ms must not cause a premature flash.
    applySnapshot(makeSnap("playing"));
    vi.advanceTimersByTime(300);
    expect(get(player).state).toBe("paused");

    vi.advanceTimersByTime(1800);
    expect(get(player).state).toBe("playing");
  });
});
