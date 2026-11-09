<script lang="ts">
  import {
    CaretUp,
    Heart,
    Queue,
    Repeat,
    RepeatOnce,
    Shuffle,
    SkipBack,
    SkipForward,
    Pause,
    Play,
    SpeakerHigh,
    SpeakerSlash,
    TextAlignLeft,
  } from "phosphor-svelte";
  import type { Snapshot } from "../../../bindings/github.com/dlcuy22/molow/backend/models";
  import { commands } from "../store";
  import { muted, toggleMute, volumeBy } from "../volume";
  import { fillFrac, formatTime, isActive } from "../format";
  import Cover from "./Cover.svelte";
  import Marquee from "./Marquee.svelte";

  let {
    snap,
    mode = "docked",
    queueOpen = false,
    liked = false,
    onexpand = () => {},
    onqueue = () => {},
    onlyrics = () => {},
    onlike = () => {},
  }: {
    snap: Snapshot;
    mode?: "docked" | "top" | "floating";
    queueOpen?: boolean;
    liked?: boolean;
    onexpand?: () => void;
    onqueue?: () => void;
    onlyrics?: () => void;
    onlike?: () => void;
  } = $props();

  const floating = $derived(mode === "floating");
  const queue = $derived(snap.queue ?? []);
  const live = $derived(isActive(snap.state));
  const duration = $derived(snap.duration > 0 ? snap.duration : 0);

  // The seek bar is optimistic: a drag moves the painted fill at once and the
  // snapshot only clears the held value once the engine agrees.
  let scrubbing = $state(false);
  let scrubValue = $state(0);
  let pendingSeek = $state<number | null>(null);
  const position = $derived(scrubbing ? scrubValue : (pendingSeek ?? snap.position));
  const seekFill = $derived(fillFrac(position, 0, duration));

  $effect(() => {
    if (pendingSeek !== null && Math.abs(snap.position - pendingSeek) < 750) {
      pendingSeek = null;
    }
  });
  $effect(() => {
    if (pendingSeek === null) {
      return;
    }
    const timer = setTimeout(() => (pendingSeek = null), 1500);

    return () => clearTimeout(timer);
  });

  function seekTo(ms: number) {
    const target = Math.min(Math.max(ms, 0), duration);
    pendingSeek = target;
    commands.seek(target);
  }

  function onSeekInput(e: Event) {
    scrubbing = true;
    pendingSeek = null;
    scrubValue = Number((e.currentTarget as HTMLInputElement).value);
  }

  function onSeekCommit(e: Event) {
    scrubbing = false;
    seekTo(Number((e.currentTarget as HTMLInputElement).value));
  }

  function onVolume(e: Event) {
    volumeBy(Number((e.currentTarget as HTMLInputElement).value) / 100 - snap.volume);
  }

  const volume = $derived(Math.round(snap.volume * 100));
  const remaining = $derived(duration > 0 ? Math.max(0, duration - position) : 0);
</script>

