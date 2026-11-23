<script lang="ts">
  import { fade } from "svelte/transition";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Plus from "phosphor-svelte/lib/Plus";
  import type { Album, LibraryInfo, Playlist, Shelf, Track } from "../lib/types";
  import AlbumCard from "../lib/ui/AlbumCard.svelte";
  import Chip from "../lib/ui/Chip.svelte";
  import Cover from "../lib/ui/Cover.svelte";
  import LoadingSpinner from "../lib/ui/LoadingSpinner.svelte";
  import PlaylistCard from "../lib/ui/PlaylistCard.svelte";
  import PlaylistView from "../lib/ui/PlaylistView.svelte";
  import TrackTile from "../lib/ui/TrackTile.svelte";

  let {
    library = null,
    shelves = [],
    filter = "all",
    loading = false,
    activePlaylist = null,
    playlistTracks = [],
    playlistLoading = false,
    playlistError = "",
    currentPath = "",
    playlistActive = false,
    playlistPlaying = false,
    onfilter = (_f: string) => {},
    onadd = () => {},
    oncreateLibrary = () => {},
    onimportFolder = () => {},
    onimportYTM = () => {},
    onopenAlbum = (_a: Album) => {},
    onplayAlbum = (_a: Album) => {},
    onplay = (_t: Track) => {},
    onopenPlaylist = (_list: Playlist) => {},
    onclosePlaylist = () => {},
    onretryPlaylist = () => {},
    onplayPlaylist = (_tracks: Track[]) => {},
    ontogglePlaylistPause = () => {},
    onplayPlaylistTrack = (_track: Track, _index: number, _ordered: Track[]) => {},
    onlikeTrack = (_track: Track, _liked: boolean) => {},
    onrenamePlaylist = (_list: Playlist) => {},
  }: {
    library?: LibraryInfo | null;
    shelves?: Shelf[];
    filter?: string;
    loading?: boolean;
    activePlaylist?: Playlist | null;
    playlistTracks?: Track[];
    playlistLoading?: boolean;
    playlistError?: string;
    currentPath?: string;
    playlistActive?: boolean;
    playlistPlaying?: boolean;
    onfilter?: (f: string) => void;
    onadd?: () => void;
    oncreateLibrary?: () => void;
    onimportFolder?: () => void;
    onimportYTM?: () => void;
    onopenAlbum?: (a: Album) => void;
    onplayAlbum?: (a: Album) => void;
    onplay?: (t: Track) => void;
    onopenPlaylist?: (list: Playlist) => void;
    onclosePlaylist?: () => void;
    onretryPlaylist?: () => void;
    onplayPlaylist?: (tracks: Track[]) => void;
    ontogglePlaylistPause?: () => void;
    onplayPlaylistTrack?: (track: Track, index: number, ordered: Track[]) => void;
    onlikeTrack?: (track: Track, liked: boolean) => void;
    onrenamePlaylist?: (list: Playlist) => void;
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
  let scroller = $state<HTMLDivElement | null>(null);

  const SPINNER_FADE_MS = 180;
  let spinnerMounted = $state(false);
  let spinnerLeaving = $state(false);
  let spinnerTimer: ReturnType<typeof setTimeout> | undefined;

  $effect(() => {
    clearTimeout(spinnerTimer);
    if (loading) {
      spinnerMounted = true;
      spinnerLeaving = false;

      return;
    }
    if (!spinnerMounted) {
      return;
    }
    spinnerLeaving = true;
    spinnerTimer = setTimeout(() => {
      spinnerLeaving = false;
      spinnerMounted = false;
    }, SPINNER_FADE_MS);

    return () => clearTimeout(spinnerTimer);
  });

  const showSpinner = $derived(loading || spinnerMounted);

  let lastFilter = "";
  $effect(() => {
    if (filter !== lastFilter) {
      lastFilter = filter;
      if (scroller) {
        scroller.scrollTop = 0;
      }
    }
  });

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

{#snippet playlistCard(list: Playlist)}
  <PlaylistCard list={list} onopen={() => onopenPlaylist(list)} />
{/snippet}

{#if activePlaylist}
  <PlaylistView
    playlist={activePlaylist}
    tracks={playlistTracks}
    loading={playlistLoading}
    error={playlistError}
    {currentPath}
    isActiveQueue={playlistActive}
    isPlaying={playlistPlaying}
    onback={onclosePlaylist}
    onretry={onretryPlaylist}
    onplayAll={onplayPlaylist}
    ontogglePause={ontogglePlaylistPause}
    onplayTrack={onplayPlaylistTrack}
    onlike={onlikeTrack}
    onrename={onrenamePlaylist}
  />
{:else}
<div class="flex h-full flex-col overflow-hidden pl-[50px] pr-9 pt-[41px]">
  <div class="flex items-start justify-between gap-4">
    <div class="min-w-0">
      <h1 class="truncate font-display text-[40px] font-bold leading-none tracking-tight">
        {library ? library.name : "Your Library"}
      </h1>
      {#if library?.description}
        <p class="mt-2 max-w-[600px] text-[12px] leading-relaxed text-muted">
          {library.description}
        </p>
      {/if}
    </div>
    {#if library}
      <button
        class="mt-1.5 flex h-8 shrink-0 items-center gap-2 rounded-md bg-fg px-3 text-bg-dark focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        aria-haspopup="menu"
        data-add
        onclick={onadd}
      >
        <Plus size="14" weight="bold" />
        <span class="text-[12px] font-medium leading-none">Add</span>
      </button>
    {/if}
  </div>

  {#if !library}
    <div class="mt-10 max-w-[420px]">
      <p class="text-[12px] leading-relaxed text-muted">
        No libraries yet. Create a library to organize your albums, folders with songs, or YouTube Music playlists.
      </p>
      <button
        class="mt-4 h-8 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        onclick={oncreateLibrary}
      >
        Create a library
      </button>
    </div>
  {:else}
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
        class="flex items-center gap-2 text-[12px] text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
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

  <div bind:this={scroller} class="scroll-thin mt-6 min-h-0 flex-1 overflow-y-auto pb-6">
    {#if showSpinner}
      <div
        class="flex h-full min-h-[240px] items-center justify-center transition-opacity duration-[180ms] ease-out"
        class:opacity-0={spinnerLeaving}
        class:opacity-100={!spinnerLeaving}
        in:fade={{ duration: 160 }}
      >
        <LoadingSpinner label="Loading catalogue ../\/\/\...." />
      </div>
    {:else}
      {#key filter}
        <div in:fade={{ duration: 160 }}>
          {#if filter === "artists"}
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
                  {@render playlistCard(list)}
                {/each}
              </div>
            {/if}
          {:else if filter === "all"}
            {#if tracks.length === 0 && albums.length === 0 && playlists.length === 0}
              <div class="max-w-[440px]">
                <p class="text-[12px] leading-relaxed text-muted">
                  This library is empty. Import a folder from your computer or add tracks from YouTube Music.
                </p>
                <div class="mt-4 flex flex-wrap gap-2">
                  <button
                    class="h-8 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                    onclick={onimportFolder}
                  >
                    Import from computer
                  </button>
                  <button
                    class="h-8 rounded-md border border-line bg-surface px-3 text-[12px] text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                    onclick={onimportYTM}
                  >
                    Import from YTM
                  </button>
                </div>
              </div>
            {:else}
              <div class="flex flex-wrap gap-x-4 gap-y-5">
                {#each playlists as list (list.id)}
                  {@render playlistCard(list)}
                {/each}
                {#each sortedAlbums as album (album.id)}
                  <AlbumCard
                    {album}
                    variant="library"
                    onopen={onopenAlbum}
                    onplay={onplayAlbum}
                  />
                {/each}
                {#each tracks.slice(0, 5) as track (track.ref)}
                  <TrackTile {track} onplay={onplay} />
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
      {/key}
    {/if}
  </div>
  {/if}
</div>
{/if}
