import { describe, expect, it, vi } from "vitest";
import { render } from "svelte/server";
import { ParamKind, type EffectParam, type EffectStage } from "../../effect-types";

// The preset menu reads the store on mount. svelte/server does not run the
// mount hook, but importing the store still touches window at module load, so
// the module is stubbed with the state and commands the panel reads.
vi.mock("../../store", async () => {
  const { writable } = await import("svelte/store");

  return {
    appliedPresets: writable({}),
    commands: {
      effectPresets: async () => [],
      saveEffectPreset: async () => null,
      applyEffectPreset: async () => false,
      deleteEffectPreset: async () => false,
      openEffectPresetFolder: async () => {},
    },
  };
});

import EqualizerPanel from "./EqualizerPanel.svelte";
import ScrubField from "./ScrubField.svelte";

// Server-rendered to HTML: the panel is a data display plus a control surface,
// and the checks here are that the band values reach the markup, that an
// inactive band is disabled, and that a partial schema does not throw. Pointer
// gestures need a DOM and are covered by the pure helpers instead.

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

function schema(count = 10): EffectParam[] {
  return [
    param({ key: "num-bands", kind: ParamKind.Int, min: 1, max: 32, step: 1, default: 10 }),
    ...Array.from({ length: count }, (_, i) => [
      param({
        key: `band${i}-frequency`,
        min: 20,
        max: 20000,
        step: 1,
        unit: "Hz",
        default: i < 10 ? 32 * Math.pow(2, i) : 100,
      }),
      param({ key: `band${i}-gain`, min: -20, max: 20, step: 0.1, unit: "dB" }),
      param({ key: `band${i}-q`, min: 0.1, max: 10, step: 0.01 }),
    ]).flat(),
  ];
}

function stage(over: Partial<EffectStage> = {}): EffectStage {
  return {
    id: "eq",
    kind: "equalizer",
    impl: "equalizer-rbj",
    label: "Equalizer",
    bypassed: false,
    schema: schema(),
    values: {
      "num-bands": 10,
      "band0-frequency": 32,
      "band0-gain": 4,
      "band0-q": 1.5,
      "band1-frequency": 1000,
      "band1-gain": -2.5,
    },
    meters: null,
    readings: [],
    visuals: [],
    ...over,
  };
}

function renderPanel(s: EffectStage): string {
  const { body } = render(EqualizerPanel, {
    props: { stage: s, onparam: () => {} },
  });

  return body;
}

describe("EqualizerPanel", () => {
  it("renders one column per band with the band's three values", () => {
    const body = renderPanel(stage());
    expect(body).toContain("Band 1");
    expect(body).toContain("Band 10");
    expect(body).toContain("32 Hz");
    expect(body).toContain("+4.0 dB");
    expect(body).toContain("Q");
    expect(body).toContain("1.50");
    expect(body).toContain("1.0 kHz");
    expect(body).toContain("-2.5 dB");
  });

  it("renders a range input for every band's gain", () => {
    const body = renderPanel(stage());
    expect(body.match(/class="fader"/g)).toHaveLength(10);
  });

  it("paints a boost and a cut around the 0 dB baseline", () => {
    const body = renderPanel(stage());
    expect(body).toContain("--lo: 0.5; --hi: 0.6;");
    expect(body).toContain("--lo: 0.4375; --hi: 0.5;");
  });

  it("renders only the active bands, plus an append cell", () => {
    const body = renderPanel(stage({ values: { "num-bands": 1, "band0-gain": 0 } }));
    expect(body).toContain("Band 1");
    expect(body).not.toContain("Band 2");
    expect(body).toContain('aria-label="Add a band at 100 Hz"');
  });

  it("renders a band past ten once the stage activates it", () => {
    const s = stage({
      schema: schema(12),
      values: { "num-bands": 12, "band11-frequency": 100 },
    });
    const body = renderPanel(s);
    expect(body).toContain("Band 12");
    expect(body).toContain("100");
    expect(body).toContain('aria-label="Band 12 frequency"');
  });

  it("renders a preset chooser beside the band count", () => {
    const body = renderPanel(stage());
    expect(body).toContain("Preset");
    expect(body).toContain("Choose");
    expect(body).toContain('aria-haspopup="true"');
  });

  it("renders a partial schema without throwing", () => {
    const s = stage({
      schema: schema().filter((p) => p.key !== "band3-q"),
      values: {},
    });
    const body = renderPanel(s);
    expect(body).toContain("Band 3");
    expect(body).not.toContain("Band 4");
  });

  it("renders an empty panel for a schema with no bands", () => {
    const body = renderPanel(stage({ schema: [], values: {} }));
    expect(body).not.toContain("Band 1");
  });
});

describe("ScrubField", () => {
  it("renders the formatted value with its unit and prefix", () => {
    const { body } = render(ScrubField, {
      props: {
        value: 1.5,
        min: 0.1,
        max: 10,
        step: 0.01,
        label: "Band 1 Q",
        text: "1.50",
        prefix: "Q",
        onchange: () => {},
      },
    });
    expect(body).toContain("Q");
    expect(body).toContain("1.50");
    expect(body).toContain('role="spinbutton"');
    expect(body).toContain('aria-valuenow="1.5"');
    expect(body).toContain('aria-valuetext="Q 1.50"');
  });

  it("renders disabled when the band is not active", () => {
    const { body } = render(ScrubField, {
      props: {
        value: 0,
        min: -20,
        max: 20,
        label: "Band 9 gain",
        text: "0.00 dB",
        disabled: true,
        onchange: () => {},
      },
    });
    expect(body).toContain("disabled");
  });
});
