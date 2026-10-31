<script lang="ts">
  import { tick } from "svelte";
  import { clamp, roundToStep } from "./effects-helpers";
  import {
    SCRUB_THRESHOLD_PX,
    parseScrubText,
    plainNumber,
    scrubValue,
    stepValue,
    type ScrubRange,
  } from "./eq-helpers";

  // A numeric value that is read at a glance, typed on a click, and dragged on
  // a press: the three ways a mixing engineer expects to change a number. The
  // press-to-drag and click-to-type split avoids the usual conflict between a
  // text caret and a scrub gesture, because the field is only an input while it
  // is being edited.
  let {
    value,
    min,
    max,
    step = 0.1,
    log = false,
    label,
    text,
    prefix = "",
    suffix = "",
    disabled = false,
    onchange,
  }: {
    value: number;
    min: number;
    max: number;
    step?: number;
    log?: boolean;
    label: string;
    text: string;
    prefix?: string;
    suffix?: string;
    disabled?: boolean;
    onchange: (value: number) => void;
  } = $props();

  let editing = $state(false);
  let draft = $state("");
  let inputEl = $state<HTMLInputElement | null>(null);

  const range = $derived<ScrubRange>({ min, max, step, log });
  // The visible field splits the unit into a muted suffix to fit a narrow band
  // column, but assistive technology must hear the value as one phrase.
  const spoken = $derived([prefix, text, suffix].filter((s) => s !== "").join(" "));

  // The gesture state is deliberately plain variables: they change on every
  // pointermove and must not schedule a render for a value nobody displays.
  let pointerId = -1;
  let startX = 0;
  let startValue = 0;
  let moved = false;
  let lastEmitted = 0;

  function emit(next: number) {
    if (!Number.isFinite(next) || next === lastEmitted) {
      return;
    }
    lastEmitted = next;
    onchange(next);
  }

  function onPointerDown(e: PointerEvent) {
    if (disabled || e.button !== 0) {
      return;
    }
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    pointerId = e.pointerId;
    startX = e.clientX;
    startValue = value;
    lastEmitted = value;
    moved = false;
  }

  function onPointerMove(e: PointerEvent) {
    if (pointerId === -1 || e.pointerId !== pointerId) {
      return;
    }
    const dx = e.clientX - startX;
    if (!moved && Math.abs(dx) < SCRUB_THRESHOLD_PX) {
      return;
    }
    moved = true;
    emit(scrubValue(startValue, dx, range, e.shiftKey));
  }

  function onPointerUp(e: PointerEvent) {
    if (pointerId === -1 || e.pointerId !== pointerId) {
      return;
    }
    const el = e.currentTarget as HTMLElement;
    if (el.hasPointerCapture(e.pointerId)) {
      el.releasePointerCapture(e.pointerId);
    }
    pointerId = -1;
    if (!moved) {
      void startEdit();
    }
  }

  function onPointerCancel(e: PointerEvent) {
    if (pointerId === -1 || e.pointerId !== pointerId) {
      return;
    }
    pointerId = -1;
    moved = false;
  }

  async function startEdit() {
    if (disabled) {
      return;
    }
    draft = plainNumber(value);
    editing = true;
    await tick();
    // Focus on the next frame, after the click that ended the press has been
    // dispatched. Focusing inside the pointerup handler loses to that click and
    // leaves the caret nowhere.
    requestAnimationFrame(() => {
      inputEl?.focus();
      inputEl?.select();
    });
  }

  function cancel() {
    editing = false;
  }

  function commit() {
    if (!editing) {
      return;
    }
    editing = false;
    const parsed = parseScrubText(draft);
    if (parsed === null) {
      return;
    }
    // Round and clamp here, not in the parent: an entry like "1.2345" must land
    // on the step the field advertises, the same way a drag does.
    onchange(clamp(roundToStep(parsed, step), min, max));
  }

  function onInputKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      commit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    }
  }

  function onButtonKey(e: KeyboardEvent) {
    if (disabled) {
      return;
    }
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      void startEdit();

      return;
    }
    if (e.key === "Home") {
      e.preventDefault();
      onchange(min);

      return;
    }
    if (e.key === "End") {
      e.preventDefault();
      onchange(max);

      return;
    }
    let direction = 0;
    let big = false;
    if (e.key === "ArrowUp" || e.key === "ArrowRight") {
      direction = 1;
    } else if (e.key === "ArrowDown" || e.key === "ArrowLeft") {
      direction = -1;
    } else if (e.key === "PageUp") {
      direction = 1;
      big = true;
    } else if (e.key === "PageDown") {
      direction = -1;
      big = true;
    } else {
      return;
    }
    e.preventDefault();
    onchange(stepValue(value, direction, range, { big, fine: e.shiftKey }));
  }
</script>

{#if editing}
  <!-- An explicit text input, sized like the button it replaces, so opening the
       editor does not shift the column. -->
  <input
    bind:this={inputEl}
    class="h-6 w-full rounded-[4px] border border-line bg-bg px-1 text-center text-[11px] tabular-nums text-fg"
    type="text"
    inputmode="decimal"
    value={draft}
    aria-label={label}
    oninput={(e) => (draft = (e.currentTarget as HTMLInputElement).value)}
    onkeydown={onInputKey}
    onblur={commit}
  />
{:else}
  <!--
    A spinbutton, not a plain button: arrow keys, Home and End are expected to
    change the value, and assistive technology announces the number with its
    range. The press-and-drag gesture is additive, never the only way in.
  -->
  <button
    type="button"
    role="spinbutton"
    class="flex h-6 w-full touch-none select-none items-center justify-center gap-0.5 rounded-[4px] border border-line bg-bg/40 px-0.5 text-[11px] tabular-nums text-fg transition-colors hover:bg-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:cursor-default disabled:opacity-40 disabled:hover:bg-bg/40"
    class:cursor-ew-resize={!disabled}
    aria-label={label}
    aria-valuenow={value}
    aria-valuemin={min}
    aria-valuemax={max}
    aria-valuetext={spoken}
    disabled={disabled}
    title="Drag to adjust, click to type"
    onpointerdown={onPointerDown}
    onpointermove={onPointerMove}
    onpointerup={onPointerUp}
    onpointercancel={onPointerCancel}
    onkeydown={onButtonKey}
  >
    {#if prefix !== ""}
      <span class="shrink-0 text-muted">{prefix}</span>
    {/if}
    <span class="truncate">{text}</span>
    {#if suffix !== ""}
      <span class="shrink-0 text-[10px] text-muted">{suffix}</span>
    {/if}
  </button>
{/if}
