<script lang="ts">
  import type { Album, Shelf, Track } from "../lib/types";
  import AlbumCard from "../lib/ui/AlbumCard.svelte";
  import TrackTile from "../lib/ui/TrackTile.svelte";
  import { loadAccents } from "../lib/library";

  let {
    shelves = [],
    greeting = "For you this morning",
    onplay = (_t: Track) => {},
    onopenAlbum = (_a: Album) => {},
    onplayAlbum = (_a: Album) => {},
    onadd = () => {},
  }: {
    shelves?: Shelf[];
    greeting?: string;
    onplay?: (t: Track) => void;
    onopenAlbum?: (a: Album) => void;
    onplayAlbum?: (a: Album) => void;
    onadd?: () => void;
  } = $props();

  const recent = $derived(shelves.find((s) => s.id === "recent")?.items.slice(0, 9) ?? []);
  const albums = $derived(shelves.find((s) => s.id === "albums")?.albums.slice(0, 5) ?? []);
  const featured = $derived(albums[0]);

  let stops = $state<string[]>([]);
  let stopId = "";
  $effect(() => {
    const coverId = featured?.coverId ?? "";
    if (coverId === stopId) {
      return;
    }
    stopId = coverId;
    stops = [];
    if (coverId === "") {
      return;
    }
    void loadAccents(coverId).then((hex) => {
      if (stopId === coverId) {
        stops = hex;
      }
    });
  });

  const gradient = $derived(
    stops.length >= 2
      ? `linear-gradient(180deg, ${stops[0]} 11.66%, ${stops[1]} 75.95%)`
      : stops.length === 1
        ? `linear-gradient(180deg, ${stops[0]}, ${stops[0]})`
        : "linear-gradient(135deg, #3a3a3e, #2f2f32)",
  );
</script>

<div class="scroll-thin flex h-full flex-col overflow-y-auto pl-[50px] pr-9 pt-[41px]">
  <h1 class="shrink-0 font-display text-[40px] font-bold leading-none tracking-tight">{greeting}</h1>

  {#if shelves.length === 0}
    <div class="mt-10 max-w-[420px]">
      <p class="text-[12px] text-muted">
        Nothing in the library yet. Add a folder and its albums, tracks and artists appear here.
      </p>
      <button
        class="mt-4 h-8 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark"
        onclick={onadd}
      >
        Add a library folder
      </button>
    </div>
  {:else}
    <div class="mt-4 flex flex-wrap gap-5">
      {#if featured}
        <button
          class="relative h-[175px] w-[340px] shrink-0 overflow-hidden rounded-lg text-left"
          style="background:{gradient}"
          aria-label="Open {featured.name}"
          onclick={() => onopenAlbum(featured)}
        >
          <div class="absolute bottom-[14px] left-5">
            <p class="font-display text-[20px] font-bold leading-tight text-white">
              {featured.name}
            </p>
            <p class="text-[12px] leading-none text-white/80">{featured.artist}</p>
          </div>
          <span
            class="absolute bottom-[14px] right-[14px] grid size-[38px] place-items-center rounded-full bg-fg text-bg-dark"
            role="presentation"
            onclick={(e) => {
              e.stopPropagation();
              onplayAlbum(featured);
            }}
          >
            <span class="ml-0.5 size-0 border-y-[6px] border-l-[9px] border-y-transparent border-l-bg-dark"></span>
          </span>
        </button>
      {/if}

      <div class="grid max-w-[526px] shrink-0 grid-cols-3 gap-2">
        {#each recent as track (track.ref)}
          <TrackTile {track} onplay={onplay} />
        {/each}
      </div>
    </div>

    {#if albums.length > 0}
      <div class="mt-[38px] flex flex-wrap gap-4 pb-6">
        {#each albums as album (album.id)}
          <AlbumCard {album} variant="home" onopen={onopenAlbum} onplay={onplayAlbum} />
        {/each}
      </div>
    {/if}
  {/if}
</div>
