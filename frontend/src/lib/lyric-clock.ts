export const JITTER_HOLD_MS = 80;

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
