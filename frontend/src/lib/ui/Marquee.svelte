<script lang="ts">
  // A single-line label that only scrolls when it does not fit. Two copies of
  // the text are laid side by side; the animation moves exactly one copy, so the
  // wrap is invisible. The duration follows the distance, so a short overflow
  // does not crawl and a long one does not race.
  let {
    text,
    class: cls = "",
  }: { text: string; class?: string } = $props();

  const GAP = 32;

  let box: HTMLDivElement | undefined = $state();
  let first: HTMLSpanElement | undefined = $state();
  let overflow = $state(false);
  let shift = $state(0);

  function measure() {
    if (!box || !first) {
      return;
    }
    const width = first.offsetWidth;
    shift = width + GAP;
    overflow = width > box.clientWidth + 1;
  }

  $effect(() => {
    // Re-measure when the label changes and when the dock is resized.
    void text;
    measure();
    if (!box) {
      return;
    }
    const ro = new ResizeObserver(measure);
    ro.observe(box);
    if (first) {
      ro.observe(first);
    }

    return () => ro.disconnect();
  });
</script>

<div bind:this={box} class="overflow-hidden {cls}" title={text}>
  <div
    class="flex w-max items-center"
    class:marquee={overflow}
    style="--marquee-shift: {shift}px; --marquee-duration: {Math.max(7, shift / 30)}s; gap: {GAP}px"
  >
    <span bind:this={first} class="whitespace-nowrap">{text}</span>
    {#if overflow}
      <span class="whitespace-nowrap" aria-hidden="true">{text}</span>
    {/if}
  </div>
</div>
