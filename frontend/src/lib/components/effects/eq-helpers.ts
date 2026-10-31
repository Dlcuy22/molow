// Pure helpers for the RBJ equalizer panel: how its ten bands are read out of
// the schema, how their three values are formatted and parsed, and the drag and
// fill math the scrub fields and faders share. They live here so the behaviour
// is testable without a DOM and the panel stays a thin renderer.

import { fillFrac } from "../../format";
import type { EffectParam, EffectStage } from "../../effect-types";
import { clamp, numericValue, paramValue, roundToStep } from "./effects-helpers";

/** EQ_IMPL is the implementation name the panel is selected by. */
export const EQ_IMPL = "equalizer-rbj";

/** EQ_NUM_BANDS is the key of the band-count parameter. */
export const EQ_NUM_BANDS = "num-bands";

/** SCRUB_PIXELS is the pointer travel that covers the whole value range. A
 *  smaller number makes a drag more sensitive; this one asks for about a panel
 *  width for a full sweep. */
export const SCRUB_PIXELS = 320;

/** SCRUB_THRESHOLD_PX is how far a press must move before it counts as a drag
 *  instead of a click. Below it, the click opens the field for typing. */
export const SCRUB_THRESHOLD_PX = 3;

/** EqBand is one band's three parameters, keyed by nothing but its index. */
export interface EqBand {
  index: number;
  frequency: EffectParam;
  gain: EffectParam;
  q: EffectParam;
}

/** ScrubRange is the value span a scrub field moves through. */
export interface ScrubRange {
  min: number;
  max: number;
  step: number;
  /** log sweeps proportionally, which a frequency needs: one step at 20 Hz is
   *  the same visual distance as one step at 16 kHz. */
  log?: boolean;
}

function bandKey(index: number, suffix: string): string {
  return `band${index}-${suffix}`;
}

/** eqBands reads the ten band groups out of a stage schema in index order. It
 *  stops at the first incomplete band, so a partial schema renders what it
 *  actually declares instead of throwing on a missing pointer. */
export function eqBands(stage: EffectStage): EqBand[] {
  const byKey = new Map(stage.schema.map((p) => [p.key, p]));
  const out: EqBand[] = [];
  for (let index = 0; ; index += 1) {
    const frequency = byKey.get(bandKey(index, "frequency"));
    const gain = byKey.get(bandKey(index, "gain"));
    const q = byKey.get(bandKey(index, "q"));
    if (!frequency || !gain || !q) {
      break;
    }
    out.push({ index, frequency, gain, q });
  }

  return out;
}

/** eqNumBandsParam is the band-count control, or null on a schema that has
 *  none. */
export function eqNumBandsParam(stage: EffectStage): EffectParam | null {
  return stage.schema.find((p) => p.key === EQ_NUM_BANDS) ?? null;
}

/** eqActiveBands is how many bands the engine processes, clamped to the bands
 *  actually declared and defaulted to all of them when the count is missing. */
export function eqActiveBands(stage: EffectStage, bands: EqBand[]): number {
  const param = eqNumBandsParam(stage);
  if (!param) {
    return bands.length;
  }
  const count = Math.round(numericValue(paramValue(stage, param), bands.length));

  return clamp(count, 0, bands.length);
}

/** formatHz renders a frequency the way a band label reads: whole hertz below
 *  1 kHz, one decimal in kHz above it. The kHz form keeps the same width up to
 *  20 kHz, which is what keeps ten columns aligned. */
export function hzParts(v: number): { value: string; unit: string } {
  if (!Number.isFinite(v)) {
    return { value: "--", unit: "" };
  }
  const hz = Math.round(v);
  if (hz >= 1000) {
    return { value: (v / 1000).toFixed(1), unit: "kHz" };
  }

  return { value: String(hz), unit: "Hz" };
}

/** formatHz joins the parts for text that has to read as one value, such as an
 *  accessible name. */
export function formatHz(v: number): string {
  const { value, unit } = hzParts(v);

  return unit === "" ? value : `${value} ${unit}`;
}

/** formatQ renders a Q with two decimals, the precision its 0.01 step has. */
export function formatQ(v: number): string {
  return Number.isFinite(v) ? v.toFixed(2) : "--";
}

/** gainParts renders a gain with an explicit sign on boosts, so a cut and a
 *  boost are distinguishable at a glance instead of by comparing digits. One
 *  decimal is the resolution of the 0.1 dB step, which is all the readout needs
 *  to show. */
export function gainParts(v: number): { value: string; unit: string } {
  if (!Number.isFinite(v)) {
    return { value: "--", unit: "" };
  }
  const rounded = Number(v.toFixed(1));
  const sign = rounded > 0 ? "+" : "";

  return { value: `${sign}${rounded.toFixed(1)}`, unit: "dB" };
}

