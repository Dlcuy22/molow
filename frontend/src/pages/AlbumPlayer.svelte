<script lang="ts">
  import { untrack } from "svelte";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import MusicNote from "phosphor-svelte/lib/MusicNote";
  import Pause from "phosphor-svelte/lib/Pause";
  import Play from "phosphor-svelte/lib/Play";
  import Repeat from "phosphor-svelte/lib/Repeat";
  import RepeatOnce from "phosphor-svelte/lib/RepeatOnce";
  import Shuffle from "phosphor-svelte/lib/Shuffle";
  import SkipBack from "phosphor-svelte/lib/SkipBack";
  import SkipForward from "phosphor-svelte/lib/SkipForward";
  import type { Snapshot } from "../../bindings/github.com/dlcuy22/molow/backend/models";
  import type { Lyrics, LyricLine, LyricWord } from "../lib/types";
  import { commands, spectrumConfig, appSettings } from "../lib/store";
  import { formatTime } from "../lib/format";
  import { sweepSegments } from "../lib/word-sweep";
  import SeekBar from "../lib/ui/SeekBar.svelte";
  import Marquee from "../lib/ui/Marquee.svelte";
  import Spectrum from "../lib/components/Spectrum.svelte";
  import Cover from "../lib/ui/Cover.svelte";

  let {
    snap,
    lyrics = null,
    stops = [],
    open = true,
    onclose = () => {},
    onseek = (_ms: number) => {},
  }: {
    snap: Snapshot;
    lyrics?: Lyrics | null;
    stops?: string[];
    open?: boolean;
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

  // Interpolate the 4 Hz snapshot position at display rate and publish --clock
  // once per frame on the active line so every word span updates via CSS.
  let clockBase = 0;
  let clockAt = 0;
  let clockRaf: number | undefined;

  const clockNow = (): number =>
    snap.state === "playing" ? clockBase + (performance.now() - clockAt) : clockBase;

  function paintClock() {
    const el = pairEls[active];
    if (!el || !el.isConnected) return;
    el.style.setProperty("--clock", String(Math.round(clockNow())));
  }

  function stopClock() {
    if (clockRaf !== undefined) {
      cancelAnimationFrame(clockRaf);
      clockRaf = undefined;
    }
  }

  function stepClock() {
    paintClock();
    clockRaf = requestAnimationFrame(stepClock);
  }

  // Re-anchor only on snapshot updates; tracking active here would stamp a stale position.
  $effect(() => {
    const pos = snap.position;
    clockBase = pos;
    clockAt = performance.now();
    untrack(paintClock);
  });

  // Paint --clock when lyrics load/reload or active changes while paused.
  $effect(() => {
    void lines;
    void active;
    paintClock();
  });

  $effect(() => {
    stopClock();
    if (!open || snap.state !== "playing") {
      untrack(paintClock);
      return;
    }
    stepClock();
    return stopClock;
  });

  // Opacity slope (0.3 -> 1.0) over the word's duration; zero-length words snap.
  const wordRate = (w: { startMs: number; endMs: number }): number => {
    const span = w.endMs - w.startMs;
    return span > 0 ? 0.7 / span : 1e6;
  };

  // instrumentalMark is the line text the TTML pre-processing leaves in a long
  // instrumental gap. The pane draws notes for it instead of the character.
  const instrumentalMark = "♪";

  type LyricTier = { text: string; words?: LyricWord[] };

  // Original line first, then romaji when present; empty word texts drop from sweepSegments.
  const lyricTiers = (line: LyricLine): LyricTier[] => {
    const tiers: LyricTier[] = [{ text: line.text, words: line.words }];
    if (line.romanized) {
      tiers.push({
        text: line.romanized,
        words: line.words?.map((w) => ({ ...w, text: w.romanized ?? "" })),
      });
    }

    return tiers;
  };

  const sweepBoth = $derived($appSettings.playback.lyrics.wordSweep !== "original");

  const visualizerStyle = $derived($appSettings.general.visualizerStyle ?? "minimal");
  const visualizerFull = $derived($spectrumConfig.enabled && visualizerStyle === "full");
  // lyricsFade masks the bottom of the lyrics column, transparent under the
  // band and solid above it, so scrolled text dissolves into the visualizer.
  const lyricsFade = [
    "-webkit-mask-image: linear-gradient(to bottom, #000 calc(100% - 170px), transparent calc(100% - 8px))",
    "mask-image: linear-gradient(to bottom, #000 calc(100% - 170px), transparent calc(100% - 8px))",
  ].join("; ");

  let viewport: HTMLDivElement | undefined = $state();
  let pairEls: HTMLDivElement[] = [];

  // The lyric column scrolls like a document: any wheel, touch drag or scroll
  // key hands control to the user for as long as they keep moving, and two
  // seconds after the movement stops the pane glides back to the sung line.
  // While the session is live the auto-follow stands down, so the pane never
  // fights the hand that is scrolling it.
  let userScrolling = $state(false);
  let idleTimer: ReturnType<typeof setTimeout> | undefined;

  // The glides, both the auto-follow and the two-second return are timed
  // here rather than left to CSS, so the pane's easing never fights a glide
  // the rAF loop is driving. Any user input cancels the flight immediately.
  let glideRaf: number | undefined;
  const GLIDE_MS = 700;

  function stopGlide() {
    if (glideRaf !== undefined) {
      cancelAnimationFrame(glideRaf);
      glideRaf = undefined;
    }
  }

  function glideTo(target: number) {
    if (!viewport) return;
    stopGlide();
    const vp = viewport;
    const from = vp.scrollTop;
    if (Math.abs(target - from) < 2) {
      vp.scrollTop = target;
      return;
    }
    const start = performance.now();
    // Ease in-out cubic: the pane leaves and arrives without a jerk, which a
    // single-ease curve reads as the sheet settling rather than snapping.
    const step = (now: number) => {
      const t = Math.min(1, (now - start) / GLIDE_MS);
      const eased = t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
      vp.scrollTop = from + (target - from) * eased;
      glideRaf = t < 1 ? requestAnimationFrame(step) : undefined;
    };
    glideRaf = requestAnimationFrame(step);
  }

  function beginUserScroll() {
    userScrolling = true;
    stopGlide();
    clearTimeout(idleTimer);
    idleTimer = setTimeout(returnToLine, 2000);
  }

  function endUserScroll() {
    userScrolling = false;
    clearTimeout(idleTimer);
  }

  function returnToLine() {
    endUserScroll();
    const el = pairEls[active];
    if (el && el.isConnected && viewport) {
      centerOn(el);
    }
  }

  // A line's scroll target puts it in the vertical centre of the pane, the
  // same spot the old transform put the active line at. The positions are
  // captured once at glide start, so mid-flight frames stay on the line
  // originally aimed at.
  function centerOn(el: HTMLElement) {
    if (!viewport) return;
    const vp = viewport.getBoundingClientRect();
    const r = el.getBoundingClientRect();
    glideTo(viewport.scrollTop + (r.top - vp.top) - vp.height / 2 + r.height / 2);
  }

  // Wheel, touch and the page scroll keys arm the user session. A CSS smooth
  // scroll the pane's own gliding fires scroll events too, so raw scroll
  // events never count as user intent. The arrows and space are transport
  // keys now: the shortcut layer consumes them, so they can no longer scroll
  // the pane and must not read as browsing.
  const armKeys = new Set(["PageUp", "PageDown", "Home", "End"]);

  function onwheel(e: WheelEvent) {
    if (e.deltaY !== 0) beginUserScroll();
  }

  function onkey(e: KeyboardEvent) {
    if (armKeys.has(e.key)) beginUserScroll();
  }

  // A fresh track clears any browse session and any flight in progress.
  $effect(() => {
    void lyrics;
    endUserScroll();
    stopGlide();
  });

  // The idle timer, the glide and the sweep clock must not outlive the pane.
  $effect(() => {
    return () => {
      clearTimeout(idleTimer);
      stopGlide();
      stopClock();
    };
  });

  // Auto-follow: scroll the active line to centre whenever the song moves and
  // the user is not browsing. Clicks are handled by their own handler, which
  // seeks first, so the line the user chose has become the active one.
  $effect(() => {
    if (userScrolling) return;
    const el = pairEls[active];
    if (el && el.isConnected) {
      centerOn(el);
    }
  });

  // The pointer dims out when the mouse rests: the artwork shows clean while
  // the hand is still. Any input, a move, a press, a wheel turn or a key
  // wakes it again.
  const CURSOR_IDLE_MS = 2000;
  let cursorIdle = $state(false);
  let cursorTimer: ReturnType<typeof setTimeout> | undefined;

  function wakeCursor() {
    cursorIdle = false;
    clearTimeout(cursorTimer);
    cursorTimer = setTimeout(() => (cursorIdle = true), CURSOR_IDLE_MS);
  }

  // The idle clock runs from the moment the sheet is up and is torn down with
  // it, so a closing sheet never leaves the pointer hidden.
  wakeCursor();
  $effect(() => {
    if (!open) wakeCursor();
    return () => clearTimeout(cursorTimer);
  });
</script>

<svelte:window
  onmousemove={wakeCursor}
  onpointerdown={wakeCursor}
  onwheel={wakeCursor}
  onkeydown={wakeCursor}
/>

{#snippet tier(text: string, words: LyricWord[] | undefined, swept: boolean)}
  {#if swept && words && words.length > 0}
    {@const segments = sweepSegments(text, words)}
    {#if segments}
      {#each segments as segment, j (j)}
        {#if segment.word}
          <span
            class="lyric-word"
            style="--from:{segment.word.startMs};--inv:{wordRate(segment.word)}"
          >{segment.text}</span>
        {:else}{segment.text}{/if}
      {/each}
    {:else}
      {text}
    {/if}
  {:else}
    {text}
  {/if}
{/snippet}

<div
  class="album-sheet h-full w-full overflow-hidden text-fg"
  class:open
  class:cursor-idle={cursorIdle}
  style="background:{background}"
>
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
    <p class="truncate text-[12px] leading-tight text-fg/56">{snap.artist}</p>

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
    <!-- A scroll container: the touch and key handlers exist to detect the
         user taking over the scroll, not to change the page. -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div
      bind:this={viewport}
      class="lyrics-scroll h-full overflow-y-auto overscroll-contain"
      style={visualizerFull ? lyricsFade : ""}
      role="region"
      aria-label="Lyrics"
      tabindex="-1"
      onwheel={onwheel}
      ontouchmove={beginUserScroll}
      onkeydown={onkey}
    >
    {#if lines.length === 0}
        <div class="flex h-full items-center pl-2 text-[12px] text-fg/50">
          No lyrics for this track.
        </div>
      {:else}
        <div class="flex flex-col gap-[22px] py-[calc(50vh-22px)] pl-2">
          {#each lines as line, i (line.timeMs + ":" + i)}
            {@const tiers = lyricTiers(line)}
            <div
              bind:this={pairEls[i]}
              class="flex flex-col gap-[2px]"
              class:opacity-100={i === active}
              class:opacity-[0.28]={i !== active}
            >
              {#if line.timeMs >= 0}
                <!-- A timed line is a seek control: choosing it jumps the track
                     to that second, ends any browse session and centres the
                     chosen line. -->
                <button
                  class="cursor-pointer rounded-md border-0 bg-transparent p-0 text-left font-jp text-[28px] font-medium leading-[1.4] text-inherit transition-opacity hover:opacity-80"
                  onclick={() => {
                    onseek(line.timeMs);
                    endUserScroll();
                    const el = pairEls[i];
                    if (el && el.isConnected) centerOn(el);
                  }}
                >
                  {#if line.text === instrumentalMark}
                    <span class="inline-flex items-center gap-[2px]" aria-hidden="true">
                      <MusicNote size={28} weight="fill" />
                      <MusicNote size={28} weight="fill" />
                      <MusicNote size={28} weight="fill" />
                    </span>
                  {:else}
                    <span class="block">{@render tier(tiers[0].text, tiers[0].words, i === active)}</span>
                    {#if tiers[1]}
                      <span class="mt-[3px] block font-jp text-[17px] font-normal italic leading-[1.4] text-fg/56">
                        {@render tier(tiers[1].text, tiers[1].words, i === active && sweepBoth)}
                      </span>
                    {/if}
                  {/if}
                </button>
              {:else}
                <p class="font-jp text-[28px] font-medium leading-[1.4]">
                  {@render tier(tiers[0].text, tiers[0].words, false)}
                </p>
                {#if tiers[1]}
                  <p class="mt-[3px] font-jp text-[17px] font-normal italic leading-[1.4] text-fg/56">
                    {@render tier(tiers[1].text, tiers[1].words, false)}
                  </p>
                {/if}
              {/if}
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
    {#if visualizerFull}
      <!-- The lyrics run on behind the band; this veil blurs them strongest at
           the bottom and lets go upward, so the join reads as depth. -->
      <div
        class="pointer-events-none absolute inset-x-0 bottom-0"
        style="height: 168px; -webkit-backdrop-filter: blur(7px); backdrop-filter: blur(7px); -webkit-mask-image: linear-gradient(to top, #000 0%, rgba(0, 0, 0, 0.7) 45%, transparent 100%); mask-image: linear-gradient(to top, #000 0%, rgba(0, 0, 0, 0.7) 45%, transparent 100%);"
        aria-hidden="true"
      ></div>
    {/if}
  </div>

  <!-- The visualizer: full runs across the bottom of the sheet, minimal keeps
       the strip under the art column. -->
  {#if $spectrumConfig.enabled}
    {#if visualizerFull}
      <div class="pointer-events-none absolute inset-x-0 bottom-0">
        <Spectrum height={72} opacity={0.5} chrome={false} />
      </div>
    {:else}
      <div class="pointer-events-none absolute bottom-0 left-0 right-1/2 pr-6">
        <Spectrum height={72} opacity={0.5} chrome={false} />
      </div>
    {/if}
  {/if}
</div>

<style>
  .lyric-word {
    opacity: clamp(0.3, calc(0.3 + (var(--clock, 0) - var(--from)) * var(--inv)), 1);
  }
</style>
