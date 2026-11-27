<script lang="ts">
  import { tick } from "svelte";
  import { fade, scale } from "svelte/transition";
  import { flip } from "svelte/animate";
  import { cubicIn, cubicOut } from "svelte/easing";
  import ArrowsClockwise from "phosphor-svelte/lib/ArrowsClockwise";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Plus from "phosphor-svelte/lib/Plus";
  import WarningCircle from "phosphor-svelte/lib/WarningCircle";
  import { activityStore } from "../lib/activity";
  import { showToast } from "../lib/toast";
  import type { Album, Artist, LibraryInfo, Playlist, Shelf, Track } from "../lib/types";
  import AlbumCard from "../lib/ui/AlbumCard.svelte";
  import BigArtistCard from "../lib/ui/BigArtistCard.svelte";
  import Chip from "../lib/ui/Chip.svelte";
  import LibraryActivity from "../lib/ui/LibraryActivity.svelte";
  import LoadingSpinner from "../lib/ui/LoadingSpinner.svelte";
  import type { LibraryAuthStatus } from "../lib/auth";
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
    authStatus = null,
    onresignin,
    onfilter = (_f: string) => {},
    onadd = () => {},
    onrefresh = () => {},
    oncreateLibrary = () => {},
    onimportFolder = () => {},
    onimportYTM = () => {},
    onopenAlbum = (_a: Album) => {},
    onplayAlbum = (_a: Album) => {},
    onopenArtist = (_a: Artist) => {},
    onopenTrackArtist,
    onplay = (_t: Track) => {},
    onopenPlaylist = (_list: Playlist) => {},
    onplayPlaylistCard = (_list: Playlist) => {},
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
    authStatus?: LibraryAuthStatus | null;
    onresignin?: () => void;
    onfilter?: (f: string) => void;
    onadd?: () => void;
    onrefresh?: () => boolean | Promise<boolean> | void;
    oncreateLibrary?: () => void;
    onimportFolder?: () => void;
    onimportYTM?: () => void;
    onopenAlbum?: (a: Album) => void;
    onplayAlbum?: (a: Album) => void;
    onopenArtist?: (a: Artist) => void;
    onopenTrackArtist?: (t: Track) => void;
    onplay?: (t: Track) => void;
    onopenPlaylist?: (list: Playlist) => void;
    onplayPlaylistCard?: (list: Playlist) => void;
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

  const canRefresh = $derived(Boolean(library?.capabilities.includes("refresh")));
  const isRefreshing = $derived(
    Boolean(
      library &&
        (library.status === "scanning" ||
          ($activityStore[library.id]?.items.some(
            (item) => item.state === "running" && (item.kind === "sync" || item.kind === "scan"),
          ) ??
            false)),
    ),
  );
  const refreshDesc = $derived(
    library?.kind === "local" ? "Refresh watched library" : "Refresh from remote library",
  );
  const refreshBlockedReason = $derived.by(() => {
    if (!library || library.kind === "local") {
      return "";
    }
    if (authStatus?.expired) {
      return "Session expired. Re-sign in to refresh this library.";
    }
    if (library.status === "signedOut" || (authStatus && !authStatus.signedIn)) {
      return "Signed out of YouTube Music. Sign in to refresh this library.";
    }
    if (authStatus?.error) {
      return authStatus.error;
    }
    return "";
  });

  let refreshTipHidden = $state(false);
  let refreshShaking = $state(false);
  let refreshShakeTimer: ReturnType<typeof setTimeout> | undefined;

  async function triggerRefreshShake() {
    clearTimeout(refreshShakeTimer);
    refreshShaking = false;
    await tick();
    refreshShaking = true;
    refreshShakeTimer = setTimeout(() => {
      refreshShaking = false;
    }, 380);
  }

  async function handleRefreshClick(e: MouseEvent) {
    refreshTipHidden = true;
    if (e.detail !== 0) {
      (e.currentTarget as HTMLButtonElement).blur();
    }
    if (refreshBlockedReason !== "") {
      void triggerRefreshShake();
      showToast({
        key: "library-refresh",
        kind: "message",
        label: refreshBlockedReason,
        icon: WarningCircle,
      });
      return;
    }
    const ok = await onrefresh();
    if (ok === false) {
      void triggerRefreshShake();
    }
  }
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

  type AllGridItem =
    | { key: string; kind: "playlist"; list: Playlist }
    | { key: string; kind: "album"; album: Album }
    | { key: string; kind: "track"; track: Track };

  const allGridItems = $derived<AllGridItem[]>([
    ...playlists.map((list) => ({ key: `pl:${list.id}`, kind: "playlist" as const, list })),
    ...sortedAlbums.map((album) => ({ key: `al:${album.id}`, kind: "album" as const, album })),
    ...tracks.slice(0, 5).map((track) => ({ key: `tr:${track.ref}`, kind: "track" as const, track })),
  ]);

  const prefersReducedMotion =
    typeof window !== "undefined" && Boolean(window.matchMedia?.("(prefers-reduced-motion: reduce)").matches);
  const EXIT_GAP_MS = 50;
  const flipDuration = prefersReducedMotion ? 0 : 250;
  const exitDuration = prefersReducedMotion ? 0 : 200;
  let pendingExitDelay = 0;

  function cardExit(node: Element) {
    if (exitDuration > 0) {
      pendingExitDelay = exitDuration + EXIT_GAP_MS;
      queueMicrotask(() => {
        pendingExitDelay = 0;
      });
    }
    return scale(node, { start: 0.9, opacity: 0, duration: exitDuration, easing: cubicIn });
  }

  function cardFlip(node: Element, animation: { from: DOMRect; to: DOMRect }) {
    return flip(node, animation, {
      delay: pendingExitDelay,
      duration: flipDuration,
      easing: cubicOut,
    });
  }
