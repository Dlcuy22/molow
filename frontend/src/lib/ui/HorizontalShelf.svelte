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
  let isDragging = $state(false);

  const canScroll = $derived(canPrev || canNext);
  const DRAG_THRESHOLD = 5;
  const DRAG_LERP_RATE = 24;
  const COAST_DAMPING = 3.6;
  const SAMPLE_WINDOW_MS = 100;
  const MAX_COAST_SPEED = 3200;
  const MAX_OVERSCROLL = 56;
  const OVERSCROLL_RESISTANCE = 140;
  const SPRING_STIFFNESS = 260;
  const SPRING_DAMPING = 24;

  let dragStartX = 0;
  let dragStartScrollLeft = 0;
  let didDrag = false;
  let suppressClick = false;
  let targetScrollLeft = 0;
  let currentScrollLeft = 0;
  let coastVx = 0;
  let samples: Array<{ x: number; t: number }> = [];
  let glideRaf = 0;
  let lastFrameTime = 0;
  let targetOverscroll = 0;
  let overscrollX = $state(0);
  let subPixelX = $state(0);
  let overscrollVx = 0;

  function maxScrollLeft(): number {
    if (!scroller) {
      return 0;
    }
    return Math.max(0, scroller.scrollWidth - scroller.clientWidth);
  }

  function clampScroll(value: number): number {
    return Math.min(maxScrollLeft(), Math.max(0, value));
  }

  function rubberBand(over: number): number {
    if (over === 0) {
      return 0;
    }
    const sign = over < 0 ? -1 : 1;
    const abs = Math.abs(over);
    // Negative scroll-over (past start) shifts content right (+X); positive shifts left (-X).
    return -sign * ((MAX_OVERSCROLL * abs) / (abs + OVERSCROLL_RESISTANCE));
  }

  function stopGlide() {
    if (glideRaf !== 0) {
      cancelAnimationFrame(glideRaf);
      glideRaf = 0;
    }
    lastFrameTime = 0;
    coastVx = 0;
    targetOverscroll = 0;
    overscrollX = 0;
    subPixelX = 0;
    overscrollVx = 0;
  }

  function prefersReducedMotion(): boolean {
    return (
      typeof window !== "undefined" &&
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    );
  }

  function applyScrollPosition() {
    if (!scroller) {
      return;
    }
    scroller.scrollLeft = currentScrollLeft;
    // Compensate for integer scrollLeft quantization so slow motion stays sub-pixel smooth on the GPU.
    subPixelX = scroller.scrollLeft - currentScrollLeft;
  }

  function startGlide() {
    if (glideRaf !== 0 || !scroller) {
      return;
    }
    lastFrameTime = 0;

    const step = (now: number) => {
      if (!scroller) {
        glideRaf = 0;
        return;
      }
      const dt =
        lastFrameTime === 0
          ? 1 / 60
          : Math.min(0.05, Math.max(0.001, (now - lastFrameTime) / 1000));
      lastFrameTime = now;

      if (isDragging) {
        const alpha = 1 - Math.exp(-DRAG_LERP_RATE * dt);
        currentScrollLeft += (targetScrollLeft - currentScrollLeft) * alpha;
        applyScrollPosition();
        overscrollX += (targetOverscroll - overscrollX) * alpha;
        overscrollVx = 0;
      } else {
        if (Math.abs(coastVx) >= 2) {
          const decay = Math.exp(-COAST_DAMPING * dt);
          // Integrate velocity over dt using average decay factor for frame-rate independence.
          const avgScale = (1 - decay) / (COAST_DAMPING * dt);
          const next = currentScrollLeft + coastVx * dt * avgScale;
          coastVx *= decay;

          const max = maxScrollLeft();
          if (next <= 0) {
            currentScrollLeft = 0;
            if (coastVx < -40) {
              overscrollVx = Math.min(650, -coastVx * 0.45);
            }
            coastVx = 0;
          } else if (next >= max) {
            currentScrollLeft = max;
            if (coastVx > 40) {
              overscrollVx = Math.max(-650, -coastVx * 0.45);
            }
            coastVx = 0;
          } else {
            currentScrollLeft = next;
          }
          applyScrollPosition();
        } else {
          coastVx = 0;
        }

        const force = -SPRING_STIFFNESS * overscrollX - SPRING_DAMPING * overscrollVx;
        overscrollVx += force * dt;
        overscrollX += overscrollVx * dt;
        if (Math.abs(overscrollX) < 0.2 && Math.abs(overscrollVx) < 1) {
          overscrollX = 0;
          overscrollVx = 0;
        }
      }

      const scrollSettled = coastVx === 0;
      const springSettled = overscrollX === 0 && overscrollVx === 0;
      if (!isDragging && scrollSettled && springSettled) {
        glideRaf = 0;
        return;
      }

      glideRaf = requestAnimationFrame(step);
    };

    glideRaf = requestAnimationFrame(step);
  }

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
      return () => stopGlide();
    }
    const observer = new ResizeObserver(syncBounds);
    observer.observe(el);

    return () => {
      observer.disconnect();
      stopGlide();
    };
  });

  function scrollByPage(direction: -1 | 1) {
    if (!scroller) {
      return;
    }
    stopGlide();
    const step = Math.max(240, Math.round(scroller.clientWidth * 0.75));
    scroller.scrollBy({ left: direction * step, behavior: "smooth" });
  }

  function handlePointerDown(e: PointerEvent) {
    if (e.button !== 0 || e.pointerType === "touch" || !scroller) {
      return;
    }
    if (scroller.scrollWidth <= scroller.clientWidth) {
      return;
    }

    stopGlide();
    dragStartX = e.clientX;
    dragStartScrollLeft = scroller.scrollLeft;
    currentScrollLeft = scroller.scrollLeft;
    targetScrollLeft = scroller.scrollLeft;
    samples = [{ x: e.clientX, t: performance.now() }];
    didDrag = false;

    const onMove = (moveEvent: PointerEvent) => {
      if (!scroller) {
        return;
      }
      const now = performance.now();
      samples.push({ x: moveEvent.clientX, t: now });
      const cutoff = now - SAMPLE_WINDOW_MS;
      while (samples.length > 2 && samples[0].t < cutoff) {
        samples.shift();
      }

      const dx = moveEvent.clientX - dragStartX;
      if (!didDrag && Math.abs(dx) > DRAG_THRESHOLD) {
        didDrag = true;
        isDragging = true;
      }
      if (didDrag) {
        const rawTarget = dragStartScrollLeft - dx;
        const max = maxScrollLeft();
        targetScrollLeft = clampScroll(rawTarget);
        const over = rawTarget < 0 ? rawTarget : rawTarget > max ? rawTarget - max : 0;
        targetOverscroll = prefersReducedMotion() ? 0 : rubberBand(over);
        if (prefersReducedMotion()) {
          scroller.scrollLeft = targetScrollLeft;
        } else {
          startGlide();
        }
      }
    };

    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
      isDragging = false;
      targetOverscroll = 0;
      if (didDrag) {
        if (!prefersReducedMotion()) {
          const now = performance.now();
          const newest = samples[samples.length - 1];
          const oldest = samples[0];
          const dtMs = newest && oldest ? newest.t - oldest.t : 0;
          const stale = !newest || now - newest.t > 90;
          if (!stale && dtMs >= 8 && Math.abs(overscrollX) < 1) {
            const rawVx = -((newest.x - oldest.x) / (dtMs / 1000));
            coastVx = Math.max(-MAX_COAST_SPEED, Math.min(MAX_COAST_SPEED, rawVx));
          } else {
            coastVx = 0;
          }
          startGlide();
        }
        suppressClick = true;
        requestAnimationFrame(() => {
          suppressClick = false;
        });
      }
      samples = [];
      didDrag = false;
    };

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
  }

  function handleClickCapture(e: MouseEvent) {
    if (suppressClick) {
      e.preventDefault();
      e.stopPropagation();
      suppressClick = false;
    }
  }
