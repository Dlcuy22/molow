import { describe, expect, it } from "vitest";
import { ParamKind, type EffectParam, type EffectStage } from "../../effect-types";
import {
  EQ_IMPL,
  eqActiveBands,
  eqBands,
  eqPresetMatches,
  faderFill,
  formatGainDB,
  formatHz,
  formatQ,
  parseScrubText,
  plainNumber,
  scrubValue,
  SCRUB_PIXELS,
  stepValue,
} from "./eq-helpers";

function param(over: Partial<EffectParam>): EffectParam {
  return {
    key: "k",
    kind: ParamKind.Float,
    min: 0,
    max: 1,
    step: 0.1,
    unit: "",
    options: null,
    default: 0,
    label: "",
    group: "",
    widget: "",
    ...over,
  };
}

function eqSchema(): EffectParam[] {
  return [
    param({ key: "num-bands", kind: ParamKind.Int, min: 1, max: 10, step: 1, default: 10 }),
    ...Array.from({ length: 10 }, (_, i) => [
      param({ key: `band${i}-frequency`, min: 20, max: 20000, step: 1, unit: "Hz" }),
      param({ key: `band${i}-gain`, min: -20, max: 20, step: 0.1, unit: "dB" }),
      param({ key: `band${i}-q`, min: 0.1, max: 10, step: 0.01 }),
    ]).flat(),
  ];
}

function stage(over: Partial<EffectStage> = {}): EffectStage {
  return {
    id: "eq",
    kind: "equalizer",
    impl: EQ_IMPL,
    label: "Equalizer",
    bypassed: false,
    schema: eqSchema(),
    values: {},
    meters: null,
    readings: [],
    visuals: [],
    ...over,
  };
}

describe("eqBands", () => {
  it("reads ten bands in index order with all three params", () => {
    const bands = eqBands(stage());
    expect(bands).toHaveLength(10);
    expect(bands[0].index).toBe(0);
    expect(bands[0].frequency.key).toBe("band0-frequency");
    expect(bands[0].gain.key).toBe("band0-gain");
    expect(bands[0].q.key).toBe("band0-q");
    expect(bands[9].frequency.key).toBe("band9-frequency");
  });

  it("stops at the first incomplete band instead of throwing", () => {
    const schema = eqSchema().filter((p) => p.key !== "band3-q");
    expect(eqBands(stage({ schema }))).toHaveLength(3);
  });

  it("is empty when the schema declares no bands", () => {
    expect(eqBands(stage({ schema: [] }))).toEqual([]);
  });
});

describe("eqActiveBands", () => {
  const bands = eqBands(stage());

  it("defaults to every band when there is no count param", () => {
    expect(eqActiveBands(stage({ schema: eqSchema().slice(1) }), bands)).toBe(10);
  });

  it("reads the count and clamps it to the declared bands", () => {
    expect(eqActiveBands(stage({ values: { "num-bands": 3 } }), bands)).toBe(3);
    expect(eqActiveBands(stage({ values: { "num-bands": 99 } }), bands)).toBe(10);
    expect(eqActiveBands(stage({ values: { "num-bands": 0 } }), bands)).toBe(0);
  });
});

describe("formatHz", () => {
  it("keeps whole hertz below 1 kHz", () => {
    expect(formatHz(32)).toBe("32 Hz");
    expect(formatHz(125.4)).toBe("125 Hz");
    expect(formatHz(500)).toBe("500 Hz");
  });

  it("switches to one decimal in kHz and keeps a stable width", () => {
    expect(formatHz(1000)).toBe("1.0 kHz");
    expect(formatHz(2000)).toBe("2.0 kHz");
    expect(formatHz(16000)).toBe("16.0 kHz");
    expect(formatHz(20000)).toBe("20.0 kHz");
  });

  it("treats a value that rounds up to 1 kHz as kHz", () => {
    expect(formatHz(999.6)).toBe("1.0 kHz");
    expect(formatHz(999.4)).toBe("999 Hz");
  });

  it("is honest about a non-finite value", () => {
    expect(formatHz(Number.NaN)).toBe("--");
  });
});

describe("formatQ and formatGainDB", () => {
  it("renders Q with two decimals", () => {
    expect(formatQ(1.504760237537245)).toBe("1.50");
    expect(formatQ(0.1)).toBe("0.10");
  });

  it("signs a boost but not a cut or zero", () => {
    expect(formatGainDB(4)).toBe("+4.0 dB");
    expect(formatGainDB(-2.5)).toBe("-2.5 dB");
    expect(formatGainDB(0)).toBe("0.0 dB");
  });

  it("does not let float residue turn zero into a signed value", () => {
    expect(formatGainDB(-0.001)).toBe("0.0 dB");
  });
});

describe("plainNumber", () => {
  it("drops the unit and the float residue an edit start should not carry", () => {
    expect(plainNumber(32)).toBe("32");
    expect(plainNumber(1.504760237537245)).toBe("1.5");
    expect(plainNumber(-0.0001)).toBe("0");
  });
});

