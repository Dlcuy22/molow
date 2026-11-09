<script lang="ts">
  import Cover from "./Cover.svelte";
  import type { Album } from "../types";

  let {
    album,
    variant = "home",
    onopen = (_a: Album) => {},
    onplay = (_a: Album) => {},
  }: {
    album: Album;
    variant?: "home" | "library";
    onopen?: (a: Album) => void;
    onplay?: (a: Album) => void;
  } = $props();

  const home = $derived(variant === "home");
  const meta = $derived(
    home ? album.artist : `Album · ${album.songCount} ${album.songCount === 1 ? "song" : "songs"}`,
  );
</script>

<div class="group w-[166px] shrink-0">
  <button
    class="relative block w-[166px] overflow-hidden rounded-lg"
    style="height:{home ? 175 : 166}px"
    aria-label="{album.name} by {album.artist}"
    onclick={() => onopen(album)}
  >
    <Cover id={album.coverId} alt="" size={166} radius={8} class="transition-opacity group-hover:opacity-90" />
    <span
      class="absolute bottom-2 right-2 grid size-9 place-items-center rounded-full bg-fg text-bg-dark opacity-0 shadow-lift transition-opacity group-hover:opacity-100"
      role="presentation"
      onclick={(e) => {
        e.stopPropagation();
        onplay(album);
      }}
    >
      <span class="ml-0.5 size-0 border-y-[6px] border-l-[9px] border-y-transparent border-l-bg-dark"></span>
    </span>
  </button>
  <div class="pt-1.5">
    <button
      class="block max-w-full truncate text-left font-display font-bold leading-tight hover:underline"
      class:text-[20px]={home}
      class:text-[15px]={!home}
      onclick={() => onopen(album)}
    >
      {album.name}
    </button>
    <p
      class="truncate text-muted"
      class:text-[12px]={home}
      class:text-[11px]={!home}
    >
      {meta}
    </p>
  </div>
</div>
