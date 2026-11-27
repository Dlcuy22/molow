<script lang="ts">
  // Drives the WebGL2 animated backdrop (backdrop-gl.ts) and crossfade loop
  // used in AlbumPlayer.svelte, ported from ytm-rs's cover backdrop pipeline
  // (https://github.com/grapedjuice/ytm-rs, GPL-3.0-only).
  import { untrack } from "svelte";
  import { clampBackdropSize, createBackdropGL, type BackdropGL } from "../backdrop-gl";

  let {
    src,
    active = false,
    useShader = false,
    dim = 0.85,
  }: {
    src: string;
    active?: boolean;
    useShader?: boolean;
    dim?: number;
  } = $props();

  const FRAME_MS = 1000 / 30;
  const CROSSFADE_MS = 1400;

  let host: HTMLDivElement | undefined = $state();
  let canvas: HTMLCanvasElement | undefined = $state();
  let glFailed = $state(false);
  let displaySrc = $state("");

  const renderStatic = $derived(!useShader || glFailed);

  let gl: BackdropGL | null = null;
  let curSlot: 0 | 1 = 0;
  let hasTexture = false;
  let lastLoadedImg: HTMLImageElement | null = null;
  let blend = 1;
  let fadeStart = 0;
  let animTime = 0;
  let lastTick = 0;
  let accum = 0;
  let rafId: number | undefined;
  let resizeTimer: ReturnType<typeof setTimeout> | undefined;
  let restoreAttempts = 0;
  let loadSeq = 0;
  let prefersReducedMotion = false;

  function stopLoop() {
    if (rafId !== undefined) {
      cancelAnimationFrame(rafId);
      rafId = undefined;
    }
    lastTick = 0;
    accum = 0;
  }

  function paintNow(now: number) {
    if (!gl || !hasTexture) return;
    if (blend < 1) {
      blend = prefersReducedMotion ? 1 : Math.min(1, (now - fadeStart) / CROSSFADE_MS);
    }
    gl.draw({ time: animTime, blend, curSlot, dim });
  }

  function shouldAnimate(): boolean {
    if (!gl || !hasTexture || document.hidden || prefersReducedMotion) return false;
    return active || blend < 1;
  }

  function syncLoop() {
    if (!shouldAnimate()) {
      stopLoop();
      return;
    }
    if (rafId !== undefined) return;
    lastTick = performance.now();
    accum = 0;

    const tick = (now: number) => {
      rafId = undefined;
      if (!shouldAnimate()) {
        stopLoop();
        return;
      }
      const dt = Math.min(100, Math.max(0, now - lastTick));
      lastTick = now;
      accum += dt;

      if (accum >= FRAME_MS) {
        const step = accum;
        accum %= FRAME_MS;
        if (active) {
          animTime += step / 1000;
        }
        paintNow(now);
      }

      if (shouldAnimate()) {
        rafId = requestAnimationFrame(tick);
      } else {
        stopLoop();
      }
    };
    rafId = requestAnimationFrame(tick);
  }

  function applyCanvasSize() {
    if (!gl || !host) return;
    const rect = host.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const { width, height } = clampBackdropSize(rect.width, rect.height, window.devicePixelRatio || 1);
    gl.resize(width, height);
    paintNow(performance.now());
  }

  function initGL(el: HTMLCanvasElement) {
    gl?.dispose();
    gl = createBackdropGL(el, {
      onContextLost: () => {
        stopLoop();
      },
      onContextRestored: () => {
        if (restoreAttempts >= 1) {
          gl?.dispose();
          gl = null;
          glFailed = true;
          return;
        }
        restoreAttempts++;
        initGL(el);
      },
    });
    if (!gl) {
      glFailed = true;
      return;
    }
    applyCanvasSize();
    if (lastLoadedImg) {
      gl.setTexture(0, lastLoadedImg);
      gl.setTexture(1, lastLoadedImg);
      curSlot = 0;
      blend = 1;
      hasTexture = true;
      paintNow(performance.now());
      syncLoop();
    }
  }

  // Mount and tear down WebGL2 only while shader mode is active.
  $effect(() => {
    const el = canvas;
    if (renderStatic || !el) {
      stopLoop();
      gl?.dispose();
      gl = null;
      return;
    }

    const mq = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    prefersReducedMotion = mq?.matches ?? false;
    const onMotionChange = () => {
      prefersReducedMotion = mq?.matches ?? false;
      if (prefersReducedMotion && blend < 1) {
        blend = 1;
        paintNow(performance.now());
      }
      syncLoop();
    };
    const onVisibility = () => syncLoop();

    mq?.addEventListener?.("change", onMotionChange);
    document.addEventListener("visibilitychange", onVisibility);

    untrack(() => initGL(el));

    const ro =
      typeof ResizeObserver !== "undefined" && host
        ? new ResizeObserver(() => {
            clearTimeout(resizeTimer);
            resizeTimer = setTimeout(applyCanvasSize, 80);
          })
        : null;
    if (ro && host) ro.observe(host);

    return () => {
      clearTimeout(resizeTimer);
      ro?.disconnect();
      mq?.removeEventListener?.("change", onMotionChange);
      document.removeEventListener("visibilitychange", onVisibility);
      stopLoop();
      gl?.dispose();
      gl = null;
    };
  });

  // Load each new backdrop image and crossfade (or swap in static mode).
  $effect(() => {
    const nextSrc = src;
    if (!nextSrc) return;
    const seq = ++loadSeq;
    const img = new Image();
    img.decoding = "async";
    img.onload = () => {
      if (seq !== loadSeq) return;
      lastLoadedImg = img;
      displaySrc = nextSrc;
      if (!gl) return;
      if (!hasTexture) {
        gl.setTexture(0, img);
        gl.setTexture(1, img);
        curSlot = 0;
        blend = 1;
        hasTexture = true;
      } else {
        const nextSlot: 0 | 1 = curSlot === 0 ? 1 : 0;
        gl.setTexture(nextSlot, img);
        curSlot = nextSlot;
        blend = prefersReducedMotion ? 1 : 0;
        fadeStart = performance.now();
      }
      paintNow(performance.now());
      syncLoop();
    };
    img.src = nextSrc;
  });

  // Start/stop the animation loop as playback or visibility changes.
  $effect(() => {
    void active;
    void dim;
    if (gl && hasTexture) {
      paintNow(performance.now());
      syncLoop();
    }
  });
</script>

<div bind:this={host} class="pointer-events-none absolute inset-0" aria-hidden="true">
  {#if renderStatic}
    {#if displaySrc}
      <div
        class="h-full w-full bg-cover bg-center transition-[background-image] duration-700"
        style={`background-image:url("${displaySrc}");filter:brightness(${dim});`}
      ></div>
    {/if}
  {:else}
    <canvas bind:this={canvas} class="block h-full w-full"></canvas>
  {/if}
</div>