</script>

{#snippet playlistCard(list: Playlist)}
  <PlaylistCard list={list} onopen={() => onopenPlaylist(list)} onplay={onplayPlaylistCard} />
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
    {onopenTrackArtist}
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
      <div class="mt-1.5 flex shrink-0 items-center">
        {#if canRefresh}
          <div class="relative mr-2">
            <button
              type="button"
              class="peer grid size-8 shrink-0 place-items-center rounded-md border border-line/60 bg-surface text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
              class:opacity-60={refreshBlockedReason !== ""}
              class:animate-refresh-shake={refreshShaking}
              class:motion-reduce:animate-none={refreshShaking}
              aria-label={refreshDesc}
              aria-disabled={refreshBlockedReason !== "" ? "true" : undefined}
              data-refresh
              onclick={(e) => void handleRefreshClick(e)}
              onpointerleave={() => (refreshTipHidden = false)}
              onblur={() => (refreshTipHidden = false)}
              onanimationend={() => (refreshShaking = false)}
            >
              <ArrowsClockwise
                size="15"
                class={isRefreshing ? "animate-spin motion-reduce:animate-none" : ""}
                aria-hidden="true"
              />
            </button>
            <div
              role="tooltip"
              class="pointer-events-none absolute right-0 top-10 z-40 whitespace-nowrap rounded-md border border-line/60 bg-surface px-2.5 py-1 text-[11px] font-medium text-fg opacity-0 shadow-menu transition-opacity duration-150 motion-reduce:transition-none {refreshTipHidden
                ? ''
                : 'peer-hover:opacity-100 peer-focus-visible:opacity-100'}"
            >
              {refreshDesc}
            </div>
          </div>
        {/if}
        <LibraryActivity libraryId={library.id} />
        <button
          class="flex h-8 shrink-0 items-center gap-2 rounded-md bg-fg px-3 text-bg-dark focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          aria-haspopup="menu"
          data-add
          onclick={onadd}
        >
          <Plus size="14" weight="bold" />
          <span class="text-[12px] font-medium leading-none">Add</span>
        </button>
      </div>
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
  {#if library.kind !== "local" && (authStatus?.expired || library.status === "signedOut" || (authStatus && !authStatus.signedIn))}
    <div
      class="mt-4 flex flex-wrap items-center justify-between gap-3 rounded-md border border-line bg-surface px-3.5 py-2.5 text-[12px]"
      role="status"
    >
      <span class="text-fg">
        {authStatus?.expired
          ? "Session expired. Re-sign in to play uncached tracks and refresh playlists."
          : "Signed out of YouTube Music. Cached items stay available offline."}
      </span>
      {#if onresignin}
        <button
          type="button"
          class="h-7 shrink-0 rounded-[6px] bg-fg px-2.5 text-[12px] font-medium text-bg-dark transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          onclick={onresignin}
        >
          {authStatus?.expired ? "Re-sign in" : "Sign in"}
        </button>
      {/if}
    </div>
  {/if}
  <div class="mt-4 flex items-center justify-between">
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

  <div bind:this={scroller} class="scroll-thin mt-4 min-h-0 flex-1 overflow-y-auto pb-6">
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
              <div class="relative flex flex-wrap gap-x-4 gap-y-5">
                {#each artists as artist (artist.id)}
                  <div
                    animate:cardFlip
                    out:cardExit
                  >
                    <BigArtistCard
                      {artist}
                      subtitle="{artist.songCount} {artist.songCount === 1 ? 'song' : 'songs'}"
                      onopen={onopenArtist}
                    />
                  </div>
                {/each}
              </div>
            {/if}
          {:else if filter === "playlists"}
            {#if playlists.length === 0}
              <p class="text-[12px] text-muted">No playlists yet.</p>
            {:else}
              <div class="relative flex flex-wrap gap-x-4 gap-y-5">
                {#each playlists as list (list.id)}
                  <div
                    animate:cardFlip
                    out:cardExit
                  >
                    {@render playlistCard(list)}
                  </div>
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
              <div class="relative flex flex-wrap gap-x-4 gap-y-5">
                {#each allGridItems as item (item.key)}
                  <div
                    animate:cardFlip
                    out:cardExit
                  >
                    {#if item.kind === "playlist"}
                      {@render playlistCard(item.list)}
                    {:else if item.kind === "album"}
                      <AlbumCard
                        album={item.album}
                        variant="library"
                        onopen={onopenAlbum}
                        onplay={onplayAlbum}
                      />
                    {:else}
                      <TrackTile track={item.track} onplay={onplay} onopenArtist={onopenTrackArtist} />
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          {:else if albums.length === 0}
            <p class="text-[12px] text-muted">No albums in this library yet.</p>
          {:else}
            <div class="relative flex flex-wrap gap-x-4 gap-y-5">
              {#each sortedAlbums as album (album.id)}
                <div
                  animate:cardFlip
                  out:cardExit
                >
                  <AlbumCard {album} variant="library" onopen={onopenAlbum} onplay={onplayAlbum} />
                </div>
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

<style>
  @keyframes refresh-shake {
    0%,
    100% {
      transform: translateX(0);
    }
    20% {
      transform: translateX(-4px);
    }
    40% {
      transform: translateX(4px);
    }
    60% {
      transform: translateX(-3px);
    }
    80% {
      transform: translateX(3px);
    }
  }

  .animate-refresh-shake {
    animation: refresh-shake 340ms cubic-bezier(0.36, 0.07, 0.19, 0.97) both;
  }
</style>
