<script lang="ts">
  import { onMount, tick } from "svelte";
  import { Plus } from "phosphor-svelte";
  import { appliedPresets, commands } from "../../store";
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
  // Only the processed bands are drawn. The rest are declared by the engine but
  // have no place on screen until the user adds them.
  const shown = $derived(bands.slice(0, active));

  let stripEl = $state<HTMLDivElement | null>(null);

  // Adding appends one band at the far right. Its schema default is 100 Hz for
  // anything past the classic ten, so the new column lands on a usable
  // frequency instead of a copy of the highest band.
  async function addBand() {
    if (!numBandsParam || active >= bands.length) {
      return;
    }
    onparam(numBandsParam.key, active + 1);
    await tick();
    stripEl?.scrollTo({ left: stripEl.scrollWidth });
  }

  // Presets belong to the store, not this component: the applied label is
  // keyed by stage ID, so switching to another effect and back keeps it, and a
  // remount of this panel does not lose it.
  let presets = $state<EffectPreset[]>([]);
  const applied = $derived($appliedPresets[stage.id] ?? null);
  const dirty = $derived(applied !== null && !eqPresetMatches(stage, applied.values));

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
      onrefresh={loadPresets}
    />
  </div>

  <div class="scroll-thin overflow-x-auto pb-1" bind:this={stripEl}>
    <!-- The active bands plus one append cell. Columns keep a minimum width so
         they stay legible; a narrow window scrolls them horizontally instead of
         shrinking the faders into unusable slivers. -->
    <div
      class="grid gap-x-1"
      style="grid-template-columns: repeat({shown.length}, minmax(3.25rem, 1fr)) 2rem"
    >
      {#each shown as band (band.index)}
        {@const freq = valueOf(band.frequency.key)}
        {@const gain = valueOf(band.gain.key)}
        {@const q = valueOf(band.q.key)}
        {@const hz = hzParts(freq)}
        {@const db = gainParts(gain)}
        {@const fill = faderFill(gain, band.gain.min, band.gain.max)}
        {@const zeroTop = 1 - fill.zero}
        <div class="flex min-w-0 flex-col items-center gap-1.5">
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
            onchange={(v) => onparam(band.q.key, v)}
          />

          <div class="fader-row w-full">
            <!-- The 0 dB baseline runs across the whole column, so the faders
                 share one reference line instead of each hiding its zero inside
                 a track. -->
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
            onchange={(v) => onparam(band.gain.key, v)}
          />
        </div>
      {/each}

      {#if numBandsParam}
        <div class="flex flex-col items-center gap-1.5">
          <span class="text-[11px] text-muted">Add</span>
          <button
            type="button"
            class="flex h-6 w-full items-center justify-center rounded-[4px] border border-dashed border-line text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:cursor-default disabled:opacity-40 disabled:hover:bg-transparent"
            aria-label="Add a band at 100 Hz"
            title="Add a band at 100 Hz"
            disabled={active >= bands.length}
            onclick={addBand}
          >
            <Plus size="12" aria-hidden="true" />
          </button>
        </div>
      {/if}
    </div>
  </div>
</div>
