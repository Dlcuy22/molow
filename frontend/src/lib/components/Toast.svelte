<script lang="ts">
  import { untrack } from "svelte";
  import type { Component } from "svelte";

  /**
   * Toast is the message card. It keeps its own clock, calls onclose when the
   * lifetime ends, and leaves motion to the host, since only the host knows the
   * card's place in the stack. The bar is the time left, so it reaches zero
   * exactly at the lifetime. Hovering freezes both.
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
    onclose,
  }: Props = $props();

  const stacked = $derived(layout === "stacked");
  // One style attribute, because a second would replace the first.
  const style = $derived(
    `--toast-duration: ${duration}ms${maxWidth ? `; max-width: ${maxWidth}px` : ""}`,
  );

  let paused = $state(false);

  // A plain binding, not a signal, so spending it down does not re-run the
  // effect; untrack keeps duration an initial value only.
  let remaining = untrack(() => duration);
  let startedAt = 0;

  $effect(() => {
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
  class:w-fit={!stacked}
  class:gap-2.5={!stacked}
  class:pt-[7px]={!stacked}
  class:pb-[9px]={!stacked}
  {style}
  onmouseenter={() => (paused = true)}
  onmouseleave={() => (paused = false)}
>
  {#if icon}
    {@const Icon = icon}
    <Icon size={stacked ? 20 : 16} aria-hidden="true" />
  {/if}

  <span class:text-center={stacked}>{message}</span>
  <span
    class="toast-bar absolute inset-x-0 bottom-0 h-[3px] bg-fg"
    class:paused
    aria-hidden="true"
  ></span>
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

  @keyframes toast-drain {
    from {
      transform: scaleX(1);
    }
    to {
      transform: scaleX(0);
    }
  }
</style>
