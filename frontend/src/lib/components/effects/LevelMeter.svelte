<script lang="ts">
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

  // Throttle numeric dB text updates to ~10 Hz so rapid meter ticks don't force
  // 60-144 Hz text shaping and layout on every vsync.
  const TEXT_INTERVAL_MS = 100;

  // A null reading means the effect does not meter, which must draw as an inert
  // track, not a zero bar, so silence and "no meter" stay distinguishable.
  const meters = $derived(db !== null && Number.isFinite(db));

  const clipping = $derived(
    kind === "level" && live && db !== null && Number.isFinite(db) && db >= max,
  );

  // Non-level meters (gain-reduction, scalar) have no decay ballistics and
  // render directly from the latest reading.
  const scalarFrac = $derived(kind === "level" ? 0 : clamp(meterFraction(db, min, max), 0, 1));
  const scalarText = $derived(kind === "level" ? "" : meterText(db, unit));

  let maskEl = $state<HTMLDivElement | null>(null);
  let wallEl = $state<HTMLDivElement | null>(null);
  let textEl = $state<HTMLSpanElement | null>(null);
  let srEl = $state<HTMLSpanElement | null>(null);

  // Ballistics state stays off Svelte $state so 60-144 Hz bar motion writes
  // compositor transforms directly to the DOM without reactive churn.
  let shown: number | null = null;
  let wall: MeterWall = { db: null, heldMs: 0 };
  let lastText = "";
  let lastTextAt = 0;

  let targetDb: number | null = null;
  let curMin = -60;
  let curMax = 0;
  let curUnit = "dB";
  let curLabel = "";
  let curMeters = false;

  let raf = 0;
  let lastFrameAt = 0;

  function writeText(value: number | null, now: number, force: boolean) {
    const next = curMeters ? meterText(value, curUnit) : "";
    if (!force && now - lastTextAt < TEXT_INTERVAL_MS) {
      return;
    }
    lastTextAt = now;
    if (next === lastText) {
      return;
    }
    lastText = next;
    if (textEl) {
      textEl.textContent = next;
    }
    if (srEl) {
      srEl.textContent = `${curLabel} ${curMeters ? next : "--"}`;
    }
  }

  function paint(now: number, forceText: boolean) {
    const frac = clamp(levelMeterFraction(shown, curMin, curMax), 0, 1);
    if (maskEl) {
      maskEl.style.transform = `scaleX(${1 - frac})`;
    }
    if (wallEl) {
      const wallFrac =
        wall.db === null ? 0 : clamp(levelMeterFraction(wall.db, curMin, curMax), 0, 1);
      if (wallFrac > 0) {
        wallEl.style.opacity = "1";
        wallEl.style.transform = `translateX(${wallFrac * 100}%)`;
      } else {
        wallEl.style.opacity = "0";
      }
    }
    writeText(shown, now, forceText);
  }

  function tick(now: number) {
    raf = 0;
    const dt = lastFrameAt === 0 ? 0 : Math.min(0.1, (now - lastFrameAt) / 1000);
    lastFrameAt = now;

    shown = stepMeterDb(shown, targetDb, dt);
    wall = stepMeterWall(wall, shown, dt);

    const settled =
      shown === targetDb &&
      (wall.db === null || (shown !== null && wall.db <= shown));
    paint(now, settled);

    if (!settled) {
      raf = requestAnimationFrame(tick);
    } else {
      lastFrameAt = 0;
    }
  }

  $effect(() => {
    curMin = min;
    curMax = max;
    curUnit = unit;
    curLabel = label;
    curMeters = meters;

    if (kind !== "level") {
      return;
    }

    if (!meters) {
      if (raf !== 0) {
        cancelAnimationFrame(raf);
        raf = 0;
      }
      lastFrameAt = 0;
      shown = null;
      wall = { db: null, heldMs: 0 };
      paint(performance.now(), true);

      return;
    }

    targetDb = live ? db : min;
    if (raf === 0) {
      // Paint synchronously on first mount/wake so there is no blank frame,
      // then schedule rAF only if decay or peak-hold still has motion.
      const now = performance.now();
      if (shown === null) {
        shown = targetDb;
        wall = { db: targetDb, heldMs: 0 };
        paint(now, true);
      } else {
        raf = requestAnimationFrame(tick);
      }
    }
  });

  $effect(() => {
    return () => {
      if (raf !== 0) {
        cancelAnimationFrame(raf);
        raf = 0;
      }
    };
  });
</script>

<!--
  The fill is a solid accent for a gain reduction or a scalar, and the level
  scale for a level reading. The level gradient spans the whole track and is
  masked from the right with a compositor-only scaleX cover, so the colour
  encodes the absolute level without per-frame clip-path repaints.
-->
<div class="flex items-center gap-2">
  <span class="min-w-7 shrink-0 text-[11px] text-muted">{label}</span>
  <div class="relative h-2 min-w-0 flex-1 overflow-hidden rounded-[2px] bg-track" aria-hidden="true">
    {#if meters}
      {#if kind === "level"}
        <div
          class="absolute inset-0 meter-level"
          class:meter-clipping={clipping}
        ></div>
        <div
          bind:this={maskEl}
          class="absolute inset-0 origin-right bg-track will-change-transform"
          style="transform: scaleX(1)"
        ></div>
        <div
          bind:this={wallEl}
          class="pointer-events-none absolute inset-0 will-change-transform"
          style="opacity: 0"
        >
          <div class="-ml-[1px] h-full w-[2px] bg-fg/80"></div>
        </div>
      {:else}
        <div
          class="h-full origin-left rounded-[2px] bg-accent will-change-transform"
          style="transform: scaleX({scalarFrac})"
        ></div>
      {/if}
    {/if}
  </div>
  {#if kind === "level"}
    <span
      bind:this={textEl}
      class="w-[4.5rem] shrink-0 whitespace-nowrap text-right text-[11px] leading-none tabular-nums text-muted"
    ></span>
    <span bind:this={srEl} class="sr-only"></span>
  {:else}
    <span
      class="w-[4.5rem] shrink-0 whitespace-nowrap text-right text-[11px] leading-none tabular-nums text-muted"
    >
      {meters ? scalarText : ""}
    </span>
    <span class="sr-only">{label} {scalarText}</span>
  {/if}
</div>
