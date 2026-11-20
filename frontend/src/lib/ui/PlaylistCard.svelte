<script lang="ts">
  import type { Playlist } from "../types";
  import { openContextMenu } from "./context-menu.svelte";

  let {
    list,
    onopen = (_id: string) => {},
  }: { list: Playlist; onopen?: (id: string) => void } = $props();

  function menu(e: MouseEvent) {
    openContextMenu(e, {
      kind: "playlist",
      id: list.id,
      label: list.name || "this playlist",
      libraryId: list.libraryId,
      playlist: list,
    });
  }
</script>

<button class="w-[166px] text-left" onclick={() => onopen(list.id)} oncontextmenu={menu}>
  <span class="grid h-[166px] w-[166px] place-items-center rounded-lg bg-surface">
    <span class="size-7 rounded-md border border-line"></span>
  </span>
  <span class="mt-1.5 block truncate font-display text-[15px] font-bold">
    {list.name}
  </span>
  <span class="block text-[11px] text-muted">
    Playlist · {list.trackCount} {list.trackCount === 1 ? "song" : "songs"}
  </span>
</button>
