<script lang="ts">
  import ArrowLeft from "phosphor-svelte/lib/ArrowLeft";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import DotsThreeVertical from "phosphor-svelte/lib/DotsThreeVertical";
  import Heart from "phosphor-svelte/lib/Heart";
  import MusicNotes from "phosphor-svelte/lib/MusicNotes";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import Play from "phosphor-svelte/lib/Play";
  import SortAscending from "phosphor-svelte/lib/SortAscending";
  import ThumbsDown from "phosphor-svelte/lib/ThumbsDown";
  import type { Playlist, Track } from "../types";
  import { displayArtist, formatTime, subtitle } from "../format";
  import {
    PLAYLIST_SORTS,
    formatTotalDuration,
    formatTrackCount,
    playlistCovers,
    sortPlaylistTracks,
    totalDurationMs,
    type PlaylistSort,
  } from "../playlist-view";
  import Cover from "./Cover.svelte";
  import { openContextMenu } from "./context-menu.svelte";

  let {
    playlist,
    tracks = [],
    loading = false,
    error = "",
    currentPath = "",
    showDislike = false,
    onback = () => {},
    onretry = () => {},
    onplayAll = (_tracks: Track[]) => {},
    onplayTrack = (_track: Track, _index: number, _ordered: Track[]) => {},
    onlike = (_track: Track, _liked: boolean) => {},
    ondislike = (_track: Track) => {},
    onrename = (_playlist: Playlist) => {},
  }: {
    playlist: Playlist;
    tracks?: Track[];
    loading?: boolean;
    error?: string;
    currentPath?: string;
    showDislike?: boolean;
    onback?: () => void;
    onretry?: () => void;
    onplayAll?: (tracks: Track[]) => void;
    onplayTrack?: (track: Track, index: number, ordered: Track[]) => void;
    onlike?: (track: Track, liked: boolean) => void;
    ondislike?: (track: Track) => void;
    onrename?: (playlist: Playlist) => void;
  } = $props();

  let sortBy = $state<PlaylistSort>("order");
  let sortOpen = $state(false);

  const activeSortLabel = $derived(
    PLAYLIST_SORTS.find((opt) => opt.id === sortBy)?.label ?? "Playlist order",
  );
  const sortedTracks = $derived(sortPlaylistTracks(tracks, sortBy));
  const fallbackCover = $derived(
    (tracks.length > 0 ? playlistCovers(tracks) : (playlist.coverIds ?? []))[0] ?? "",
  );
  const trackCount = $derived(loading && tracks.length === 0 ? playlist.trackCount : tracks.length);
  const coverId = $derived(
    trackCount === 0 && !playlist.customCover ? "" : playlist.coverId || fallbackCover,
  );
  const creator = $derived((playlist.creator ?? "").trim());
  const totalTime = $derived(formatTotalDuration(totalDurationMs(tracks)));

  // Close the sort dropdown on outside pointer clicks or Escape without letting
  // Escape bubble up to leave the playlist view.
  $effect(() => {
    if (!sortOpen) {
      return;
    }
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as HTMLElement | null;
      if (target?.closest("[data-playlist-sort]")) {
        return;
      }
      sortOpen = false;
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        sortOpen = false;
      }
    };
    window.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("keydown", onKeyDown);

    return () => {
      window.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown);
    };
  });

  function openPlaylistMenu(e: MouseEvent) {
    openContextMenu(e, {
      kind: "playlist",
      id: playlist.id,
      label: playlist.name || "this playlist",
      libraryId: playlist.libraryId,
      playlist,
    });
  }

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

