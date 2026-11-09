<script lang="ts">
  import { CaretDown, Plus } from "phosphor-svelte";
  import type { Album, Shelf, Track } from "../lib/types";
  import AlbumCard from "../lib/ui/AlbumCard.svelte";
  import Chip from "../lib/ui/Chip.svelte";
  import Cover from "../lib/ui/Cover.svelte";

  let {
    shelves = [],
    filter = "all",
    loading = false,
    onfilter = (_f: string) => {},
    onadd = () => {},
    onopenAlbum = (_a: Album) => {},
    onplayAlbum = (_a: Album) => {},
    onplay = (_t: Track) => {},
    onopenPlaylist = (_id: string) => {},
  }: {
    shelves?: Shelf[];
    filter?: string;
    loading?: boolean;
    onfilter?: (f: string) => void;
    onadd?: () => void;
    onopenAlbum?: (a: Album) => void;
    onplayAlbum?: (a: Album) => void;
    onplay?: (t: Track) => void;
    onopenPlaylist?: (id: string) => void;
  } = $props();

  const filters = [
    { id: "all", label: "All" },
    { id: "playlists", label: "Playlists" },
    { id: "albums", label: "Albums" },
    { id: "artists", label: "Artists" },
  ];

  const albums = $derived(shelves.find((s) => s.kind === "albums")?.albums ?? []);
  const artists = $derived(shelves.find((s) => s.kind === "artists")?.artists ?? []);
  const playlists = $derived(shelves.find((s) => s.kind === "playlists")?.playlists ?? []);
  const tracks = $derived(shelves.flatMap((s) => s.items));

  const sorts = ["Recently added", "Title A–Z", "Artist A–Z"];
  let sortBy = $state("Recently added");
  let sortOpen = $state(false);

  const sortedAlbums = $derived(
    [...albums].sort((a, b) => {
      if (sortBy === "Title A–Z") {
        return a.name.localeCompare(b.name);
      }
      if (sortBy === "Artist A–Z") {
        return a.artist.localeCompare(b.artist) || a.name.localeCompare(b.name);
      }

      return 0;
    }),
  );
</script>

<div class="flex h-full flex-col overflow-hidden pl-[50px] pr-9 pt-[41px]">
  <div class="flex items-start justify-between">
    <h1 class="font-display text-[40px] font-bold leading-none tracking-tight">Your Library</h1>
    <button
      class="mt-1.5 flex h-8 items-center gap-2 rounded-md bg-fg px-3 text-bg-dark"
      aria-haspopup="menu"
      data-add
      onclick={onadd}
    >      <Plus size="14" weight="bold" />
      <span class="text-[12px] font-medium leading-none">Add</span>
    </button>
  </div>

  <div class="mt-[31px] flex items-center justify-between">
    <div class="flex gap-2">
      {#each filters as f (f.id)}
        <Chip
          label={f.label}
          selected={filter === f.id}
          onclick={() => onfilter(f.id)}
        />
      {/each}
    </div>
    <div class="relative">
      <button
        class="flex items-center gap-2 text-[12px] text-muted hover:text-fg"
        aria-haspopup="menu"
        aria-expanded={sortOpen}
        onclick={() => (sortOpen = !sortOpen)}
      >
        {sortBy}
        <CaretDown size="14" weight="bold" />
      </button>
      {#if sortOpen}
        <div
          class="absolute right-0 top-6 z-30 w-[160px] rounded-lg bg-surface p-1.5 shadow-menu"
          role="menu"
        >
          {#each sorts as option (option)}
            <button
              class="flex h-[30px] w-full items-center rounded-md px-2.5 text-left text-[12px] text-fg hover:bg-hover"
              class:bg-selected={option === sortBy}
              role="menuitemradio"
              aria-checked={option === sortBy}
              onclick={() => {
                sortBy = option;
                sortOpen = false;
              }}
            >
              {option}
            </button>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <div class="scroll-thin mt-6 min-h-0 flex-1 overflow-y-auto pb-6">
    {#if loading}
      <p class="text-[12px] text-muted">Reading the catalogue…</p>
    {:else if filter === "artists"}
      {#if artists.length === 0}
        <p class="text-[12px] text-muted">No artists in this library yet.</p>
      {:else}
        <div class="flex flex-wrap gap-x-4 gap-y-5">
          {#each artists as artist (artist.id)}
            <div class="w-[166px]">
              <Cover id={artist.coverId} size={166} radius={8} />
              <p class="mt-1.5 truncate font-display text-[15px] font-bold">{artist.name}</p>
              <p class="text-[11px] text-muted">
                Artist · {artist.songCount} {artist.songCount === 1 ? "song" : "songs"}
              </p>
            </div>
          {/each}
        </div>
      {/if}
    {:else if filter === "playlists"}
      {#if playlists.length === 0}
        <p class="text-[12px] text-muted">No playlists yet.</p>
      {:else}
        <div class="flex flex-wrap gap-x-4 gap-y-5">
          {#each playlists as list (list.id)}
            <button class="w-[166px] text-left" onclick={() => onopenPlaylist(list.id)}>
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
          {/each}
        </div>
      {/if}
    {:else if filter === "all"}
      {#if tracks.length === 0 && albums.length === 0}
        <p class="text-[12px] text-muted">The catalogue is empty.</p>
      {:else}
        <div class="flex flex-wrap gap-x-4 gap-y-5">
          {#each sortedAlbums as album (album.id)}
            <AlbumCard
              {album}
              variant="library"
              onopen={onopenAlbum}
              onplay={onplayAlbum}
            />
          {/each}
          {#each tracks.slice(0, 5) as track (track.ref)}
            <button
              class="flex h-[50px] w-[166px] items-center gap-2 rounded-lg bg-surface pl-1.5 pr-2 text-left"
              onclick={() => onplay(track)}
            >
              <Cover id={track.coverId} size={40} radius={8} />
              <span class="min-w-0">
                <span class="block truncate font-display text-[12px] font-bold">{track.title}</span>
                <span class="block truncate text-[10px] text-muted">{track.artist}</span>
              </span>
            </button>
          {/each}
        </div>
      {/if}
    {:else if albums.length === 0}
      <p class="text-[12px] text-muted">No albums in this library yet.</p>
    {:else}
      <div class="flex flex-wrap gap-x-4 gap-y-5">
        {#each sortedAlbums as album (album.id)}
          <AlbumCard {album} variant="library" onopen={onopenAlbum} onplay={onplayAlbum} />
        {/each}
      </div>
    {/if}
  </div>
</div>
