import { get } from "svelte/store";
import { beforeEach, describe, expect, it } from "vitest";
import {
  ACTIVITY_MAX_ITEMS,
  activityStore,
  clearAllActivities,
  clearFinishedActivities,
  resetActivities,
  spinnerStepMs,
  upsertActivity,
} from "./activity";

describe("spinnerStepMs", () => {
  it("runs at normal 200ms speed for small batches of 2-4 tasks", () => {
    expect(spinnerStepMs(0, 0)).toBe(200);
    expect(spinnerStepMs(2, 2)).toBe(200);
    expect(spinnerStepMs(3, 4)).toBe(200);
    expect(spinnerStepMs(4, 4)).toBe(200);
  });

  it("spins slowest when >5 tasks are active and monotonically accelerates down to fastest at 1", () => {
    const counts = [10, 8, 6, 5, 4, 3, 2, 1];
    const peak = 10;
    const durations = counts.map((c) => spinnerStepMs(c, peak));
    for (let i = 1; i < durations.length; i++) {
      expect(durations[i]).toBeLessThan(durations[i - 1]);
    }
    expect(spinnerStepMs(1, 1)).toBe(110);
    expect(spinnerStepMs(1, peak)).toBe(110);
  });
});

describe("activityStore", () => {
  beforeEach(() => {
    resetActivities();
  });

  it("patches items in place by id, prioritizes running items, and scopes by library", () => {
    upsertActivity({
      id: "a1",
      libraryId: "ytm:1",
      kind: "hydrate",
      title: "",
      detail: "Hydrating tracks",
      state: "running",
    });
    upsertActivity({
      id: "a1",
      libraryId: "ytm:1",
      kind: "hydrate",
      title: "capek :(",
      detail: "Synced",
      state: "done",
    });
    upsertActivity({
      id: "a2",
      libraryId: "ytm:1",
      kind: "import",
      title: "Roadtrip",
      detail: "Loading tracks",
      state: "running",
    });
    upsertActivity({
      id: "b1",
      libraryId: "local:main",
      kind: "scan",
      title: "Music",
      state: "running",
    });

    const all = get(activityStore);
    expect(all["ytm:1"]?.items.map((x) => x.id)).toEqual(["a2", "a1"]);
    expect(all["ytm:1"]?.items[1]?.title).toBe("capek :(");
    expect(all["local:main"]?.items.map((x) => x.id)).toEqual(["b1"]);
  });

  it("caps stored items per library at 50 and clears finished items", () => {
    for (let i = 0; i < 60; i++) {
      upsertActivity({
        id: `t-${i}`,
        libraryId: "ytm:1",
        kind: "hydrate",
        title: `Playlist ${i}`,
        state: i === 59 ? "running" : "done",
      });
    }

    const state = get(activityStore)["ytm:1"];
    expect(state?.items).toHaveLength(ACTIVITY_MAX_ITEMS);
    expect(state?.items[0]?.id).toBe("t-59");
    expect(state?.items.some((x) => x.id === "t-0")).toBe(false);

    clearFinishedActivities("ytm:1");
    expect(get(activityStore)["ytm:1"]?.items.map((x) => x.id)).toEqual(["t-59"]);

    clearAllActivities("ytm:1");
    expect(get(activityStore)["ytm:1"]?.items).toEqual([]);
  });

  it("preserves friendly retry prompt on late raw error and clears retry on restart", () => {
    const retry = () => {};
    upsertActivity({
      id: "ytm-import:PL1",
      libraryId: "ytm:1",
      kind: "import",
      title: "capek :(",
      detail: "Uh oh, failed to import the ytm playlist. wanna try again?",
      rawError: "context deadline exceeded",
      state: "error",
      retryLabel: "again",
      onRetry: retry,
    });
    upsertActivity({
      id: "ytm-import:PL1",
      libraryId: "ytm:1",
      kind: "import",
      title: "capek :(",
      detail: "Post https://music.youtube.com/youtubei/v1/browse: context deadline exceeded",
      state: "error",
    });

    let item = get(activityStore)["ytm:1"]?.items[0];
    expect(item?.detail).toBe("Uh oh, failed to import the ytm playlist. wanna try again?");
    expect(item?.onRetry).toBe(retry);
    expect(item?.rawError).toContain("context deadline exceeded");

    upsertActivity({
      id: "ytm-import:PL1",
      libraryId: "ytm:1",
      kind: "import",
      title: "capek :(",
      detail: "Starting import",
      state: "running",
    });
    item = get(activityStore)["ytm:1"]?.items[0];
    expect(item?.onRetry).toBeUndefined();
    expect(item?.retryLabel).toBeUndefined();
  });
});
