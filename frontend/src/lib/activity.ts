import { writable } from "svelte/store";
import { Events } from "@wailsio/runtime";

export const ACTIVITY_COMPACT_LIMIT = 5;
export const ACTIVITY_MAX_ITEMS = 50;
export const SPINNER_MIN_STEP_MS = 50;
export const SPINNER_MAX_STEP_MS = 500;

export type ActivityKind = "sync" | "import" | "scan" | "hydrate";
export type ActivityState = "running" | "done" | "error";

export interface ActivityEventPayload {
  id: string;
  libraryId: string;
  kind: ActivityKind;
  title: string;
  detail?: string;
  rawError?: string;
  done?: number;
  total?: number;
  state: ActivityState;
  retryLabel?: string;
  onRetry?: () => void;
}

export interface ActivityItem extends ActivityEventPayload {
  updatedAt: number;
  completedAt?: number;
}

export function formatShortTime(ts: number): string {
  return new Date(ts).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

export function formatCompletionMessage(item: ActivityItem, timeStr?: string): string {
  if (item.detail === "Stopped" || item.detail === "Already in playlist") {
    return item.detail;
  }
  const verb = item.kind === "import" ? "Imported" : item.kind === "scan" ? "Scanned" : "Synced";
  const at = timeStr ?? (item.completedAt ? formatShortTime(item.completedAt) : "");
  return at ? `${verb} ${item.title} at ${at}` : `${verb} ${item.title}`;
}

export interface LibraryActivityState {
  items: ActivityItem[];
  batchPeak: number;
}

/**
 * spinnerStepMs adapts the square-spinner cadence to the active task queue:
 * <5 tasks spin at normal 200ms speed, >5 tasks start slower and steadily speed
 * up as the queue drains, and 1 remaining task spins fastest at 110ms.
 */
export function spinnerStepMs(activeCount: number, batchPeak: number): number {
  if (activeCount <= 0) {
    return 200;
  }
  if (activeCount === 1) {
    return 110;
  }
  if (activeCount > 5) {
    return Math.min(340, Math.round(215 + (activeCount - 6) * 14));
  }
  if (batchPeak > 5) {
    return Math.round(135 + (activeCount - 2) * 20);
  }
  return 200;
}

export const activityStore = writable<Record<string, LibraryActivityState>>({});

let seq = 0;
function nextOrder(): number {
  seq += 1;
  return Date.now() * 1000 + (seq % 1000);
}

function sortAndCap(items: ActivityItem[]): ActivityItem[] {
  return [...items]
    .sort((a, b) => {
      const aRunning = a.state === "running" ? 1 : 0;
      const bRunning = b.state === "running" ? 1 : 0;
      if (aRunning !== bRunning) {
        return bRunning - aRunning;
      }
      return b.updatedAt - a.updatedAt;
    })
    .slice(0, ACTIVITY_MAX_ITEMS);
}

export function upsertActivity(payload: ActivityEventPayload): void {
  const libId = payload.libraryId?.trim();
  const id = payload.id?.trim();
  if (!libId || !id) {
    return;
  }
  const stamp = nextOrder();
  activityStore.update((all) => {
    const prev = all[libId] ?? { items: [], batchPeak: 0 };
    const existing = prev.items.find((item) => item.id === id);
    const title = payload.title?.trim() || existing?.title || "Task";
    const preserveRetry =
      existing?.state === "error" &&
      Boolean(existing.onRetry) &&
      payload.state === "error" &&
      !payload.onRetry;

    let nextItem: ActivityItem;
    if (preserveRetry && existing) {
      nextItem = {
        ...existing,
        title,
        rawError: payload.detail ?? payload.rawError ?? existing.rawError,
        updatedAt: stamp,
      };
    } else if (payload.state === "running" || payload.state === "done") {
      nextItem = {
        id,
        libraryId: libId,
        kind: payload.kind,
        title,
        detail: payload.detail ?? existing?.detail,
        done: payload.done ?? existing?.done,
        total: payload.total ?? existing?.total,
        state: payload.state,
        updatedAt: stamp,
        ...(payload.state === "done"
          ? { completedAt: existing?.completedAt ?? Date.now() }
          : {}),
      };
    } else {
      nextItem = {
        id,
        libraryId: libId,
        kind: payload.kind,
        title,
        detail: payload.detail ?? existing?.detail,
        rawError: payload.rawError ?? existing?.rawError,
        done: payload.done ?? existing?.done,
        total: payload.total ?? existing?.total,
        state: payload.state,
        retryLabel: payload.retryLabel,
        onRetry: payload.onRetry,
        updatedAt: stamp,
      };
    }

    const merged = existing
      ? prev.items.map((item) => (item.id === id ? nextItem : item))
      : [nextItem, ...prev.items];
    const items = sortAndCap(merged);
    const runningCount = items.filter((item) => item.state === "running").length;
    const batchPeak = runningCount === 0 ? 0 : Math.max(prev.batchPeak, runningCount);
    return {
      ...all,
      [libId]: { items, batchPeak },
    };
  });
}

export function clearFinishedActivities(libraryId: string): void {
  if (!libraryId) {
    return;
  }
  activityStore.update((all) => {
    const prev = all[libraryId];
    if (!prev) {
      return all;
    }
    const items = prev.items.filter((item) => item.state === "running");
    const runningCount = items.length;
    return {
      ...all,
      [libraryId]: {
        items,
        batchPeak: runningCount === 0 ? 0 : Math.max(prev.batchPeak, runningCount),
      },
    };
  });
}

export function clearAllActivities(libraryId: string): void {
  if (!libraryId) {
    return;
  }
  activityStore.update((all) => {
    if (!all[libraryId]) {
      return all;
    }
    return {
      ...all,
      [libraryId]: { items: [], batchPeak: 0 },
    };
  });
}

export function dismissActivity(libraryId: string, id: string): void {
  if (!libraryId || !id) {
    return;
  }
  activityStore.update((all) => {
    const prev = all[libraryId];
    if (!prev) {
      return all;
    }
    const items = prev.items.filter((item) => item.id !== id);
    const runningCount = items.filter((item) => item.state === "running").length;
    return {
      ...all,
      [libraryId]: {
        items,
        batchPeak: runningCount === 0 ? 0 : Math.max(prev.batchPeak, runningCount),
      },
    };
  });
}

export function resetActivities(): void {
  seq = 0;
  activityStore.set({});
}

export function connectActivity(): () => void {
  return Events.On("molo:activity", (ev) => {
    const payload = ev.data as ActivityEventPayload | null;
    if (payload) {
      upsertActivity(payload);
    }
  });
}
