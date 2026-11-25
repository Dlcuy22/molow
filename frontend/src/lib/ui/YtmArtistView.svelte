<script lang="ts">
  import ArrowLeft from "phosphor-svelte/lib/ArrowLeft";
  import CaretLeft from "phosphor-svelte/lib/CaretLeft";
  import Heart from "phosphor-svelte/lib/Heart";
  import Play from "phosphor-svelte/lib/Play";
  import Shuffle from "phosphor-svelte/lib/Shuffle";
  import { formatTime } from "../format";
  import type { Album, Artist, Track, YtmArtist, YtmVideo } from "../types";
  import AlbumCard from "./AlbumCard.svelte";
  import BigArtistCard from "./BigArtistCard.svelte";
  import Cover from "./Cover.svelte";
  import HorizontalShelf from "./HorizontalShelf.svelte";
  import LoadingSpinner from "./LoadingSpinner.svelte";
  import VideoCard from "./VideoCard.svelte";
  import { openContextMenu } from "./context-menu.svelte";

  const TOP_SONGS_PREVIEW = 5;

  let {
    artist = null,
    loading = false,
    error = "",
    currentPath = "",
    backLabel = "Back",
    showSubscribe = false,
    subscribed = false,
    canShuffle = false,
    onback = () => {},
    onretry = () => {},
    onshuffle = () => {},
    onsubscribe = (_next: boolean) => {},
    onplayTrack = (_track: Track, _index: number, _tracks: Track[]) => {},
    onlike = (_track: Track, _liked: boolean) => {},
    onopenAlbum = (_album: Album) => {},
    onplayAlbum = (_album: Album) => {},
    onplayVideo = (_video: YtmVideo) => {},
    onopenArtist = (_artist: Artist) => {},
  }: {
    artist?: YtmArtist | null;
    loading?: boolean;
    error?: string;
    currentPath?: string;
    backLabel?: string;
    showSubscribe?: boolean;
    subscribed?: boolean;
    canShuffle?: boolean;
    onback?: () => void;
    onretry?: () => void;
    onshuffle?: () => void;
    onsubscribe?: (next: boolean) => void;
    onplayTrack?: (track: Track, index: number, tracks: Track[]) => void;
    onlike?: (track: Track, liked: boolean) => void;
    onopenAlbum?: (album: Album) => void;
    onplayAlbum?: (album: Album) => void;
    onplayVideo?: (video: YtmVideo) => void;
    onopenArtist?: (artist: Artist) => void;
  } = $props();

  let showAllSongs = $state(false);
  let bannerFailed = $state(false);

  const bannerSrc = $derived(
    artist?.bannerUrl ||
      (artist?.coverId ? `/art/${encodeURIComponent(artist.coverId)}` : ""),
  );
  const topSongs = $derived(artist?.topSongs ?? []);
  const visibleSongs = $derived(
    showAllSongs ? topSongs : topSongs.slice(0, TOP_SONGS_PREVIEW),
  );
  const hasMoreSongs = $derived(topSongs.length > TOP_SONGS_PREVIEW);
  const shuffleEnabled = $derived(canShuffle || topSongs.length > 0);
  const isSubscribed = $derived(subscribed || Boolean(artist?.subscribed));

  $effect(() => {
    void artist?.id;
    showAllSongs = false;
  });

  $effect(() => {
    void bannerSrc;
    bannerFailed = false;
  });

  function openTrackMenu(e: MouseEvent, track: Track) {
    openContextMenu(e, {
      kind: "track",
      id: track.ref,
      label: track.title || "this track",
      libraryId: track.libraryId,
      track,
    });
  }
</script>