{#snippet transport()}
  <div class="flex w-[223px] items-center justify-between">
    <button
      class="transition-colors hover:text-fg disabled:text-disabled"
      class:text-fg={snap.shuffled}
      class:text-muted={!snap.shuffled}
      disabled={queue.length === 0}
      aria-label={snap.shuffled ? "Shuffle on" : "Shuffle off"}
      aria-pressed={snap.shuffled}
      onclick={() => commands.shuffle(!snap.shuffled)}
    >
      <Shuffle size="19" weight={snap.shuffled ? "fill" : "regular"} />
    </button>
    <button
      class="text-fg transition-colors hover:text-muted disabled:text-disabled"
      disabled={queue.length === 0}
      aria-label="Previous track"
      onclick={() => commands.prev()}
    >
      <SkipBack size="22" weight="fill" />
    </button>
    <button
      class="grid size-[38px] place-items-center rounded-full bg-fg text-bg-dark transition-colors hover:brightness-95 disabled:bg-disabled"
      disabled={queue.length === 0}
      aria-label={snap.state === "playing" ? "Pause" : "Play"}
      onclick={() => commands.togglePause()}
    >
      {#if snap.state === "playing"}
        <Pause size="17" weight="fill" />
      {:else}
        <Play size="17" weight="fill" />
      {/if}
    </button>
    <button
      class="text-fg transition-colors hover:text-muted disabled:text-disabled"
      disabled={queue.length === 0}
      aria-label="Next track"
      onclick={() => commands.next()}
    >
      <SkipForward size="22" weight="fill" />
    </button>
    <button
      class="transition-colors hover:text-fg"
      class:text-fg={snap.repeat !== "off"}
      class:text-muted={snap.repeat === "off"}
      aria-label={snap.repeat === "off" ? "Repeat off" : "Repeat on"}
      aria-pressed={snap.repeat !== "off"}
      onclick={() => commands.setRepeat(snap.repeat === "off" ? "all" : "off")}
    >
      {#if snap.repeat === "one"}
        <RepeatOnce size="19" weight="fill" />
      {:else}
        <Repeat size="19" weight={snap.repeat === "all" ? "fill" : "regular"} />
      {/if}
    </button>
  </div>
{/snippet}

<!--
  Dock: the persistent bar. Docked, the progress is the top edge (or the bottom
  edge when the dock sits above the content); floating, there is room for the
  bar under the transport with both times. The play disc is the only inverted
  surface in the bar.
-->
<div
  class="group relative h-[95px] w-full bg-bg text-fg"
  class:rounded-[16px]={floating}
  class:shadow-float={floating}
  class:border={floating}
  class:border-line={floating}
>
  {#if mode !== "floating"}
    <div
      class="absolute inset-x-0 h-[3px] {mode === 'top' ? 'bottom-0' : 'top-0'}"
      aria-hidden="true"
    >
      <div class="absolute inset-0 bg-track"></div>
      <div
        class="seek-fill absolute inset-y-0 left-0 bg-accent"
        style="width: {seekFill * 100}%"
      ></div>
    </div>
  {/if}

  <!-- Left: art, identity, like. It is bounded on the right by the transport's
       own edge, so a long title marquees instead of running under the controls. -->
  <div
    class="absolute left-0 top-1/2 flex min-w-0 -translate-y-1/2 items-center gap-3 pl-2"
    style="right: calc(50% + 120px)"
  >
    <Cover id={snap.coverId} alt="" size={68} radius={8} class="shrink-0" />
    <div class="flex min-w-0 flex-1 flex-col gap-[2px]">
      <Marquee
        text={snap.title || "Nothing playing"}
        class="font-display text-[15px] font-bold leading-none"
      />
      <span class="truncate text-[11px] text-muted">{snap.artist}</span>
    </div>
    <button
      class="grid size-8 shrink-0 place-items-center rounded-md text-fg transition-colors hover:bg-hover"
      aria-label={liked ? "Unlike" : "Like"}
      title={liked ? "Unlike" : "Like"}
      aria-pressed={liked}
      onclick={onlike}
    >
      <Heart size="19" weight={liked ? "fill" : "regular"} />
    </button>
  </div>

  <!-- Centre: the transport itself sits on the window's centre line. The
       duration is a separate element 24 px past its right edge, so including
       it does not pull the controls off centre. -->
  <div class="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2">
    {#if floating}
      <div class="flex flex-col items-center gap-2">
        {@render transport()}
        <div class="seek relative flex h-4 w-[400px] items-center" class:seek-instant={scrubbing}>
          <div class="pointer-events-none absolute inset-x-0 top-1/2 h-1 -translate-y-1/2 rounded-full bg-track"></div>
          <div class="pointer-events-none absolute inset-x-0 top-1/2 h-1 -translate-y-1/2 overflow-hidden rounded-full">
            <div class="seek-fill h-full rounded-full bg-accent" style="width: {seekFill * 100}%"></div>
          </div>
          <input
            class="seek-input absolute inset-0 m-0 h-full w-full cursor-pointer appearance-none bg-transparent opacity-0 disabled:cursor-default"
            type="range"
            min="0"
            max={duration || 1}
            step="1000"
            value={position}
            disabled={duration === 0}
            aria-label="Seek"
            oninput={onSeekInput}
            onchange={onSeekCommit}
          />
        </div>
        <div class="flex w-[400px] justify-between text-[10px] tabular-nums text-muted">
          <span>{formatTime(position)}</span>
          <span>{formatTime(remaining)}</span>
        </div>
      </div>
    {:else}
      {@render transport()}
    {/if}
  </div>

  {#if !floating}
    <span
      class="dock-duration absolute top-1/2 -translate-y-1/2 text-[11px] tabular-nums text-muted"
      style="left: calc(50% + 136px)"
    >
      {formatTime(position)} / {formatTime(duration)}
    </span>
  {/if}

  <!-- Right: lyrics, queue, volume. The queue toggle keeps its pill while the
       panel is open, so the dock always says which panel is up. -->
  <div
    class="absolute right-0 top-1/2 flex -translate-y-1/2 items-center justify-end gap-[21px] pr-[55px] {floating
      ? 'opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100'
      : ''}"
  >
    <button
      class="grid size-[18px] place-items-center text-muted transition-colors hover:text-fg"
      aria-label="Lyrics"
      title="Lyrics"
      onclick={onlyrics}
    >
      <TextAlignLeft size="18" />
    </button>

    <div class="relative grid size-7 place-items-center" data-queue>
      {#if queueOpen}
        <span class="absolute inset-0 rounded-lg bg-[#d9d9d9] opacity-30" aria-hidden="true"></span>
      {/if}
      <button
        class="relative text-muted transition-colors hover:text-fg"
        class:text-fg={queueOpen}
        class:text-muted={!queueOpen}
        aria-label="Queue"
        aria-pressed={queueOpen}
        title="Queue"
        onclick={onqueue}
      >
        <Queue size="18" />
      </button>
    </div>

    <div class="flex items-center gap-1.5">
      <button
        class="grid size-[18px] place-items-center text-muted transition-colors hover:text-fg"
        aria-label={$muted ? "Unmute" : "Mute"}
        onclick={toggleMute}
      >
        {#if volume === 0 || $muted}
          <SpeakerSlash size="18" />
        {:else}
          <SpeakerHigh size="18" />
        {/if}
      </button>
      <div class="relative flex h-[18px] w-[60px] items-center">
        <div class="absolute inset-x-0 top-1/2 h-1 -translate-y-1/2 rounded-full bg-track"></div>
        <div
          class="absolute left-0 top-1/2 h-1 -translate-y-1/2 rounded-full bg-accent"
          style="width: {volume}%"
        ></div>
        <input
          class="absolute inset-0 h-full w-full cursor-pointer appearance-none bg-transparent opacity-0"
          type="range"
          min="0"
          max="100"
          step="1"
          value={volume}
          aria-label="Volume"
          oninput={onVolume}
        />
      </div>
    </div>
  </div>

  <button
    class="absolute right-[18px] grid size-[18px] place-items-center text-muted transition-colors hover:text-fg"
    aria-label="Open the full player"
    title="Open the full player"
    onclick={onexpand}
  >
    <CaretUp size="18" />
  </button>
</div>
