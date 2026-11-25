<script lang="ts">
  import CardPlayButton from "./CardPlayButton.svelte";
  import Cover from "./Cover.svelte";
  import type { Album } from "../types";
  import { openContextMenu } from "./context-menu.svelte";

  let {
    album,
    variant = "home",
    onopen = (_a: Album) => {},
    onplay = (_a: Album) => {},
  }: {
    album: Album;
    variant?: "home" | "library" | "artist";
    onopen?: (a: Album) => void;
    onplay?: (a: Album) => void;
  } = $props();

  const home = $derived(variant === "home");
  const meta = $derived.by(() => {
    if (home) {
      return album.artist;
    }
    if (variant === "artist") {
      return album.year > 0 ? String(album.year) : "";
    }
    if (album.songCount > 0) {
      return `Album · ${album.songCount} ${album.songCount === 1 ? "song" : "songs"}`;
    }
    if (album.year > 0) {
      return String(album.year);
    }

    return album.artist || "Album";
  });

  function menu(e: MouseEvent) {
    openContextMenu(e, {
      kind: "catalogue",
      id: album.id,
      label: album.name || "this album",
      libraryId: album.libraryId,
      album,
    });
  }
</script>

<div class="group w-[166px] shrink-0">
  <button
    class="relative block w-[166px] cursor-pointer overflow-hidden rounded-lg"
    style="height:{home ? 175 : 166}px"
    aria-label="{album.name} by {album.artist}"
    onclick={() => onopen(album)}
    oncontextmenu={menu}
  >
    <Cover
      id={album.coverId}
      src={album.imageUrl ?? ""}
      alt=""
      size={166}
      radius={8}
      class="transition-opacity group-hover:opacity-90"
    />
    <CardPlayButton onplay={() => onplay(album)} />
  </button>
  <div class="pt-1.5">
    <button
      class="block max-w-full cursor-pointer truncate text-left font-display font-bold leading-tight"
      class:text-[20px]={home}
      class:text-[15px]={!home}
      onclick={() => onopen(album)}
      oncontextmenu={menu}
    >
      {album.name}
    </button>
    {#if meta}
      <p
        class="truncate text-muted"
        class:text-[12px]={home}
        class:text-[11px]={!home}
      >
        {meta}
      </p>
    {/if}
  </div>
</div>