</script>

<section class="flex flex-col gap-3.5" aria-label={title}>
  <div class="flex items-center justify-between gap-4">
    <h2 class="font-display text-[20px] font-bold leading-tight text-fg">
      {title}
    </h2>
    {#if canScroll}
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

  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    bind:this={scroller}
    onscroll={syncBounds}
    onpointerdown={handlePointerDown}
    onclickcapture={handleClickCapture}
    ondragstart={(e) => e.preventDefault()}
    class="scroll-none shelf-scroll flex select-none overflow-x-auto pb-2"
    class:can-scroll={canScroll}
    class:is-dragging={isDragging}
  >
    <div
      class="flex shrink-0 gap-4 will-change-transform"
      style:transform={overscrollX !== 0 || subPixelX !== 0
        ? `translate3d(${(overscrollX + subPixelX).toFixed(3)}px, 0, 0)`
        : undefined}
    >
      {@render children()}
    </div>
  </div>
</section>

<style>
  .shelf-scroll.can-scroll {
    cursor: grab;
  }
  .shelf-scroll.can-scroll :global(button),
  .shelf-scroll.can-scroll :global(a) {
    cursor: inherit;
  }
  .shelf-scroll.is-dragging,
  .shelf-scroll.is-dragging :global(*) {
    cursor: grabbing !important;
  }
</style>
