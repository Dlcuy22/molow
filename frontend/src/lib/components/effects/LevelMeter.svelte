<script lang="ts">
  import { onMount } from "svelte";
  import type { ReadingKind } from "../../effect-types";
  import { levelMeterFraction, meterFraction, meterText, clamp } from "./effects-helpers";
  import { stepMeterDb, stepMeterWall, type MeterWall } from "./meter-ballistics";

  // A level meter for one reading. The unit travels with the reading, so a gain
  // reduction or scalar is not printed as dBFS.
  let {
    db,
    label,
    min = -60,
    max = 0,
    unit = "dB",
    kind = "level",
    live = true,
  }: {
    db: number | null;
    label: string;
    min?: number;
    max?: number;
    unit?: string;
    kind?: ReadingKind;
    live?: boolean;
  } = $props();

  // A null reading means the effect does not meter, which must draw as an inert
  // track, not a zero bar, so silence and "no meter" stay distinguishable.
  const meters = $derived(db !== null && Number.isFinite(db));

  // The displayed level lags the target on the way down only. Audacity's meters
  // run this decay on a 60 Hz timer; a CSS transition cannot reproduce it,
  // because the transition restarts on every event and blurs the attack.
  let shown = $state<number | null>(null);
  const display = $derived(kind === "level" ? (shown ?? db) : db);

  // The wall is the line the bar pushes up and then falls back down under its
  // own slower gravity, so it always leads the bar on the way down.
  let wall = $state<MeterWall>({ db: null, heldMs: 0 });

  onMount(() => {
    if (kind !== "level") {
      return;
    }

    let raf = 0;
    let last = 0;
    const tick = (now: number) => {
      const dt = last === 0 ? 0 : (now - last) / 1000;
      last = now;
      // A paused transport stops feeding the meter, so the last value would sit
      // there forever. No audio is flowing, so the honest target is silence.
      shown = stepMeterDb(shown, live ? db : min, dt);
      wall = stepMeterWall(wall, shown, dt);
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);

    return () => cancelAnimationFrame(raf);
  });

  const frac = $derived(
    kind === "level" ? levelMeterFraction(display, min, max) : meterFraction(display, min, max),
  );
  const fillPct = $derived(clamp(frac * 100, 0, 100));
  const wallPct = $derived(
    wall.db === null ? 0 : clamp(levelMeterFraction(wall.db, min, max) * 100, 0, 100),
  );
  const clipping = $derived(
    kind === "level" && live && db !== null && Number.isFinite(db) && db >= max,
  );
</script>

<!--
  The fill is a solid accent for a gain reduction or a scalar, and the level
  scale for a level reading. The level gradient spans the whole track and is
  clipped to the current value, so the colour encodes the absolute level rather
  than stretching with the bar. The number sits next to it, so the bar is
  repetition for the eye and the value is still readable without colour.
  The label cell has a minimum width and can grow, so a longer name such as
  "Gain Reduction" is printed in full while the short In/Out rows keep their
  width.
-->
<div class="flex items-center gap-2">
  <span class="min-w-7 shrink-0 text-[11px] text-muted">{label}</span>
  <div class="relative h-2 min-w-0 flex-1 overflow-hidden rounded-[2px] bg-track" aria-hidden="true">
    {#if meters}
      {#if kind === "level"}
        <div
          class="absolute inset-0 meter-level"
          class:meter-clipping={clipping}
          style="clip-path: inset(0 {100 - fillPct}% 0 0)"
        ></div>
        {#if wallPct > 0}
          <div
            class="absolute inset-y-0 w-[2px] bg-fg/80"
            style="left: calc({wallPct}% - 1px)"
          ></div>
        {/if}
      {:else}
        <div class="h-full rounded-[2px] bg-accent" style="width: {fillPct}%"></div>
      {/if}
    {/if}
  </div>
  <span class="w-14 shrink-0 text-right text-[11px] tabular-nums text-muted">
    {meters ? meterText(display, unit) : ""}
  </span>
  <span class="sr-only">{label} {meterText(display, unit)}</span>
</div>