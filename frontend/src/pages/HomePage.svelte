<script lang="ts">
  import type { Album, Shelf, Track } from "../lib/types";
  import AlbumCard from "../lib/ui/AlbumCard.svelte";
  import TrackTile from "../lib/ui/TrackTile.svelte";
  import PlayerBackdrop from "../lib/components/PlayerBackdrop.svelte";

  let {
    hasLibraries = true,
    shelves = [],
    greeting = "For you this morning",
    onplay = (_t: Track) => {},
    onopenAlbum = (_a: Album) => {},
    onplayAlbum = (_a: Album) => {},
    onopenTrackArtist,
    onadd = () => {},
    oncreateLibrary = () => {},
  }: {
    hasLibraries?: boolean;
    shelves?: Shelf[];
    greeting?: string;
    onplay?: (t: Track) => void;
    onopenAlbum?: (a: Album) => void;
    onplayAlbum?: (a: Album) => void;
    onopenTrackArtist?: (t: Track) => void;
    onadd?: () => void;
    oncreateLibrary?: () => void;
  } = $props();

  const recent = $derived(shelves.find((s) => s.id === "recent")?.items.slice(0, 9) ?? []);
  const albums = $derived(shelves.find((s) => s.id === "albums")?.albums.slice(0, 5) ?? []);
  const featured = $derived(albums[0]);
</script>

<div class="scroll-thin flex h-full flex-col overflow-y-auto pl-[50px] pr-9 pt-[41px]">
  <h1 class="shrink-0 font-display text-[40px] font-bold leading-none tracking-tight">{greeting}</h1>

  {#if !hasLibraries}
    <div class="mt-10 max-w-[420px]">
      <p class="text-[12px] leading-relaxed text-muted">
        No libraries yet. Create a library to store your albums, folders with songs, or YouTube Music playlists.
      </p>
      <button
        class="mt-4 h-8 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        onclick={oncreateLibrary}
      >
        Create a library
      </button>
    </div>
  {:else if shelves.length === 0}
    <div class="mt-10 max-w-[420px]">
      <p class="text-[12px] leading-relaxed text-muted">
        Nothing in this library yet. Import a folder from your computer and its albums, tracks, and playlists will appear here.
      </p>
      <button
        class="mt-4 h-8 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        onclick={onadd}
      >
        Import from computer
      </button>
    </div>
  {:else}
    <div class="mt-4 flex flex-wrap gap-5">
      {#if featured}
        <button
          class="relative h-[175px] w-[340px] shrink-0 cursor-pointer overflow-hidden rounded-lg bg-surface text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          aria-label="Open {featured.name}"
          onclick={() => onopenAlbum(featured)}
        >
          {#if featured.coverId}
            <PlayerBackdrop
              src={`/art/bg/${encodeURIComponent(featured.coverId)}`}
              active={false}
              useShader={false}
            />
            <div
              class="pointer-events-none absolute inset-0"
              style="background: linear-gradient(180deg, rgba(0, 0, 0, 0.14) 0%, rgba(0, 0, 0, 0.62) 100%);"
              aria-hidden="true"
            ></div>
          {/if}
          <div class="absolute bottom-[14px] left-5 right-[64px] z-10 min-w-0">
            <p class="truncate font-display text-[20px] font-bold leading-tight text-white">
              {featured.name}
            </p>
            <p class="truncate text-[12px] leading-none text-white/80">{featured.artist}</p>
          </div>
          <span
            class="absolute bottom-[14px] right-[14px] z-10 grid size-[38px] place-items-center rounded-full bg-fg text-bg-dark"
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
          <TrackTile {track} onplay={onplay} onopenArtist={onopenTrackArtist} />
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
