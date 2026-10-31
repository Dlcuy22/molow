<script lang="ts">
  import { onMount } from "svelte";
  import { commands } from "../../store";
  import type { EffectPreset, EffectStage } from "../../effect-types";
  import { numericValue, paramValue } from "./effects-helpers";
  import {
    eqActiveBands,
    eqBands,
    eqNumBandsParam,
    eqPresetMatches,
    faderFill,
    formatGainDB,
    formatQ,
    gainParts,
    hzParts,
  } from "./eq-helpers";
  import PresetMenu from "./PresetMenu.svelte";
  import ScrubField from "./ScrubField.svelte";

  // The RBJ equalizer's dedicated control surface. The generic panel renders a
  // group of knobs per band, which wastes the width and hides the one thing an
  // EQ is read for: the gain of every band at once. Here each band is a column:
  // its frequency and Q above the fader, its gain below it, all three editable
  // by text or by horizontal drag.
  let {
    stage,
    onparam,
  }: {
    stage: EffectStage;
    onparam: (key: string, value: unknown) => void;
  } = $props();

  const bands = $derived(eqBands(stage));
  const numBandsParam = $derived(eqNumBandsParam(stage));
  const active = $derived(eqActiveBands(stage, bands));

  // Presets belong to the panel, not the popover: the applied name is what the
  // trigger shows and what the edited marker compares against after the menu
  // closes.
  let presets = $state<EffectPreset[]>([]);
  let applied = $state<EffectPreset | null>(null);
  const dirty = $derived(applied !== null && !eqPresetMatches(stage, applied.values));

  let appliedStage = "";

  async function loadPresets() {
    try {
      presets = await commands.effectPresets(stage.impl);
    } catch {
      presets = [];
    }
  }

  onMount(() => {
    void loadPresets();
  });

  // A different stage is a different curve, so the name that was applied to the
  // previous one no longer describes it.
  $effect(() => {
    if (appliedStage !== stage.id) {
      appliedStage = stage.id;
      applied = null;
    }
  });

  function valueOf(key: string, fallback = 0): number {
    const param = stage.schema.find((p) => p.key === key);

    return param ? numericValue(paramValue(stage, param), fallback) : fallback;
  }
</script>

<div class="flex flex-col gap-3">
  <div class="flex flex-wrap items-center justify-between gap-2">
    {#if numBandsParam}
      <div class="flex items-center gap-2">
        <span class="text-[11px] text-muted">Bands</span>
        <div class="w-14">
          <ScrubField
            value={active}
            min={numBandsParam.min}
            max={numBandsParam.max}
            step={1}
            label="Bands in use"
            text={String(active)}
            onchange={(v) => onparam(numBandsParam.key, v)}
          />
        </div>
      </div>
    {/if}

    <PresetMenu
      {stage}
      {presets}
      {applied}
      {dirty}
      onapplied={(p) => (applied = p)}
      onrefresh={loadPresets}
    />
  </div>

  <div class="scroll-thin overflow-x-auto pb-1">
    <!-- Columns keep a minimum width so ten bands stay legible side by side;
         a narrower window scrolls them horizontally instead of shrinking the
         faders into unusable slivers. -->
    <div
      class="grid gap-x-1"
      style="grid-template-columns: repeat({bands.length}, minmax(3.5rem, 1fr))"
    >
      {#each bands as band (band.index)}
        {@const on = band.index < active}
        {@const freq = valueOf(band.frequency.key)}
        {@const gain = valueOf(band.gain.key)}
        {@const q = valueOf(band.q.key)}
        {@const hz = hzParts(freq)}
        {@const db = gainParts(gain)}
        {@const fill = faderFill(gain, band.gain.min, band.gain.max)}
        {@const zeroTop = 1 - fill.zero}
        <div class="flex min-w-0 flex-col items-center gap-1.5" class:opacity-40={!on}>
          <span class="text-[11px] tabular-nums text-muted">Band {band.index + 1}</span>

          <ScrubField
            value={freq}
            min={band.frequency.min}
            max={band.frequency.max}
            step={band.frequency.step}
            log
            label="Band {band.index + 1} frequency"
            text={hz.value}
            suffix={hz.unit}
            disabled={!on}
            onchange={(v) => onparam(band.frequency.key, v)}
          />

          <ScrubField
            value={q}
            min={band.q.min}
            max={band.q.max}
            step={band.q.step}
            label="Band {band.index + 1} Q"
            text={formatQ(q)}
            prefix="Q"
            disabled={!on}
            onchange={(v) => onparam(band.q.key, v)}
          />

          <div class="fader-row w-full">
            <!-- The 0 dB baseline runs across the whole column, so the ten
                 faders share one reference line instead of each hiding its
                 zero inside a track. -->
            <div
              class="pointer-events-none absolute inset-x-0 h-px bg-line"
              style="top: calc(8px + (100% - 16px) * {zeroTop})"
              aria-hidden="true"
            ></div>
            <div class="fader-wrap">
              <input
                class="fader"
                type="range"
                min={band.gain.min}
                max={band.gain.max}
                step={band.gain.step}
                value={gain}
                style="--lo: {fill.lo}; --hi: {fill.hi};"
                aria-label="Band {band.index + 1} gain"
                aria-valuetext={formatGainDB(gain)}
                disabled={!on}
                oninput={(e) =>
                  onparam(band.gain.key, Number((e.currentTarget as HTMLInputElement).value))}
              />
            </div>
          </div>

          <ScrubField
            value={gain}
            min={band.gain.min}
            max={band.gain.max}
            step={band.gain.step}
            label="Band {band.index + 1} gain"
            text={db.value}
            suffix={db.unit}
            disabled={!on}
            onchange={(v) => onparam(band.gain.key, v)}
          />
        </div>
      {/each}
    </div>
  </div>
</div>
