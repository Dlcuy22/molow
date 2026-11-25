export const JITTER_HOLD_MS = 80;
export const CLOCK_JITTER_HOLD_MS = 250;

/**
 * reanchorClock picks the new clockBase when a backend snapshot arrives.
 * During continuous playback on the same track, a snapshot that lands slightly
 * behind the free-running clock (IPC / audio-buffer latency) holds prevNow
 * instead of rewinding the sweep backward and flashing short syllables. A real
 * backward seek (> CLOCK_JITTER_HOLD_MS) or track/state change snaps directly.
 */
export function reanchorClock(
  prevNow: number,
  snapPos: number,
  sameTrack: boolean,
  playing: boolean,
): number {
  if (sameTrack && playing && snapPos < prevNow && prevNow - snapPos <= CLOCK_JITTER_HOLD_MS) {
    return prevNow;
  }

  return snapPos;
}

/**
 * lyricOffsetFor returns the active timing offset in milliseconds for a lyric
 * sheet. Negative values shift late lyrics earlier; positive values delay early
 * ones. Only PetitLyrics sheets use the PetitLyrics delay setting.
 */
export function lyricOffsetFor(
  provider: string | undefined,
  petitLyricsDelayMs: number | undefined,
): number {
  return provider === "petitlyrics" ? (petitLyricsDelayMs ?? 0) : 0;
}

/**
 * activeLineAt returns the index of the latest timed lyric line at or before
 * clockMs. When jitterHoldMs is positive, a tiny backward dip across a line
 * boundary (e.g. snapshot re-anchor latency) keeps prevActive instead of
 * flickering backward.
 */
export function activeLineAt(
  lines: readonly { timeMs: number }[],
  clockMs: number,
  prevActive = -1,
  jitterHoldMs = 0,
): number {
  let idx = 0;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].timeMs >= 0 && lines[i].timeMs <= clockMs) {
      idx = i;
    }
  }
  if (
    jitterHoldMs > 0 &&
    prevActive > idx &&
    prevActive < lines.length &&
    lines[prevActive].timeMs >= 0 &&
    lines[prevActive].timeMs - clockMs < jitterHoldMs
  ) {
    return prevActive;
  }

  return idx;
}

/** seekTargetMs converts a lyric line timestamp to the track seek position. */
export function seekTargetMs(lineTimeMs: number, offsetMs: number): number {
  return Math.max(0, lineTimeMs + offsetMs);
}

/**
 * wordOpacity maps lyric clock t onto the 0.3..1.0 sweep opacity of one word.
 * Returns a rounded string so callers can skip redundant DOM style writes.
 */
export function wordOpacity(t: number, startMs: number, endMs: number): string {
  if (t <= startMs) {
    return "0.3";
  }
  if (t >= endMs || endMs <= startMs) {
    return "1";
  }
  const op = 0.3 + ((t - startMs) * 0.7) / (endMs - startMs);

  return op >= 0.995 ? "1" : op.toFixed(2);
}

const RTL_RE = /[\u0591-\u07FF\u200F\u202B\u202E\uFB1D-\uFDFD\uFE70-\uFEFC]/;

/** isRTLText reports whether a word contains right-to-left script characters. */
export function isRTLText(text: string): boolean {
  return RTL_RE.test(text);
}

/**
 * wordSweepBackgroundPosition maps lyric clock t onto a CSS background-position
 * for a 225%-wide active-to-inactive text-clip gradient. LTR sweeps 100% -> 0%;
 * RTL reverses to 0% -> 100%.
 */
export function wordSweepBackgroundPosition(
  t: number,
  startMs: number,
  endMs: number,
  rtl = false,
): string {
  let progress = 0;
  if (t >= endMs || (endMs <= startMs && t > startMs)) {
    progress = 1;
  } else if (t > startMs && endMs > startMs) {
    progress = (t - startMs) / (endMs - startMs);
  }
  const pct = rtl ? progress * 100 : (1 - progress) * 100;

  return `${pct.toFixed(1)}% 0`;
}

/**
 * instrumentalNoteProgress returns 0..1 progress across an instrumental gap
 * starting at lines[active] until the next non-instrumental timed line.
 */
export function instrumentalNoteProgress(
  lines: readonly { timeMs: number; text: string }[],
  active: number,
  t: number,
  mark = "♪",
): number {
  const cur = lines[active];
  if (!cur || cur.timeMs < 0) {
    return 0;
  }
  let nextStart = -1;
  for (let i = active + 1; i < lines.length; i++) {
    if (lines[i].timeMs >= 0 && lines[i].text.trim() !== mark && lines[i].text.trim() !== "") {
      nextStart = lines[i].timeMs;
      break;
    }
  }
  if (nextStart <= cur.timeMs) {
    return 1;
  }
  if (t <= cur.timeMs) {
    return 0;
  }
  if (t >= nextStart) {
    return 1;
  }

  return (t - cur.timeMs) / (nextStart - cur.timeMs);
}

/**
 * nextClockWakeMs returns how long the lyric clock can sleep before the next
 * visual change: 0 when a word on the active line is currently sweeping, a
 * positive ms delay until the next word start or line boundary, or null at the
 * end of the sheet.
 */
export function nextClockWakeMs(
  lines: readonly { timeMs: number; words?: readonly { startMs: number; endMs: number }[] }[],
  active: number,
  t: number,
): number | null {
  let nextMs = Infinity;
  const words = lines[active]?.words;
  if (words) {
    for (let i = 0; i < words.length; i++) {
      const w = words[i];
      if (w.startMs <= t && t < w.endMs) {
        return 0;
      }
      if (w.startMs > t && w.startMs - t < nextMs) {
        nextMs = w.startMs - t;
      }
    }
  }
  for (let i = active + 1; i < lines.length; i++) {
    const timeMs = lines[i].timeMs;
    if (timeMs >= 0 && timeMs > t) {
      if (timeMs - t < nextMs) {
        nextMs = timeMs - t;
      }
      break;
    }
  }

  return Number.isFinite(nextMs) ? nextMs : null;
}

export interface LyricProviderInfo {
  name: string;
  url: string;
}

const LYRIC_PROVIDERS: Record<string, LyricProviderInfo> = {
  petitlyrics: { name: "PetitLyrics", url: "https://petitlyrics.com" },
  betterlyrics: { name: "BetterLyrics", url: "https://betterlyrics.org" },
  lyricsplus: { name: "LyricsPlus", url: "https://github.com/ibratabian17/LyricsPlus" },
  kugou: { name: "KuGou", url: "https://www.kugou.com" },
  lrclib: { name: "LrcLib", url: "https://lrclib.net" },
  ytmusic: { name: "YouTube Music", url: "https://music.youtube.com" },
};

/** lyricProviderInfo maps a backend provider key to its display name and site URL. */
export function lyricProviderInfo(provider: string | undefined): LyricProviderInfo | null {
  if (!provider) {
    return null;
  }

  return LYRIC_PROVIDERS[provider.toLowerCase()] ?? null;
}

/** lyricSyncLabel classifies a timed sheet as word-synced or line-synced. */
export function lyricSyncLabel(
  lines: readonly { timeMs: number; words?: readonly unknown[] }[],
  synced: boolean,
): "word synced" | "line synced" | null {
  if (lines.some((line) => (line.words?.length ?? 0) > 0)) {
    return "word synced";
  }
  if (synced || lines.some((line) => line.timeMs >= 0)) {
    return "line synced";
  }

  return null;
}
