import type { LyricWord } from "./types";

/** SweepSegment is one piece of a rendered line: a timed word, or the text
 *  between two words. Gap segments never highlight. */
export interface SweepSegment {
  text: string;
  word?: LyricWord;
}

/**
 * sweepSegments splits a line into what it shows and which words drive the
 * highlight. The line text owns the spacing: a provider may time each
 * character, or split one word across two entries, so rebuilding the text from
 * the word list would invent spaces. Each word is matched to its place in the
 * line, and the text between two words stays a plain gap. Null means a word
 * could not be found, and the caller should show the line text without a
 * sweep.
 */
export function sweepSegments(
  text: string,
  words: readonly LyricWord[] | undefined,
): SweepSegment[] | null {
  const source = text ?? "";
  const timed = (words ?? []).filter((word) => word.text.trim() !== "");
  if (timed.length === 0) {
    return source === "" ? [] : [{ text: source }];
  }

  const out: SweepSegment[] = [];
  let at = 0;
  for (const word of timed) {
    const idx = source.indexOf(word.text, at);
    if (idx < 0) {
      return null;
    }
    if (idx > at) {
      out.push({ text: source.slice(at, idx) });
    }
    out.push({ text: word.text, word });
    at = idx + word.text.length;
  }
  if (at < source.length) {
    out.push({ text: source.slice(at) });
  }

  return out;
}
