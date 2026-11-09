// Artwork by cover id. The snapshot animation loop carries ids, not bytes; the
// bytes cross the bridge once per id, and every card that shares an album cover
// shares one fetch. The map is capped because a long session can scroll a large
// collection.

import { get, writable } from "svelte/store";
import { PlayerService } from "../../bindings/github.com/dlcuy22/molow/backend";

const MAX = 96;

export const coverUrls = writable<Record<string, string>>({});

const pending = new Map<string, Promise<string>>();
const order: string[] = [];

/** requestCover fetches an id if it is not already known or in flight. */
export function requestCover(id: string): void {
  if (id === "" || get(coverUrls)[id] !== undefined || pending.has(id)) {
    return;
  }

  const p = PlayerService.Cover(id)
    .catch(() => "")
    .then((url) => {
      pending.delete(id);
      if (url === "") {
        // Do not pin a miss: the art may not be cached yet and a later ask
        // should be allowed to pick it up.
        return url;
      }
      coverUrls.update((m) => {
        const next = { ...m, [id]: url };
        order.push(id);
        while (order.length > MAX) {
          const oldest = order.shift();
          if (oldest !== undefined) {
            delete next[oldest];
          }
        }

        return next;
      });

      return url;
    });
  pending.set(id, p);
}

/** coverFor is the synchronous read a component uses on first render. */
export function coverFor(map: Record<string, string>, id: string): string {
  return id === "" ? "" : (map[id] ?? "");
}
