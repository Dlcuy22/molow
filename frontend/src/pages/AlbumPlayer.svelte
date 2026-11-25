<script lang="ts">
  import { untrack } from "svelte";
  import { fade } from "svelte/transition";
  import { Browser } from "@wailsio/runtime";
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
  import {
    activeLineAt,
    instrumentalNoteProgress,
    isRTLText,
    JITTER_HOLD_MS,
    lyricOffsetFor,
    lyricProviderInfo,
    lyricSyncLabel,
    nextClockWakeMs,
    reanchorClock,
    seekTargetMs,
    wordOpacity,
    wordSweepBackgroundPosition,
  } from "../lib/lyric-clock";
  import { sweepSegments } from "../lib/word-sweep";
  import SeekBar from "../lib/ui/SeekBar.svelte";
  import Marquee from "../lib/ui/Marquee.svelte";
  import Spectrum from "../lib/components/Spectrum.svelte";
  import Cover from "../lib/ui/Cover.svelte";
  import LoadingSpinner from "../lib/ui/LoadingSpinner.svelte";

  let {
    snap,
    lyrics = null,
    lyricsLoading = false,
    stops = [],
    open = true,
    onclose = () => {},
    onseek = (_ms: number) => {},
    onopenArtist,
  }: {
    snap: Snapshot;
    lyrics?: Lyrics | null;
    lyricsLoading?: boolean;
    stops?: string[];
    open?: boolean;
    onclose?: () => void;
    onseek?: (ms: number) => void;
    onopenArtist?: (path: string, artist: string, artistId: string) => void;
  } = $props();

  const queue = $derived(snap.queue ?? []);
  const duration = $derived(snap.duration > 0 ? snap.duration : 0);
  const position = $derived(snap.position);
  const remaining = $derived(duration > 0 ? Math.max(0, duration - position) : 0);

  // Hold the current cover across transient empty-metadata frames during track
  // switches; clear once idle/stopped or a loaded track genuinely has no cover.
  let heldCoverId = $state("");
  $effect.pre(() => {
    if (snap.coverId !== "") {
      heldCoverId = snap.coverId;
      return;
    }
    if (snap.path === "" || snap.title !== "") {
      heldCoverId = "";
    }
  });

  const background = $derived(
    stops.length >= 2
      ? `linear-gradient(180deg, ${stops[0]} 11.66%, ${stops[1]} 75.95%)`
      : "linear-gradient(180deg, #3a3a3e 11.66%, #1d1d20 75.95%)",
  );

  const lines = $derived(lyrics?.lines ?? []);
  const providerCredit = $derived(lyricProviderInfo(lyrics?.provider));
  const syncLabel = $derived(lyrics ? lyricSyncLabel(lines, lyrics.synced) : null);

  // Hold the loading state long enough for the player to park on the left and
  // the spinner to enter cleanly, then fade the spinner out before mounting
  // lyrics or moving the player to center.
  const SPINNER_MIN_MS = 480;
  const SPINNER_FADE_MS = 300;
  let spinnerMounted = false;
  let spinnerStartedAt = 0;
  let spinnerHolding = $state(false);
  let spinnerLeaving = $state(false);
  let spinnerHoldTimer: ReturnType<typeof setTimeout> | undefined;
  let spinnerTimer: ReturnType<typeof setTimeout> | undefined;
  const showSpinner = $derived(lyricsLoading || spinnerHolding || spinnerLeaving);

  $effect.pre(() => {
    clearTimeout(spinnerHoldTimer);
    clearTimeout(spinnerTimer);
    if (lyricsLoading) {
      if (!spinnerMounted) {
        spinnerStartedAt = performance.now();
      }
      spinnerMounted = true;
      spinnerHolding = false;
      spinnerLeaving = false;
      return;
    }
    if (!spinnerMounted) {
      return;
    }
    const hold = Math.max(0, SPINNER_MIN_MS - (performance.now() - spinnerStartedAt));
    if (hold > 0) {
      spinnerHolding = true;
      spinnerHoldTimer = setTimeout(() => {
        spinnerHolding = false;
        spinnerLeaving = true;
        spinnerTimer = setTimeout(() => {
          spinnerLeaving = false;
          spinnerMounted = false;
        }, SPINNER_FADE_MS);
      }, hold);
      return;
    }
    spinnerLeaving = true;
    spinnerTimer = setTimeout(() => {
      spinnerLeaving = false;
      spinnerMounted = false;
    }, SPINNER_FADE_MS);
  });

  const lyricOffsetMs = $derived(
    lyricOffsetFor(lyrics?.provider, $appSettings.playback.lyrics.petitLyricsDelayMs),
  );

  // Cap word-sweep DOM writes to ~30 fps and sleep rAF between words/lines so
  // high-refresh displays do not repaint idle frames or line-synced tracks.
  const SWEEP_INTERVAL_MS = 33;

  type WordEntry = {
    el: HTMLSpanElement;
    startMs: number;
    endMs: number;
    rtl: boolean;
    lastOpacity: string;
    lastMaskPos: string;
  };

  let active = $state(0);
  let clockTime = $state(0);
  let clockBase = 0;
  let clockAt = 0;
  let clockPath = "";
  let clockRaf: number | undefined;
  let clockTimer: ReturnType<typeof setTimeout> | undefined;
  let lastWordPaint = 0;
  const activeWords = new Set<WordEntry>();

  const clockNow = (): number =>
    snap.state === "playing" ? clockBase + (performance.now() - clockAt) : clockBase;

  function applyWordStyle(entry: WordEntry, t: number, quality: boolean) {
    if (quality) {
      if (entry.lastOpacity !== "") {
        entry.lastOpacity = "";
        entry.el.style.opacity = "";
      }
      const pos = wordSweepBackgroundPosition(t, entry.startMs, entry.endMs, entry.rtl);
      if (pos !== entry.lastMaskPos) {
        entry.lastMaskPos = pos;
        entry.el.style.maskPosition = pos;
        entry.el.style.webkitMaskPosition = pos;
      }
      return;
    }

    if (entry.lastMaskPos !== "") {
      entry.lastMaskPos = "";
      entry.el.style.maskPosition = "";
      entry.el.style.webkitMaskPosition = "";
    }
    const op = wordOpacity(t, entry.startMs, entry.endMs);
    if (op !== entry.lastOpacity) {
      entry.lastOpacity = op;
      entry.el.style.opacity = op;
    }
  }

  function paintWords(t: number) {
    const quality = qualityKaraoke;
    for (const entry of activeWords) {
      applyWordStyle(entry, t, quality);
    }
  }

  function trackWord(el: HTMLSpanElement, word: LyricWord) {
    const entry: WordEntry = {
      el,
      startMs: word.startMs,
      endMs: word.endMs,
      rtl: isRTLText(word.text),
      lastOpacity: "",
      lastMaskPos: "",
    };
    activeWords.add(entry);
    applyWordStyle(entry, clockNow() - lyricOffsetMs, qualityKaraoke);

    return {
      update(next: LyricWord) {
        entry.startMs = next.startMs;
        entry.endMs = next.endMs;
        entry.rtl = isRTLText(next.text);
        entry.lastOpacity = "";
        entry.lastMaskPos = "";
        applyWordStyle(entry, clockNow() - lyricOffsetMs, qualityKaraoke);
      },
      destroy() {
        activeWords.delete(entry);
      },
    };
  }

  function stopClock() {
    if (clockRaf !== undefined) {
      cancelAnimationFrame(clockRaf);
      clockRaf = undefined;
    }
    if (clockTimer !== undefined) {
      clearTimeout(clockTimer);
      clockTimer = undefined;
    }
  }

  function syncClock(holdJitter: boolean, forcePaint: boolean): { t: number; idx: number } {
    const t = clockNow() - lyricOffsetMs;
    const idx = activeLineAt(lines, t, active, holdJitter ? JITTER_HOLD_MS : 0);
    if (idx !== active) {
      active = idx;
    }
    if (qualityKaraoke && lines[idx]?.text === instrumentalMark) {
      clockTime = t;
    }
    if (forcePaint) {
      paintWords(t);
      lastWordPaint = performance.now();
    }

    return { t, idx };
  }

  function scheduleClock(t: number, idx: number) {
    stopClock();
    if (!open || snap.state !== "playing") {
      return;
    }
    let wake = nextClockWakeMs(lines, idx, t);
    if (wake === null) {
      return;
    }
    if (wake > 0 && qualityKaraoke && lines[idx]?.text === instrumentalMark) {
      const cur = lines[idx];
      const gapEnd = lines.slice(idx + 1).find((l) => l.timeMs >= 0 && l.text.trim() !== instrumentalMark && l.text.trim() !== "")?.timeMs ?? -1;
      if (gapEnd > cur.timeMs) {
        const span = gapEnd - cur.timeMs;
        for (const threshold of [0.05, 0.33, 0.66]) {
          const at = cur.timeMs + span * threshold;
          if (at > t) {
            wake = Math.min(wake, at - t);
            break;
          }
        }
      }
    }
    if (wake === 0) {
      clockRaf = requestAnimationFrame(stepClock);
      return;
    }
    clockTimer = setTimeout(() => {
      clockTimer = undefined;
      const next = syncClock(true, true);
      scheduleClock(next.t, next.idx);
    }, Math.ceil(wake));
  }

  function stepClock(now: number) {
    clockRaf = undefined;
    const shouldPaint = now - lastWordPaint >= SWEEP_INTERVAL_MS;
    const { t, idx } = syncClock(true, shouldPaint);
    const wake = nextClockWakeMs(lines, idx, t);
    if (wake !== 0 && !shouldPaint) {
      // Snap finished words before sleeping rAF.
      paintWords(t);
      lastWordPaint = now;
    }
    scheduleClock(t, idx);
  }

  // Re-anchor only on snapshot updates; tracking active here would stamp a stale position.
  $effect(() => {
    const pos = snap.position;
    const sameTrack = snap.path === clockPath;
    const playing = sameTrack && snap.state === "playing";
    const now = performance.now();
    const prevNow = playing ? clockBase + (now - clockAt) : clockBase;
    clockPath = snap.path;
    clockBase = reanchorClock(prevNow, pos, sameTrack, playing);
    clockAt = now;
    untrack(() => {
      const forcePaint = !playing || clockRaf === undefined;
      const { t, idx } = syncClock(playing, forcePaint);
      if (clockRaf === undefined) {
        scheduleClock(t, idx);
      }
    });
  });

  // Sync active and words when lyrics mount/reload, the spinner clears, or settings change.
  $effect(() => {
    void lines;
    void lyricOffsetMs;
    void showSpinner;
    void qualityKaraoke;
    untrack(() => {
      const { t, idx } = syncClock(false, true);
      scheduleClock(t, idx);
    });
  });

  $effect(() => {
    stopClock();
    if (!open || snap.state !== "playing") {
      untrack(() => syncClock(false, true));
      return;
    }
    untrack(() => {
      const { t, idx } = syncClock(false, true);
      scheduleClock(t, idx);
    });
    return stopClock;
  });

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
  const qualityKaraoke = $derived($appSettings.playback.lyrics.karaokeStyle === "quality");
  const instrumentalProgress = $derived(
    qualityKaraoke && lines[active]?.text === instrumentalMark
      ? instrumentalNoteProgress(lines, active, clockTime, instrumentalMark)
      : 0,
  );

  const visualizerStyle = $derived($appSettings.general.visualizerStyle ?? "minimal");
  const visualizerFull = $derived($spectrumConfig.enabled && visualizerStyle === "full");
  const centerPlayer = $derived(
    $appSettings.playback.noLyricsBehaviour === "center" && !showSpinner && lines.length === 0,
  );
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

  // The idle timer, the glide, the spinner fade and the sweep clock must not outlive the pane.
  $effect(() => {
    return () => {
      clearTimeout(idleTimer);
      clearTimeout(spinnerHoldTimer);
      clearTimeout(spinnerTimer);
      stopGlide();
      stopClock();
    };
  });

  // Auto-follow: scroll the active line to centre whenever the song moves and
  // the user is not browsing. Clicks are handled by their own handler, which
  // seeks first, so the line the user chose has become the active one.
  $effect(() => {
    if (userScrolling || showSpinner) return;
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
            class:lyric-word-quality={qualityKaraoke}
            class:lyric-word-rtl={qualityKaraoke && isRTLText(segment.text)}
            use:trackWord={segment.word}
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

  <div
    class="player-stage absolute top-[18%] w-[28.2%] min-w-[240px] max-w-[333px] {centerPlayer ? 'left-1/2 -translate-x-1/2' : 'left-[12%] translate-x-0'}"
  >
    <Cover id={heldCoverId} alt={snap.album} size={333} radius={16} fill class="w-full" />

    <Marquee
      text={snap.title || "Nothing playing"}
      class="mt-[11px] font-display text-[20px] font-[600] leading-tight"
    />
    {#if onopenArtist && snap.path.startsWith("ytm:") && snap.artist}
      <button
        type="button"
        class="max-w-full cursor-pointer truncate rounded-sm text-left text-[12px] leading-tight text-fg/56 transition-colors hover:text-fg hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        onclick={() => onopenArtist(snap.path, snap.artist, snap.artistId)}
      >
        {snap.artist}
      </button>
    {:else}
      <p class="truncate text-[12px] leading-tight text-fg/56">{snap.artist}</p>
    {/if}

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
          <Play size="32" weight="fill" class="-translate-x-[1.5px]" />
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

  <div
    class="absolute inset-y-0 left-1/2 right-0 pr-9 transition-opacity duration-300 ease-out {centerPlayer ? 'pointer-events-none opacity-0' : 'opacity-100'}"
    aria-hidden={centerPlayer}
  >
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
      {#if showSpinner}
        <div
          class="flex h-full items-center justify-center transition-opacity duration-[180ms] ease-in-out"
          class:opacity-0={spinnerLeaving}
          class:opacity-100={!spinnerLeaving}
          in:fade={{ duration: 240, delay: 120 }}
        >
          <LoadingSpinner label="Fetching the lyrics for you..." />
        </div>
      {:else if !centerPlayer && lines.length === 0}
        <div
          class="flex h-full items-center pl-2 text-[12px] text-fg/50"
          in:fade={{ duration: SPINNER_FADE_MS }}
        >
          No lyrics for this track.
        </div>
      {:else if lines.length > 0}
        <div
          class="flex flex-col gap-[22px] py-[calc(50vh-22px)] pl-2"
          in:fade={{ duration: SPINNER_FADE_MS }}
        >
          {#each lines as line, i (line.timeMs + ":" + i)}
            {@const tiers = lyricTiers(line)}
            <div
              bind:this={pairEls[i]}
              class="flex flex-col gap-[2px]"
              class:lyric-line-active={i === active}
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
                    onseek(seekTargetMs(line.timeMs, lyricOffsetMs));
                    endUserScroll();
                    const el = pairEls[i];
                    if (el && el.isConnected) centerOn(el);
                  }}
                >
                  {#if line.text === instrumentalMark}
                    <span class="inline-flex items-center gap-[2px]" aria-hidden="true">
                      {#if qualityKaraoke && i === active}
                        {#each [0.05, 0.33, 0.66] as threshold (threshold)}
                          <span
                            class="inline-flex transition-[opacity,transform] duration-300 ease-out"
                            class:opacity-100={instrumentalProgress >= threshold}
                            class:scale-100={instrumentalProgress >= threshold}
                            class:opacity-35={instrumentalProgress < threshold}
                            class:scale-75={instrumentalProgress < threshold}
                          >
                            <MusicNote size={28} weight="fill" />
                          </span>
                        {/each}
                      {:else}
                        <MusicNote size={28} weight="fill" />
                        <MusicNote size={28} weight="fill" />
                        <MusicNote size={28} weight="fill" />
                      {/if}
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
          {#if providerCredit}
            <p class="mt-6 text-center text-[13px] text-fg/50">
              Lyrics provided by
              <a
                href={providerCredit.url}
                class="underline decoration-fg/30 underline-offset-2 transition-colors hover:text-fg/85 hover:decoration-fg/70 focus-visible:rounded-sm focus-visible:text-fg/85 focus-visible:outline focus-visible:outline-1 focus-visible:outline-fg/60"
                onclick={(e) => {
                  e.preventDefault();
                  void Browser.OpenURL(providerCredit.url);
                }}
              >{providerCredit.name}</a>{#if syncLabel} ({syncLabel}){/if}
            </p>
          {/if}
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
      <div
        class="player-stage pointer-events-none absolute bottom-0 w-[calc(50%-1.5rem)] {centerPlayer ? 'left-1/2 -translate-x-1/2' : 'left-0 translate-x-0'}"
      >
        <Spectrum height={72} opacity={0.5} chrome={false} />
      </div>
    {/if}
  {/if}
</div>

<style>
  .player-stage {
    transition:
      left 420ms cubic-bezier(0.22, 1, 0.36, 1),
      transform 420ms cubic-bezier(0.22, 1, 0.36, 1);
  }

  @media (prefers-reduced-motion: reduce) {
    .player-stage {
      transition: none;
    }
  }

  .lyric-word {
    opacity: 0.3;
  }

  .lyric-word-quality {
    opacity: 1;
    -webkit-mask-image: linear-gradient(
      to right,
      #000,
      #000 45%,
      rgb(0 0 0 / 0.3) 55%,
      rgb(0 0 0 / 0.3)
    );
    mask-image: linear-gradient(
      to right,
      #000,
      #000 45%,
      rgb(0 0 0 / 0.3) 55%,
      rgb(0 0 0 / 0.3)
    );
    -webkit-mask-size: 225% 100%;
    mask-size: 225% 100%;
    -webkit-mask-position: 100% 0;
    mask-position: 100% 0;
    -webkit-mask-repeat: no-repeat;
    mask-repeat: no-repeat;
  }

  .lyric-word-rtl {
    -webkit-mask-image: linear-gradient(
      to left,
      #000,
      #000 45%,
      rgb(0 0 0 / 0.3) 55%,
      rgb(0 0 0 / 0.3)
    );
    mask-image: linear-gradient(
      to left,
      #000,
      #000 45%,
      rgb(0 0 0 / 0.3) 55%,
      rgb(0 0 0 / 0.3)
    );
    -webkit-mask-position: 0% 0;
    mask-position: 0% 0;
  }

  /* Isolate only the active line onto its own layer so word-sweep repaints do
   * not invalidate the entire multi-thousand-pixel lyrics scroll layer. */
  .lyric-line-active {
    transform: translateZ(0);
    contain: layout paint;
  }
</style>
