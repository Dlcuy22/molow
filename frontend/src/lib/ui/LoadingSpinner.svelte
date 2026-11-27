<script lang="ts">
  import { untrack } from "svelte";
  import { SPINNER_MAX_STEP_MS, SPINNER_MIN_STEP_MS } from "../activity";

  const BASE_STEP_MS = 200;
  const CYCLE_MS = BASE_STEP_MS * 8;
  const PHASE_OFFSET_MS = BASE_STEP_MS * 5;

  function buildKeyframes(travel: number): Keyframe[] {
    return [
      { offset: 0, transform: "translate(0px, 0px)" },
      { offset: 0.125, transform: `translate(${travel}px, 0px)` },
      { offset: 0.25, transform: `translate(${travel}px, 0px)` },
      { offset: 0.375, transform: `translate(${travel}px, ${travel}px)` },
      { offset: 0.5, transform: `translate(${travel}px, ${travel}px)` },
      { offset: 0.625, transform: `translate(0px, ${travel}px)` },
      { offset: 0.75, transform: `translate(0px, ${travel}px)` },
      { offset: 0.875, transform: "translate(0px, 0px)" },
      { offset: 1, transform: "translate(0px, 0px)" },
    ];
  }

  let {
    label = "",
    size = 24,
    inline = false,
    stepMs = 200,
    class: cls = "",
  }: {
    label?: string;
    size?: number;
    inline?: boolean;
    stepMs?: number;
    class?: string;
  } = $props();

  const prefersReducedMotion =
    typeof window !== "undefined" &&
    Boolean(window.matchMedia?.("(prefers-reduced-motion: reduce)").matches);
  const clampedMs = $derived(
    Math.max(SPINNER_MIN_STEP_MS, Math.min(SPINNER_MAX_STEP_MS, Math.round(stepMs))),
  );

  const inset = $derived(size >= 20 ? 1 : 0);
  const gap = $derived(Math.max(2, Math.round(size / 12)));
  const block = $derived(Math.max(2, Math.floor((size - inset * 2 - gap) / 2)));
  const travel = $derived(block + gap);
  const keyframes = $derived(buildKeyframes(travel));

  let boxA = $state<HTMLSpanElement | null>(null);
  let boxB = $state<HTMLSpanElement | null>(null);
  let animA: Animation | null = null;
  let animB: Animation | null = null;

  function applySpeed(ms: number) {
    if (!animA || !animB) {
      return;
    }
    const rate = BASE_STEP_MS / ms;
    const t = Number(animA.currentTime ?? 0);
    animA.playbackRate = rate;
    animB.playbackRate = rate;
    animB.currentTime = t + PHASE_OFFSET_MS;
  }

  $effect(() => {
    const frames = keyframes;
    if (prefersReducedMotion || !boxA || !boxB) {
      return;
    }
    if (typeof boxA.animate !== "function" || typeof boxB.animate !== "function") {
      return;
    }
    const opts: KeyframeAnimationOptions = {
      duration: CYCLE_MS,
      iterations: Infinity,
      easing: "linear",
    };
    animA = boxA.animate(frames, opts);
    animB = boxB.animate(frames, opts);
    animB.currentTime = PHASE_OFFSET_MS;
    applySpeed(untrack(() => clampedMs));

    return () => {
      animA?.cancel();
      animB?.cancel();
      animA = null;
      animB = null;
    };
  });

  $effect(() => {
    applySpeed(clampedMs);
  });
</script>

{#snippet spinnerBoxes()}
  <span
    class="relative inline-block shrink-0"
    style="width:{size}px; height:{size}px"
    aria-hidden="true"
  >
    <span
      bind:this={boxA}
      class="absolute rounded-[1px] bg-current"
      style="top:{inset}px; left:{inset}px; width:{block}px; height:{block}px"
    ></span>
    <span
      bind:this={boxB}
      class="absolute rounded-[1px] bg-current"
      style="top:{inset}px; left:{inset}px; width:{block}px; height:{block}px; transform:translate(0px, {travel}px)"
    ></span>
  </span>
{/snippet}

{#if inline}
  <span class="inline-flex shrink-0 items-center gap-2 {cls}">
    {@render spinnerBoxes()}
    {#if label}
      <span class="text-[12px]">{label}</span>
    {/if}
  </span>
{:else}
  <div class="flex flex-col items-center gap-3 text-fg/75 {cls}" role="status" aria-live="polite">
    {@render spinnerBoxes()}
    {#if label}
      <span class="text-[12px] text-fg/70">{label}</span>
    {/if}
  </div>
{/if}
