<script lang="ts">
  import { untrack } from "svelte";
  import type { Component } from "svelte";

  /**
   * Toast is the message card. It keeps its own clock, calls onclose when the
   * lifetime ends, and leaves motion to the host, since only the host knows the
   * card's place in the stack. The bar is the time left, so it reaches zero
   * exactly at the lifetime. Hovering freezes both.
   *
   * With progress set the card turns interactive: the bottom bar is a level
   * meter for a live value instead of a countdown, and the value reads out
   * beside the label. A new level restarts the lifetime in step with the
   * store's timer, so a held key keeps the card up. The host still owns the
   * stack.
   *
   * The card carries no live region: the host's role="status" announces, and a
   * second one would double the announcement.
   */

  interface Props {
    /** The card's line. */
    message: string;
    /** A phosphor glyph, 16 px inline and 20 stacked. */
    icon?: Component;
    /** Inline is one row; stacked puts the icon over the text. */
    layout?: "inline" | "stacked";
    /** The surface behind the card. On a light plane the card takes the darkest
     *  step, so it stays the darker shape against a bright field. */
    plane?: "dark" | "light";
    /** Lifetime in milliseconds. */
    duration?: number;
    /** Caps an inline card so a long message wraps into a block. */
    maxWidth?: number;
    /** The live level, 0..1. Its presence switches the card to meter mode. */
    progress?: number;
    /** The readout beside the message, e.g. the percentage. Meter mode only. */
    value?: string;
    /** Eases the meter between levels. The countdown bar never takes it: it is
     *  a clock, and easing a clock lies about the time left. */
    eased?: boolean;
    /** Runs when the lifetime ends, so the host can remove the card. */
    onclose?: () => void;
  }

  let {
    message,
    icon,
    layout = "inline",
    plane = "dark",
    duration = 3000,
    maxWidth,
    progress,
    value,
    eased = false,
    onclose,
  }: Props = $props();

  const stacked = $derived(layout === "stacked");
  // Meter mode has its own row shape and its own bottom bar; it never stacks.
  const meter = $derived(progress !== undefined);
  // A fixed width, because a w-fit card would breathe as the percentage text
  // changes width ("5%" into "100%") and the meter under it with it.
  const meterWidth = 176;
  // One style attribute, because a second would replace the first.
  const style = $derived(
    `--toast-duration: ${duration}ms${
      maxWidth && !meter ? `; max-width: ${maxWidth}px` : ""
    }${meter ? `; width: ${meterWidth}px` : ""}`,
  );

  let paused = $state(false);

  // A plain binding, not a signal, so spending it down does not re-run the
  // effect; untrack keeps duration an initial value only.
  let remaining = untrack(() => duration);
  let startedAt = 0;
  // The level this clock is running for. A change is new content, so the
  // lifetime restarts in step with the store's timer; pausing and resuming
  // must not, or hover would hand the card a fresh life it did not earn.
  let lastProgress: number | undefined;

  $effect(() => {
    if (progress !== lastProgress) {
      lastProgress = progress;
      remaining = duration;
    }
    if (paused) {
      return;
    }
    startedAt = performance.now();
    const close = setTimeout(() => onclose?.(), remaining);

    return () => {
      clearTimeout(close);
      remaining = Math.max(0, remaining - (performance.now() - startedAt));
    };
  });
</script>

<div
  class="pointer-events-auto relative flex items-center overflow-hidden rounded-lg px-4 text-xs leading-4 text-fg shadow-menu {plane ===
  'light'
    ? 'bg-bg-dark/[0.96]'
    : 'bg-surface/90'}"
  role="presentation"
  class:flex-col={stacked}
  class:w-[200px]={stacked}
  class:gap-1.5={stacked}
  class:pt-2.5={stacked}
  class:pb-3={stacked}
  class:w-fit={!stacked && !meter}
  class:gap-2.5={!stacked}
  class:pt-[7px]={!stacked}
  class:pb-[9px]={!stacked}
  {style}
  onmouseenter={() => (paused = true)}
  onmouseleave={() => (paused = false)}
>
  {#if meter}
    {#if icon}
      {@const Icon = icon}
      <Icon size={16} aria-hidden="true" />
    {/if}
    <span class="min-w-0 truncate text-muted">{message}</span>
    <span class="ml-auto tabular-nums">{value}</span>
  {:else}
    {#if icon}
      {@const Icon = icon}
      <Icon size={stacked ? 20 : 16} aria-hidden="true" />
    {/if}

    <span class:text-center={stacked}>{message}</span>
  {/if}

  {#if meter}
    <!-- The level, not the time. The fill eases between values when the card
         opts in; the countdown bar is an animation on its own class and never
         takes that rule. -->
    <span class="absolute inset-x-0 bottom-0 h-[3px] bg-track" aria-hidden="true">
      <span
        class="toast-fill block h-full bg-accent"
        class:eased={eased}
        style="width: {Math.round((progress ?? 0) * 100)}%"
      ></span>
    </span>
  {:else}
    <span
      class="toast-bar absolute inset-x-0 bottom-0 h-[3px] bg-fg"
      class:paused
      aria-hidden="true"
    ></span>
  {/if}
</div>

<style>
  .toast-bar {
    transform-origin: left;
    animation: toast-drain var(--toast-duration, 3000ms) linear forwards;
  }

  /* The bar and the timeout are two clocks on one lifetime, so pausing both
     keeps them in step: the bar holds its place and the timeout is restarted
     with the time the bar has left. */
  .toast-bar.paused {
    animation-play-state: paused;
  }

  /* The meter eases between levels, so a held key glides instead of stepping.
     The countdown bar is a keyframe animation, not a width, so this rule
     cannot reach it. */
  .toast-fill.eased {
    transition: width 120ms ease-out;
  }

  @keyframes toast-drain {
    from {
      transform: scaleX(1);
    }
    to {
      transform: scaleX(0);
    }
  }
</style>
