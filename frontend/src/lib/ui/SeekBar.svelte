<script lang="ts">
  import { fillFrac, formatTime } from "../format";

  // One seek control for the dock and the player. The native range stays
  // underneath for pointer travel, keyboard steps and assistive technology;
  // the line, fill and thumb are painted from the same local value, so a drag
  // tracks the pointer and the 4 Hz snapshot only clears the held value once
  // the engine agrees.
  let {
    position = 0,
    duration = 0,
    onseek = (_ms: number) => {},
    variant = "bar",
    tone = "accent",
    side = "top",
  }: {
    position?: number;
    duration?: number;
    onseek?: (ms: number) => void;
    variant?: "edge" | "bar";
    tone?: "accent" | "fg";
    side?: "top" | "bottom";
  } = $props();

  let scrubbing = $state(false);
  let scrubValue = $state(0);
  let pendingSeek = $state<number | null>(null);

  const value = $derived(scrubbing ? scrubValue : (pendingSeek ?? position));
  const fill = $derived(fillFrac(value, 0, duration));
  const disabled = $derived(duration === 0);

  // A seek the engine clamped or rejected must not hold the bar forever, so the
  // pending value also expires on its own.
  $effect(() => {
    if (pendingSeek !== null && Math.abs(position - pendingSeek) < 750) {
      pendingSeek = null;
    }
  });
  $effect(() => {
    if (pendingSeek === null) {
      return;
    }
    const timer = setTimeout(() => (pendingSeek = null), 1500);

    return () => clearTimeout(timer);
  });

  function seekTo(ms: number) {
    const target = Math.min(Math.max(ms, 0), duration);
    pendingSeek = target;
    onseek(target);
  }

  function onInput(e: Event) {
    scrubbing = true;
    pendingSeek = null;
    scrubValue = Number((e.currentTarget as HTMLInputElement).value);
  }

  function onCommit(e: Event) {
    scrubbing = false;
    seekTo(Number((e.currentTarget as HTMLInputElement).value));
  }

  const fillColor = $derived(tone === "fg" ? "bg-fg" : "bg-accent");
  // The album player's rail is translucent so the artwork shows through; the
  // dock and floating bars keep the solid track.
  const trackColor = $derived(tone === "fg" ? "bg-track/27" : "bg-track");
</script>

{#if variant === "edge"}
  <!-- Docked bars have no second row, so the progress is the top edge. The hit
       area is taller than the line and the line grows on hover, which is where
       the thumb becomes available. -->
  <div
    class="seek group/seek absolute inset-x-0 h-3.5 {side === 'top' ? 'top-0' : 'bottom-0'}"
    class:seek-instant={scrubbing}
  >
    <div
      class="pointer-events-none absolute inset-x-0 top-0 h-[3px] bg-track transition-[height] duration-150 group-hover/seek:h-[5px]"
    >
      <div class="seek-fill h-full {fillColor}" style="width: {fill * 100}%"></div>
    </div>
    <div class="pointer-events-none absolute inset-x-[5px] top-[2px] h-0">
      <div
        class="seek-thumb absolute size-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full {fillColor}"
        style="left: {fill * 100}%"
      ></div>
    </div>
    <input
      class="seek-input absolute inset-0 m-0 h-full w-full cursor-pointer appearance-none bg-transparent opacity-0 disabled:cursor-default"
      type="range"
      min="0"
      max={duration || 1}
      step="1000"
      value={value}
      {disabled}
      aria-label="Seek"
      aria-valuetext="{formatTime(value)} of {formatTime(duration)}"
      oninput={onInput}
      onchange={onCommit}
    />
  </div>
{:else}
  <div class="seek relative flex h-4 w-full items-center" class:seek-instant={scrubbing} class:opacity-40={disabled}>
    <div class="pointer-events-none absolute inset-x-0 top-1/2 h-1 -translate-y-1/2 overflow-hidden rounded-[2px] {trackColor}">
      <div class="seek-fill h-full {fillColor}" style="width: {fill * 100}%"></div>
    </div>
    <div class="pointer-events-none absolute inset-x-[5px] top-1/2 h-0">
      <div
        class="seek-thumb absolute size-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full {fillColor}"
        style="left: {fill * 100}%"
      ></div>
    </div>
    <input
      class="seek-input absolute inset-0 m-0 h-full w-full cursor-pointer appearance-none bg-transparent opacity-0 disabled:cursor-default"
      type="range"
      min="0"
      max={duration || 1}
      step="1000"
      value={value}
      {disabled}
      aria-label="Seek"
      aria-valuetext="{formatTime(value)} of {formatTime(duration)}"
      oninput={onInput}
      onchange={onCommit}
    />
  </div>
{/if}