<div class="scroll-thin flex h-full min-h-0 flex-col overflow-y-auto bg-bg-dark pb-10">
  {#if loading && !artist}
    <div class="px-[50px] pt-6">
      <button
        type="button"
        class="inline-flex h-8 items-center gap-2 rounded-md px-2 text-[12px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        onclick={onback}
      >
        <ArrowLeft size="15" weight="bold" />
        <span>{backLabel}</span>
      </button>
    </div>
    <div class="flex flex-1 items-center justify-center">
      <LoadingSpinner label="Loading artist..." />
    </div>
  {:else if error !== "" && !artist}
    <div class="px-[50px] pt-6">
      <button
        type="button"
        class="inline-flex h-8 items-center gap-2 rounded-md px-2 text-[12px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        onclick={onback}
      >
        <ArrowLeft size="15" weight="bold" />
        <span>{backLabel}</span>
      </button>
      <div class="mt-6 max-w-[420px] rounded-lg bg-surface p-4" role="alert">
        <p class="text-[13px] font-medium text-fg">Could not load this artist</p>
        <p class="mt-1 text-[12px] text-muted">{error}</p>
        <button
          type="button"
          class="mt-3 h-8 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          onclick={onretry}
        >
          Try again
        </button>
      </div>
    </div>
  {:else if artist}
    <!-- Hero banner with bottom blur + gradient feathering into bg-dark. -->
    <header class="relative min-h-[240px] w-full shrink-0 overflow-hidden bg-bg">
      {#if bannerSrc && !bannerFailed}
        <img
          src={bannerSrc}
          alt=""
          draggable="false"
          class="absolute inset-0 size-full scale-105 object-cover object-center blur-md"
          onerror={() => {
            bannerFailed = true;
          }}
        />
      {:else}
        <div
          class="absolute inset-0 bg-gradient-to-br from-surface via-bg to-bg-dark"
          aria-hidden="true"
        ></div>
      {/if}

      <div
        class="pointer-events-none absolute inset-x-0 bottom-0 h-32 backdrop-blur-md"
        style="mask-image: linear-gradient(to top, black 0%, black 35%, transparent 100%); -webkit-mask-image: linear-gradient(to top, black 0%, black 35%, transparent 100%);"
        aria-hidden="true"
      ></div>
      <div
        class="pointer-events-none absolute inset-0 bg-gradient-to-t from-bg-dark via-bg-dark/70 to-bg-dark/30"
        aria-hidden="true"
      ></div>

      <div class="relative z-10 flex h-full min-h-[240px] flex-col justify-between gap-6 pl-[50px] pr-9 pb-6 pt-6">
        <div>
          <button
            type="button"
            class="grid size-9 cursor-pointer place-items-center rounded-full bg-bg-dark/60 text-fg transition-colors hover:bg-bg-dark/85 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            aria-label={backLabel}
            title={backLabel}
            onclick={onback}
          >
            <CaretLeft size="16" weight="bold" />
          </button>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-6">
          <div class="flex min-w-0 flex-1 items-center gap-5">
            <div class="size-24 shrink-0 overflow-hidden rounded-full bg-surface sm:size-28">
              <Cover
                id={artist.coverId}
                src={artist.bannerUrl ?? ""}
                alt={artist.name}
                fill
                radius={9999}
                class="size-full"
              />
            </div>

            <div class="min-w-0">
              <p class="text-[12px] font-medium text-muted">Artist</p>
              <h1
                class="mt-1 truncate font-display text-[34px] font-bold leading-none tracking-tight text-fg sm:text-[42px]"
                title={artist.name}
              >
                {artist.name}
              </h1>
              {#if artist.subscriberText}
                <p class="mt-2 text-[12px] text-muted">
                  {artist.subscriberText}
                </p>
              {/if}
            </div>
          </div>

          <div class="flex shrink-0 flex-wrap items-center gap-2.5">
            <button
              type="button"
              class="inline-flex h-10 cursor-pointer items-center gap-2 rounded-full bg-fg px-5 text-[13px] font-semibold text-bg-dark transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:cursor-default disabled:bg-disabled disabled:opacity-60"
              disabled={topSongs.length === 0}
              onclick={() => {
                if (topSongs.length > 0) {
                  onplayTrack(topSongs[0], 0, topSongs);
                }
              }}
            >
              <Play size="15" weight="fill" />
              <span>Play</span>
            </button>

            <button
              type="button"
              class="inline-flex h-10 cursor-pointer items-center gap-2 rounded-full border border-line bg-bg-dark/60 px-4 text-[13px] font-medium text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:cursor-default disabled:opacity-50"
              disabled={!shuffleEnabled}
              onclick={onshuffle}
            >
              <Shuffle size="15" weight="bold" />
              <span>Shuffle</span>
            </button>

            {#if showSubscribe}
              <button
                type="button"
                class="inline-flex h-10 cursor-pointer items-center rounded-full border px-4 text-[13px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus {isSubscribed
                  ? 'border-line bg-surface text-fg hover:bg-hover'
                  : 'border-line bg-bg-dark/60 text-fg hover:bg-hover'}"
                aria-pressed={isSubscribed}
                onclick={() => onsubscribe(!isSubscribed)}
              >
                {isSubscribed ? "Subscribed" : "Subscribe"}
              </button>
            {/if}
          </div>
        </div>
      </div>
    </header>

    <div class="flex flex-col gap-9 pl-[50px] pr-9 pt-4">

      {#if topSongs.length > 0}
        <section class="flex flex-col gap-3" aria-label="Top songs">
          <div class="flex items-center justify-between gap-4">
            <h2 class="font-display text-[20px] font-bold leading-tight text-fg">
              Top songs
            </h2>
            {#if hasMoreSongs}
              <button
                type="button"
                class="h-7 cursor-pointer rounded-md px-2 text-[12px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                onclick={() => {
                  showAllSongs = !showAllSongs;
                }}
              >
                {showAllSongs ? "Show less" : "Show all"}
              </button>
            {/if}
          </div>

          <ul class="flex flex-col gap-0.5" role="list">
            {#each visibleSongs as track, i (track.ref + ":" + i)}
              {@const active = currentPath !== "" && track.ref === currentPath}
              <li
                class="group flex items-center gap-3 rounded-md px-2 py-1 transition-colors {active
                  ? 'bg-selected'
                  : 'hover:bg-hover'}"
                oncontextmenu={(e) => openTrackMenu(e, track)}
              >
                <button
                  type="button"
                  class="flex min-w-0 flex-1 cursor-pointer items-center gap-3 rounded-sm text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                  onclick={() => onplayTrack(track, i, topSongs)}
                >
                  <span class="group/cover relative size-9 shrink-0 overflow-hidden rounded-[4px]">
                    <Cover
                      id={track.coverId}
                      src={track.imageUrl ?? ""}
                      size={36}
                      radius={0}
                      class="block"
                    />
                    <span
                      class="pointer-events-none absolute inset-0 grid place-items-center bg-black/50 text-fg opacity-0 transition-opacity group-hover/cover:opacity-100"
                      aria-hidden="true"
                    >
                      <Play size="15" weight="fill" class="-translate-x-[1px]" />
                    </span>
                  </span>
                  <span
                    class="min-w-0 flex-1 truncate font-content text-[13px] font-bold leading-snug {active
                      ? 'text-accent'
                      : 'text-fg'}"
                  >
                    {track.title}
                  </span>
                  {#if track.album}
                    <span class="hidden max-w-[32%] shrink-0 truncate text-[12px] text-muted sm:inline">
                      {track.album}
                    </span>
                  {/if}
                </button>

                <button
                  type="button"
                  class="dock-btn grid size-8 shrink-0 place-items-center rounded-md transition-colors hover:bg-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus {track.liked
                    ? 'text-fg'
                    : 'text-muted hover:text-fg'}"
                  aria-label={track.liked ? `Unlike ${track.title}` : `Like ${track.title}`}
                  title={track.liked ? "Unlike" : "Like"}
                  aria-pressed={track.liked}
                  onclick={() => onlike(track, !track.liked)}
                >
                  <Heart size="17" weight={track.liked ? "fill" : "regular"} />
                </button>

                {#if track.durationMs > 0}
                  <span class="w-12 shrink-0 text-right text-[12px] tabular-nums text-muted">
                    {formatTime(track.durationMs)}
                  </span>
                {/if}
              </li>
            {/each}
          </ul>
        </section>
      {/if}

      {#if artist.albums.length > 0}
        <HorizontalShelf title="Top albums">
          {#each artist.albums as album (album.id)}
            <AlbumCard
              {album}
              variant="artist"
              onopen={onopenAlbum}
              onplay={onplayAlbum}
            />
          {/each}
        </HorizontalShelf>
      {/if}

      {#if artist.singles.length > 0}
        <HorizontalShelf title="Singles">
          {#each artist.singles as single (single.id)}
            <AlbumCard
              album={single}
              variant="artist"
              onopen={onopenAlbum}
              onplay={onplayAlbum}
            />
          {/each}
        </HorizontalShelf>
      {/if}

      {#if artist.videos.length > 0}
        <HorizontalShelf title="Videos">
          {#each artist.videos as video (video.id)}
            <VideoCard {video} onplay={onplayVideo} />
          {/each}
        </HorizontalShelf>
      {/if}

      {#if artist.relatedArtists.length > 0}
        <HorizontalShelf title="Related artists">
          {#each artist.relatedArtists as related (related.id)}
            <BigArtistCard artist={related} onopen={onopenArtist} />
          {/each}
        </HorizontalShelf>
      {/if}
    </div>
  {/if}
</div>