/** formatGainDB joins the parts for text that has to read as one value. */
export function formatGainDB(v: number): string {
  const { value, unit } = gainParts(v);

  return unit === "" ? value : `${value} ${unit}`;
}

/** plainNumber is the text an edit starts from: the value without a unit and
 *  without float residue. */
export function plainNumber(v: number): string {
  if (!Number.isFinite(v)) {
    return "0";
  }
  const rounded = Number(v.toFixed(2));

  return String(rounded === 0 ? 0 : rounded);
}

/**
 * parseScrubText reads what a user typed into a scrub field. It accepts a comma
 * decimal separator, any surrounding whitespace, the unit the field displays
 * and a k suffix, because "1.5 kHz" is what the field itself shows. Anything
 * with no number in it is null, which the field treats as "keep the old value".
 */
export function parseScrubText(text: string): number | null {
  const normalized = text.trim().toLowerCase().replace(",", ".");
  if (normalized === "") {
    return null;
  }
  const match = normalized.match(/[-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[-+]?\d+)?/);
  if (!match) {
    return null;
  }
  const n = Number(match[0]);
  if (!Number.isFinite(n)) {
    return null;
  }

  return normalized.includes("k") ? n * 1000 : n;
}

/** scrubValue turns a pointer delta into a value. Linear fields move by range
 *  over the shared travel; log fields double every SCRUB_PIXELS / octaves
 *  pixels. `fine` (Shift) slows the same gesture down. The result is clamped and
 *  snapped, so the engine never sees a value the control could not show. */
export function scrubValue(
  start: number,
  dx: number,
  range: ScrubRange,
  fine = false,
): number {
  const scale = fine ? 0.2 : 1;
  let next: number;
  if (range.log && start > 0 && range.min > 0 && range.max > range.min) {
    const octaves = Math.log2(range.max / range.min);
    next = start * Math.pow(2, (dx / SCRUB_PIXELS) * octaves * scale);
  } else {
    const perPixel = Math.max(
      (range.max - range.min) / SCRUB_PIXELS,
      range.step > 0 ? range.step / 8 : 0,
    );
    next = start + dx * perPixel * scale;
  }

  return clamp(roundToStep(next, range.step), range.min, range.max);
}

/** stepValue is the keyboard path: one step, ten steps for Page, and a
 *  proportional nudge on a log field, where a fixed increment would be
 *  invisible at the top of the range. */
export function stepValue(
  value: number,
  direction: number,
  range: ScrubRange,
  opts: { big?: boolean; fine?: boolean } = {},
): number {
  if (range.log && value > 0 && range.min > 0) {
    const factor = opts.big ? 2 : 1.05;
    const scaled = opts.fine ? 1 + (factor - 1) / 5 : factor;
    const next = value * (direction > 0 ? scaled : 1 / scaled);

    return clamp(roundToStep(next, range.step), range.min, range.max);
  }
  const step = range.step > 0 ? range.step : (range.max - range.min) / 100;
  const mult = (opts.big ? 10 : 1) * (opts.fine ? 0.2 : 1);
  const next = value + direction * step * mult;

  return clamp(roundToStep(next, range.step), range.min, range.max);
}

/** FaderFill is the painted span of a bipolar gain track, as 0..1 fractions
 *  from the bottom. `zero` is where 0 dB sits, so the component can draw the
 *  baseline the fill grows away from. */
export interface FaderFill {
  lo: number;
  hi: number;
  zero: number;
}

/** faderFill paints a gain from 0 dB to the value instead of from the bottom of
 *  the range, which is what makes a boost and a cut read as opposite gestures
 *  around the same line. */
export function faderFill(value: number, min: number, max: number): FaderFill {
  const zero = clamp(fillFrac(0, min, max), 0, 1);
  const at = fillFrac(value, min, max);

  return { lo: Math.min(at, zero), hi: Math.max(at, zero), zero };
}

/**
 * eqPresetMatches reports whether a stage still holds the values a preset was
 * applied with, so the menu can mark it as edited. Only the keys the preset
 * carries are compared: bypass and the input/output gains are stage utilities,
 * not part of a preset, and a change to them must not mark the curve dirty.
 */
export function eqPresetMatches(
  stage: EffectStage,
  values: Record<string, unknown>,
): boolean {
  const keys = Object.keys(values);
  if (keys.length === 0) {
    return false;
  }
  for (const key of keys) {
    const want = values[key];
    const have = stage.values[key];
    if (typeof want === "number" && typeof have === "number") {
      if (Math.abs(want - have) > 1e-6) {
        return false;
      }
    } else if (want !== have) {
      return false;
    }
  }

  return true;
}
