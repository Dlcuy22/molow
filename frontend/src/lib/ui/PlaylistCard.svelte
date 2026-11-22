<script lang="ts">
  import MusicNotes from "phosphor-svelte/lib/MusicNotes";
  import type { Playlist } from "../types";
  import Cover from "./Cover.svelte";
  import { openContextMenu } from "./context-menu.svelte";

  let {
    list,
    onopen = (_id: string) => {},
  }: { list: Playlist; onopen?: (id: string) => void } = $props();

  const covers = $derived(list.coverIds ?? []);

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

<button
  class="group w-[166px] cursor-pointer rounded-lg text-left focus-visible:ring-2 focus-visible:ring-focus"
  onclick={() => onopen(list.id)}
  oncontextmenu={menu}
>
  <span class="block size-[166px] overflow-hidden rounded-lg bg-surface transition-opacity group-hover:opacity-90">
    {#if list.coverId}
      <Cover id={list.coverId} size={166} radius={0} class="block" />
    {:else if covers.length >= 4}
      <span class="grid h-full w-full grid-cols-2 grid-rows-2">
        {#each covers.slice(0, 4) as cid (cid)}
          <span class="overflow-hidden">
            <Cover id={cid} fill radius={0} class="block h-full w-full" />
          </span>
        {/each}
      </span>
    {:else if covers.length > 0}
      <Cover id={covers[0]} size={166} radius={0} class="block" />
    {:else}
      <span class="grid h-full w-full place-items-center text-muted">
        <MusicNotes size="36" />
      </span>
    {/if}
  </span>
  <span class="mt-1.5 block truncate font-display text-[15px] font-bold group-hover:underline">
    {list.name}
  </span>
  <span class="block text-[11px] text-muted">
    Playlist · {list.trackCount} {list.trackCount === 1 ? "song" : "songs"}
  </span>
</button>
