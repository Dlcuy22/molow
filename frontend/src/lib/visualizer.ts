// The album player's two layouts draw the spectrum at different widths, so
// they do not want the same number of points. The full sheet spans the whole
// window and gets the densest curve the schema offers; the compact strip keeps
// the shipped default.

/** The point count the full-width sheet draws. */
export const FULL_VISUALIZER_BARS = 256;

/** The point count the compact strip draws. */
export const DEFAULT_VISUALIZER_BARS = 150;

/**
 * visualizerBars maps a layout name to the point count it draws. Anything that
 * is not "full" keeps the default, including an absent value from a document
 * written before the style field existed.
 */
export function visualizerBars(style: string | undefined): number {
  return style === "full" ? FULL_VISUALIZER_BARS : DEFAULT_VISUALIZER_BARS;
}
