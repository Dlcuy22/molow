<script lang="ts">
  import MusicNotes from "phosphor-svelte/lib/MusicNotes";
  import type { Playlist } from "../types";
  import CardPlayButton from "./CardPlayButton.svelte";
  import Cover from "./Cover.svelte";
  import { openContextMenu } from "./context-menu.svelte";

  let {
    list,
    onopen = (_id: string) => {},
    onplay = (_list: Playlist) => {},
  }: {
    list: Playlist;
    onopen?: (id: string) => void;
    onplay?: (list: Playlist) => void;
  } = $props();

  const coverId = $derived(
    list.trackCount === 0 && !list.customCover && !list.remoteId
      ? ""
      : list.coverId || list.coverIds?.[0] || "",
  );

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
  <span class="relative block size-[166px] overflow-hidden rounded-lg bg-surface">
    {#if coverId}
      <Cover id={coverId} size={166} radius={0} class="block transition-opacity group-hover:opacity-90" />
    {:else}
      <span class="grid h-full w-full place-items-center text-muted transition-opacity group-hover:opacity-90">
        <MusicNotes size="36" />
      </span>
    {/if}
    {#if list.trackCount > 0}
      <CardPlayButton onplay={() => onplay(list)} />
    {/if}
  </span>
  <span class="mt-1.5 block truncate font-display text-[15px] font-bold">
    {list.name}
  </span>
  <span class="block text-[11px] text-muted">
    {#if list.remoteId && list.trackCount === 0}
      Playlist
    {:else}
      Playlist · {list.trackCount} {list.trackCount === 1 ? "song" : "songs"}
    {/if}
  </span>
</button>