<div class="@container flex h-full min-h-0 flex-col overflow-hidden pl-[50px] pr-9 pt-6">
  <div class="shrink-0 pb-5">
    <button
      class="inline-flex h-8 items-center gap-2 rounded-md px-2 text-[12px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:ring-2 focus-visible:ring-focus"
      onclick={onback}
    >
      <ArrowLeft size="15" weight="bold" />
      <span>Your Library</span>
    </button>
  </div>

  <div class="flex min-h-0 flex-1 gap-10 overflow-hidden @max-[680px]:flex-col @max-[680px]:gap-6 @max-[680px]:overflow-y-auto">
    <!-- Left rail: square playlist artwork, title, optional creator, stats and actions. -->
    <aside class="flex w-[240px] shrink-0 flex-col items-center pt-1 @max-[680px]:w-full">
      <div
        class="relative size-[220px] shrink-0 overflow-hidden rounded-lg bg-surface @max-[680px]:size-[180px]"
        aria-hidden="true"
      >
        {#if coverId}
          <Cover id={coverId} fill radius={0} class="block h-full w-full" />
        {:else}
          <div class="grid h-full w-full place-items-center text-muted">
            <MusicNotes size="48" />
          </div>
        {/if}
      </div>

      <h1
        class="mt-5 line-clamp-2 w-full text-center font-display text-[22px] font-bold leading-snug text-fg"
        title={playlist.name}
      >
        {playlist.name}
      </h1>

      {#if creator !== ""}
        <p class="mt-1.5 max-w-full truncate text-[12px] font-medium text-fg">
          {creator}
        </p>
      {/if}

      <p class="mt-1.5 text-center text-[12px] tabular-nums text-muted">
        {#if totalTime !== ""}
          {formatTrackCount(trackCount)} · {totalTime}
        {:else}
          {formatTrackCount(trackCount)}
        {/if}
      </p>

      <div class="mt-5 flex items-center justify-center gap-3">
        <button
          class="grid size-9 place-items-center rounded-full bg-surface text-fg transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
          aria-label="Rename playlist"
          title="Rename playlist"
          onclick={() => onrename(playlist)}
        >
          <PencilSimple size="17" />
        </button>

        <button
          class="dock-play grid size-12 place-items-center rounded-full bg-fg text-bg-dark focus-visible:ring-2 focus-visible:ring-focus disabled:bg-disabled"
          aria-label="Play playlist"
          title="Play playlist"
          disabled={sortedTracks.length === 0}
          onclick={() => onplayAll(sortedTracks)}
        >
          <Play size="20" weight="fill" class="-translate-x-[1px]" />
        </button>

        <button
          class="grid size-9 place-items-center rounded-full bg-surface text-fg transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
          aria-label="Playlist options"
          title="Playlist options"
          aria-haspopup="menu"
          onclick={openPlaylistMenu}
        >
          <DotsThreeVertical size="18" weight="bold" />
        </button>
      </div>
    </aside>

    <!-- Right pane: sort filter and scrollable track rows. -->
    <section class="flex min-h-0 min-w-0 flex-1 flex-col @max-[680px]:overflow-visible" aria-label="Playlist tracks">
      <div class="relative mb-3 flex shrink-0 items-center" data-playlist-sort>
        <button
          class="inline-flex h-8 items-center gap-2 rounded-md px-2.5 text-[12px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:ring-2 focus-visible:ring-focus"
          aria-haspopup="menu"
          aria-expanded={sortOpen}
          onclick={() => (sortOpen = !sortOpen)}
        >
          <SortAscending size="16" />
          <span>{activeSortLabel}</span>
          <CaretDown size="13" weight="bold" />
        </button>

        {#if sortOpen}
          <div
            class="absolute left-0 top-9 z-30 w-[170px] rounded-lg bg-surface p-1.5 shadow-menu"
            role="menu"
          >
            {#each PLAYLIST_SORTS as option (option.id)}
              <button
                class="flex h-[30px] w-full items-center rounded-md px-2.5 text-left text-[12px] text-fg transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
                class:bg-selected={option.id === sortBy}
                role="menuitemradio"
                aria-checked={option.id === sortBy}
                onclick={() => {
                  sortBy = option.id;
                  sortOpen = false;
                }}
              >
                {option.label}
              </button>
            {/each}
          </div>
        {/if}
      </div>

      <div class="scroll-thin min-h-0 flex-1 overflow-y-auto pb-6 pr-1 @max-[680px]:overflow-visible">
        {#if loading && tracks.length === 0}
          <p class="px-2 py-4 text-[12px] text-muted" role="status">
            Loading playlist tracks...
          </p>
        {:else if error !== ""}
          <div class="rounded-lg bg-surface p-4" role="alert">
            <p class="text-[13px] font-medium text-fg">Could not load this playlist</p>
            <p class="mt-1 text-[12px] text-muted">{error}</p>
            <button
              class="mt-3 h-8 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark focus-visible:ring-2 focus-visible:ring-focus"
              onclick={onretry}
            >
              Try again
            </button>
          </div>
        {:else if sortedTracks.length === 0}
          <div class="max-w-[380px] px-2 py-4">
            <p class="text-[12px] text-muted">
              This playlist has no songs yet. Right-click any track in your library to add it here.
            </p>
            <button
              class="mt-3 h-8 rounded-md bg-surface px-3 text-[12px] font-medium text-fg transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
              onclick={onback}
            >
              Back to library
            </button>
          </div>
        {:else}
          <ul class="flex flex-col gap-0.5" role="list">
            {#each sortedTracks as track, i (track.ref + ":" + i)}
              {@const active = currentPath !== "" && track.ref === currentPath}
              {@const meta = subtitle(displayArtist(track.artist), track.album)}
              <li
                class="group flex items-center gap-3 rounded-md px-2 py-1.5 transition-colors {active
                  ? 'bg-selected'
                  : 'hover:bg-hover'}"
                oncontextmenu={(e) => openTrackMenu(e, track)}
              >
                <button
                  class="flex min-w-0 flex-1 cursor-pointer items-center gap-3 rounded-sm text-left focus-visible:ring-2 focus-visible:ring-focus"
                  onclick={() => onplayTrack(track, i, sortedTracks)}
                >
                  <span class="group/cover relative size-10 shrink-0 overflow-hidden rounded-[4px]">
                    <Cover id={track.coverId} size={40} radius={0} class="block" />
                    <span
                      class="pointer-events-none absolute inset-0 grid place-items-center bg-black/50 text-fg opacity-0 transition-opacity group-hover/cover:opacity-100"
                      aria-hidden="true"
                    >
                      <Play size="16" weight="fill" class="-translate-x-[1px]" />
                    </span>
                  </span>
                  <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span
                      class="truncate font-content text-[13px] font-bold leading-snug {active
                        ? 'text-accent'
                        : 'text-fg'}"
                    >
                      {track.title}
                    </span>
                    {#if meta !== ""}
                      <span class="truncate text-[11px] leading-snug text-muted">
                        {meta}
                      </span>
                    {/if}
                  </span>
                </button>

                <div class="flex shrink-0 items-center gap-1">
                  {#if showDislike}
                    <button
                      class="dock-btn grid size-8 place-items-center rounded-md text-muted transition-colors hover:bg-surface hover:text-fg focus-visible:ring-2 focus-visible:ring-focus"
                      aria-label="Dislike {track.title}"
                      title="Dislike"
                      onclick={() => ondislike(track)}
                    >
                      <ThumbsDown size="17" />
                    </button>
                  {/if}

                  <button
                    class="dock-btn grid size-8 place-items-center rounded-md transition-colors hover:bg-surface focus-visible:ring-2 focus-visible:ring-focus {track.liked
                      ? 'text-fg'
                      : 'text-muted hover:text-fg'}"
                    aria-label={track.liked ? `Unlike ${track.title}` : `Like ${track.title}`}
                    title={track.liked ? "Unlike" : "Like"}
                    aria-pressed={track.liked}
                    onclick={() => onlike(track, !track.liked)}
                  >
                    <Heart size="18" weight={track.liked ? "fill" : "regular"} />
                  </button>
                </div>

                <span class="w-12 shrink-0 text-right text-[12px] tabular-nums text-muted">
                  {track.durationMs > 0 ? formatTime(track.durationMs) : "--:--"}
                </span>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    </section>
  </div>
</div>