describe("parseScrubText", () => {
  it("reads a plain number, with a unit or with whitespace around it", () => {
    expect(parseScrubText("32")).toBe(32);
    expect(parseScrubText(" 125 Hz ")).toBe(125);
    expect(parseScrubText("-2.5 dB")).toBe(-2.5);
    expect(parseScrubText("+4")).toBe(4);
  });

  it("accepts a comma decimal separator", () => {
    expect(parseScrubText("1,5")).toBe(1.5);
    expect(parseScrubText("-2,25")).toBe(-2.25);
  });

  it("expands a k suffix, which is how the field displays kHz", () => {
    expect(parseScrubText("1.5k")).toBe(1500);
    expect(parseScrubText("1,5 kHz")).toBe(1500);
  });

  it("is null when there is nothing numeric to read", () => {
    expect(parseScrubText("")).toBeNull();
    expect(parseScrubText("   ")).toBeNull();
    expect(parseScrubText("abc")).toBeNull();
  });
});

describe("scrubValue", () => {
  const gain = { min: -20, max: 20, step: 0.1 };

  it("moves linearly across the full range in one travel", () => {
    expect(scrubValue(0, SCRUB_PIXELS, gain)).toBe(20);
    expect(scrubValue(0, -SCRUB_PIXELS, gain)).toBe(-20);
    expect(scrubValue(0, SCRUB_PIXELS / 4, gain)).toBeCloseTo(10, 5);
  });

  it("is five times finer with the fine gesture", () => {
    const normal = scrubValue(0, 100, gain);
    const fine = scrubValue(0, 100, gain, true);
    expect(fine).toBeCloseTo(normal / 5, 0);
  });

  it("clamps instead of running past the ends", () => {
    expect(scrubValue(0, 5000, gain)).toBe(20);
    expect(scrubValue(0, -5000, gain)).toBe(-20);
  });

  it("snaps to the step so the engine never sees float residue", () => {
    expect(scrubValue(0, 10, { min: -20, max: 20, step: 0.1 })).toBe(1.3);
  });

  it("sweeps a log field proportionally, not by fixed hertz", () => {
    const freq = { min: 20, max: 20000, step: 1, log: true };
    const once = scrubValue(1000, 32, freq);
    const twice = scrubValue(1000, 64, freq);
    // The same gesture covers the same number of octaves wherever it starts.
    expect(once / 1000).toBeCloseTo(2, 0);
    expect(twice / 1000).toBeCloseTo(4, 0);
    expect(scrubValue(20000, 5000, freq)).toBe(20000);
    expect(scrubValue(20, -5000, freq)).toBe(20);
  });
});

describe("stepValue", () => {
  const gain = { min: -20, max: 20, step: 0.1 };
  const freq = { min: 20, max: 20000, step: 1, log: true };

  it("nudges a linear field by one step, ten for Page", () => {
    expect(stepValue(0, 1, gain)).toBeCloseTo(0.1, 5);
    expect(stepValue(0, 1, gain, { big: true })).toBeCloseTo(1, 5);
    expect(stepValue(0, -1, gain)).toBeCloseTo(-0.1, 5);
  });

  it("steps a log field proportionally", () => {
    expect(stepValue(1000, 1, freq)).toBeCloseTo(1050, 0);
    expect(stepValue(1000, 1, freq, { big: true })).toBeCloseTo(2000, 0);
    expect(stepValue(20000, 1, freq)).toBe(20000);
  });

  it("stays inside the range", () => {
    expect(stepValue(20, 1, gain)).toBe(20);
    expect(stepValue(-20, -1, gain)).toBe(-20);
  });
});

describe("faderFill", () => {
  it("grows a boost up from the 0 dB baseline", () => {
    expect(faderFill(10, -20, 20)).toEqual({ lo: 0.5, hi: 0.75, zero: 0.5 });
  });

  it("grows a cut down from the same baseline", () => {
    expect(faderFill(-5, -20, 20)).toEqual({ lo: 0.375, hi: 0.5, zero: 0.5 });
  });

  it("is a single point at 0 dB", () => {
    expect(faderFill(0, -20, 20).lo).toBe(faderFill(0, -20, 20).hi);
  });

  it("uses the whole track when the range does not cross zero", () => {
    expect(faderFill(5, 0, 10)).toEqual({ lo: 0, hi: 0.5, zero: 0 });
  });
});

describe("eqPresetMatches", () => {
  const values = {
    "num-bands": 10,
    "band0-frequency": 32,
    "band0-gain": 4,
    "band0-q": 1.5,
  };

  it("matches when every key the preset carries is held", () => {
    expect(eqPresetMatches(stage({ values: { ...values } }), values)).toBe(true);
  });

  it("is false when one value differs", () => {
    expect(
      eqPresetMatches(stage({ values: { ...values, "band0-gain": 5 } }), values),
    ).toBe(false);
  });

  it("ignores stage utilities the preset does not carry", () => {
    const s = stage({
      values: { ...values, bypass: true, "input-gain": -6, "output-gain": 3 },
    });
    expect(eqPresetMatches(s, values)).toBe(true);
  });

  it("is false when the stage is missing a key", () => {
    const partial: Record<string, unknown> = { ...values };
    delete partial["band0-q"];
    expect(eqPresetMatches(stage({ values: partial }), values)).toBe(false);
  });

  it("is false for an empty preset", () => {
    expect(eqPresetMatches(stage({ values }), {})).toBe(false);
  });
});
