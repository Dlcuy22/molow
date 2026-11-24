<script lang="ts">
  import type { Snippet } from "svelte";
  import CaretLeft from "phosphor-svelte/lib/CaretLeft";
  import CaretRight from "phosphor-svelte/lib/CaretRight";

  let {
    title,
    children,
  }: {
    title: string;
    children: Snippet;
  } = $props();

  let scroller = $state<HTMLDivElement | null>(null);
  let canPrev = $state(false);
  let canNext = $state(false);

  function syncBounds() {
    if (!scroller) {
      canPrev = false;
      canNext = false;

      return;
    }
    const max = scroller.scrollWidth - scroller.clientWidth;
    canPrev = scroller.scrollLeft > 2;
    canNext = max - scroller.scrollLeft > 2;
  }

  $effect(() => {
    const el = scroller;
    if (!el) {
      return;
    }
    syncBounds();
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(syncBounds);
    observer.observe(el);

    return () => observer.disconnect();
  });

  function scrollByPage(direction: -1 | 1) {
    if (!scroller) {
      return;
    }
    const step = Math.max(240, Math.round(scroller.clientWidth * 0.75));
    scroller.scrollBy({ left: direction * step, behavior: "smooth" });
  }
</script>

<section class="flex flex-col gap-3.5" aria-label={title}>
  <div class="flex items-center justify-between gap-4">
    <h2 class="font-display text-[20px] font-bold leading-tight text-fg">
      {title}
    </h2>
    {#if canPrev || canNext}
      <div class="flex shrink-0 items-center gap-2">
        <button
          type="button"
          class="grid size-8 place-items-center rounded-full border border-line bg-bg text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:cursor-default disabled:opacity-35 disabled:hover:bg-bg"
          aria-label="Scroll {title} left"
          disabled={!canPrev}
          onclick={() => scrollByPage(-1)}
        >
          <CaretLeft size="15" weight="bold" />
        </button>
        <button
          type="button"
          class="grid size-8 place-items-center rounded-full border border-line bg-bg text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:cursor-default disabled:opacity-35 disabled:hover:bg-bg"
          aria-label="Scroll {title} right"
          disabled={!canNext}
          onclick={() => scrollByPage(1)}
        >
          <CaretRight size="15" weight="bold" />
        </button>
      </div>
    {/if}
  </div>

  <div
    bind:this={scroller}
    onscroll={syncBounds}
    class="scroll-thin flex gap-4 overflow-x-auto pb-2"
  >
    {@render children()}
  </div>
</section>
