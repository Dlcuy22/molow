<script lang="ts">
  import {
    CaretDown,
    Pause,
    Play,
    Repeat,
    RepeatOnce,
    Shuffle,
    SkipBack,
    SkipForward,
  } from "phosphor-svelte";
  import type { Snapshot } from "../../bindings/github.com/dlcuy22/molow/backend/models";
  import type { Lyrics } from "../lib/types";
  import { commands } from "../lib/store";
  import { formatTime } from "../lib/format";
  import SeekBar from "../lib/ui/SeekBar.svelte";
  import Marquee from "../lib/ui/Marquee.svelte";
  import Cover from "../lib/ui/Cover.svelte";

  let {
    snap,
    lyrics = null,
    stops = [],
    onclose = () => {},
    onseek = (_ms: number) => {},
  }: {
    snap: Snapshot;
    lyrics?: Lyrics | null;
    stops?: string[];
    onclose?: () => void;
    onseek?: (ms: number) => void;
  } = $props();

  const queue = $derived(snap.queue ?? []);
  const duration = $derived(snap.duration > 0 ? snap.duration : 0);
  const position = $derived(snap.position);
  const remaining = $derived(duration > 0 ? Math.max(0, duration - position) : 0);

  const background = $derived(
    stops.length >= 2
      ? `linear-gradient(180deg, ${stops[0]} 11.66%, ${stops[1]} 75.95%)`
      : "linear-gradient(180deg, #3a3a3e 11.66%, #1d1d20 75.95%)",
  );

  const lines = $derived(lyrics?.lines ?? []);
  let active = $state(0);
  $effect(() => {
    const pos = position;
    let idx = 0;
    for (let i = 0; i < lines.length; i++) {
      if (lines[i].timeMs >= 0 && lines[i].timeMs <= pos) {
        idx = i;
      }
    }
    active = idx;
  });

  let viewport: HTMLDivElement | undefined = $state();
  let pairEls: HTMLDivElement[] = [];
  let shift = $state(0);
  $effect(() => {
    const el = pairEls[active];
    if (el) {
      shift = 360 - el.offsetTop - el.offsetHeight / 2;
    }
  });
</script>

<div class="relative h-full w-full overflow-hidden text-fg" style="background:{background}">
  <button
    class="dock-btn dock-chip absolute right-[13px] top-[13px] z-20 grid size-8 place-items-center rounded-md text-fg transition-colors"
    aria-label="Close the full player"
    title="Close"
    onclick={onclose}
  >
    <CaretDown size="18" weight="bold" />
  </button>

  <div class="absolute left-[12%] top-[18%] w-[28.2%] min-w-[240px] max-w-[333px]">
    <Cover id={snap.coverId} alt={snap.album} size={333} radius={16} fill class="w-full" />

    <Marquee
      text={snap.title || "Nothing playing"}
      class="mt-[11px] font-display text-[20px] font-bold leading-tight"
    />
    <p class="truncate text-[12px] leading-tight">{snap.artist}</p>

    <div class="mt-[16px] flex items-center justify-between text-[10px] tabular-nums">
      <span>{formatTime(position)}</span>
      <span>{formatTime(remaining)}</span>
    </div>
    <div class="mt-1">
      <SeekBar
        position={snap.position}
        {duration}
        tone="fg"
        onseek={(ms) => onseek(ms)}
      />
    </div>

    <div class="mx-auto mt-4 flex w-[253px] max-w-full items-center justify-between">
      <button
        class="dock-btn dock-chip -m-[5.5px] grid size-9 place-items-center rounded-md text-fg transition-colors disabled:text-disabled"
        class:text-muted={!snap.shuffled}
        disabled={queue.length === 0}
        aria-label={snap.shuffled ? "Shuffle on" : "Shuffle off"}
        aria-pressed={snap.shuffled}
        onclick={() => commands.shuffle(!snap.shuffled)}
      >
        <Shuffle size="25" weight={snap.shuffled ? "fill" : "regular"} />
      </button>
      <button
        class="dock-btn -m-[5.5px] grid size-9 place-items-center text-fg transition-colors disabled:text-disabled"
        disabled={queue.length === 0}
        aria-label="Previous track"
        onclick={() => commands.prev()}
      >
        <SkipBack size="25" weight="fill" />
      </button>
      <button
        class="dock-btn dock-chip -m-[4px] grid size-10 place-items-center rounded-md text-fg transition-colors disabled:text-disabled"
        disabled={queue.length === 0}
        aria-label={snap.state === "playing" ? "Pause" : "Play"}
        onclick={() => commands.togglePause()}
      >
        {#if snap.state === "playing"}
          <Pause size="32" weight="fill" />
        {:else}
          <Play size="32" weight="fill" />
        {/if}
      </button>
      <button
        class="dock-btn -m-[5.5px] grid size-9 place-items-center text-fg transition-colors disabled:text-disabled"
        disabled={queue.length === 0}
        aria-label="Next track"
        onclick={() => commands.next()}
      >
        <SkipForward size="25" weight="fill" />
      </button>
      <button
        class="dock-btn dock-chip -m-[5.5px] grid size-9 place-items-center rounded-md text-fg transition-colors disabled:text-disabled"
        class:text-muted={snap.repeat === "off"}
        aria-label={snap.repeat === "off" ? "Repeat off" : "Repeat on"}
        aria-pressed={snap.repeat !== "off"}
        onclick={() => commands.setRepeat(snap.repeat === "off" ? "all" : "off")}
      >
        {#if snap.repeat === "one"}
          <RepeatOnce size="25" weight="fill" />
        {:else}
          <Repeat size="25" weight={snap.repeat === "all" ? "fill" : "regular"} />
        {/if}
      </button>
    </div>
  </div>

  <div class="absolute inset-y-0 left-1/2 right-0 pr-9">
    <div bind:this={viewport} class="h-full overflow-hidden">
      {#if lines.length === 0}
        <div class="flex h-full items-center pl-2 text-[12px] text-fg/50">
          No lyrics for this track.
        </div>
      {:else}
        <div
          class="flex flex-col gap-[22px] pl-2 transition-transform duration-500 ease-out"
          style="transform: translateY({shift}px)"
        >
          {#each lines as line, i (line.timeMs + ":" + i)}
            <div
              bind:this={pairEls[i]}
              class="flex flex-col gap-[2px]"
              class:opacity-100={i === active}
              class:opacity-[0.28]={i !== active}
            >
              <p class="font-jp text-[28px] font-medium leading-[1.4]">{line.text}</p>
              {#if line.translation}
                <p
                  class="text-[26px] font-semibold leading-[1.4]"
                  class:opacity-[0.65]={i === active}
                  class:opacity-20={i !== active}
                >
                  {line.translation}
                </p>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
</div>
