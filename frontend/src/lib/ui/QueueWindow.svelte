<script lang="ts">
  import Play from "phosphor-svelte/lib/Play";
  import PushPin from "phosphor-svelte/lib/PushPin";
  import { fly } from "svelte/transition";
  import { cubicInOut } from "svelte/easing";
  import type { QueueRow } from "../../../bindings/github.com/dlcuy22/molow/backend/models";
  import Cover from "./Cover.svelte";

  let {
    rows = [],
    pinned = false,
    onpin = () => {},
    onplay = (_i: number) => {},
    onmove = (_from: number, _to: number) => {},
  }: {
    rows?: QueueRow[];
    pinned?: boolean;
    onpin?: () => void;
    onplay?: (index: number) => void;
    onmove?: (from: number, to: number) => void;
  } = $props();

  const ROW = 50;
  const GAP = 58;

  let dragIndex = $state<number | null>(null);
  let gapAt = $state<number | null>(null);
  let pointerY = $state(0);
  let pointerX = $state(0);
  let grabOffset = $state(0);
  let listTop = $state(0);
  let listLeft = $state(0);
  let listEl: HTMLDivElement | undefined = $state();
  let holdTimer: ReturnType<typeof setTimeout> | null = null;
  let suppressClick = false;

  function begin(e: PointerEvent, index: number) {
    if (e.button !== 0) {
      return;
    }
    suppressClick = false;
    // Click and hold: a plain click plays the row, a hold lifts it.
    grabOffset = e.clientY - (e.currentTarget as HTMLElement).getBoundingClientRect().top;
    pointerY = e.clientY;
    pointerX = e.clientX;
    const el = e.currentTarget as HTMLElement;
    const pointerId = e.pointerId;
    holdTimer = setTimeout(() => {
      holdTimer = null;
      dragIndex = index;
      gapAt = index;
      const rect = listEl?.getBoundingClientRect();
      listTop = rect?.top ?? 0;
      listLeft = rect?.left ?? pointerX - 20;
      el.setPointerCapture(pointerId);
    }, 140);
  }

  function move(e: PointerEvent) {
    if (dragIndex === null) {
      if (holdTimer !== null && Math.abs(e.clientY - pointerY) > 6) {
        clearHold();
      }

      return;
    }
    pointerY = e.clientY;
    pointerX = e.clientX;
    const raw = Math.round((e.clientY - listTop - grabOffset) / GAP);
    const max = rows.length - 1;
    gapAt = Math.min(max, Math.max(0, raw));
  }

  function end() {
    if (dragIndex !== null) {
      suppressClick = true;
      if (gapAt !== null && gapAt !== dragIndex) {
        onmove(dragIndex, gapAt);
      }
    }
    clearHold();
    dragIndex = null;
    gapAt = null;
  }

  function clearHold() {
    if (holdTimer !== null) {
      clearTimeout(holdTimer);
      holdTimer = null;
    }
  }

  const separator = (i: number) => i < rows.length - 1;
</script>

<div
  class="queue-window absolute right-0 top-[186px] w-[300px] rounded-lg bg-bg shadow-float"
  style="height: 440px"
  transition:fly={{ y: 14, duration: 180, easing: cubicInOut }}
  role="dialog"
  aria-label="Queue"
  tabindex="-1"
  data-queue
  onpointerdown={(e) => e.stopPropagation()}
>
  <header class="flex h-[30px] items-center justify-between pl-2 pr-2 pt-[2px]">
    <h2 class="font-content text-[15px] font-bold leading-none">Queue</h2>
    <button
      class="grid size-6 place-items-center text-muted transition-colors hover:text-fg"
      class:text-fg={pinned}
      aria-label={pinned ? "Unpin queue" : "Pin queue"}
      aria-pressed={pinned}
      title={pinned ? "Unpin queue" : "Pin queue"}
      onclick={() => onpin()}
    >
      <PushPin size="16" weight={pinned ? "fill" : "regular"} />
    </button>
  </header>

  <div
    bind:this={listEl}
    class="scroll-thin relative mx-auto mt-2 w-[280px] select-none overflow-y-auto"
    style="max-height: 400px; height: {Math.max(0, rows.length - (dragIndex === null ? 0 : 1)) * GAP + (dragIndex === null ? 0 : GAP)}px"
  >
    {#each rows as row, i (row.index + ":" + row.path)}
      {#if dragIndex !== null && gapAt === i}
        <div style="height:{GAP}px" aria-hidden="true"></div>
      {/if}
      {#if dragIndex !== i}
        <div
          class="group relative w-[280px]"
          style="height:{ROW}px;margin-bottom:{separator(i) ? GAP - ROW : 0}px"
        >
          <button
            class="absolute inset-0 flex items-center gap-2.5 rounded-lg pl-1.5 pr-2 text-left transition-colors hover:bg-hover"
            class:bg-surface={row.active}
            onclick={() => {
              if (!suppressClick) {
                onplay(row.index);
              }
              suppressClick = false;
            }}
            onpointerdown={(e) => begin(e, i)}
            onpointermove={move}
            onpointerup={end}
            onpointercancel={end}
          >
            <span class="relative shrink-0">
              <Cover id={row.coverId} size={40} radius={8} />
              {#if row.active}
                <span class="absolute inset-0 grid place-items-center rounded-lg bg-bg-dark/55">
                  <Play size="16" weight="fill" class="text-fg" />
                </span>
              {/if}
            </span>
            <span class="flex min-w-0 flex-col gap-[3px]">
              <span
                class="truncate font-display text-[12px] font-bold leading-none"
                class:text-[#a5c8f3]={row.active}
              >
                {row.title}
              </span>
              <span class="truncate text-[10px] leading-none text-muted">{row.artist}</span>
            </span>
          </button>
          {#if separator(i)}
            <span class="absolute inset-x-0 h-px bg-line" style="top:{GAP - 1}px"></span>
          {/if}
        </div>
      {/if}
    {/each}

    {#if rows.length === 0}
      <p class="px-2 pt-3 text-[12px] text-muted">Nothing queued</p>
    {/if}

    {#if dragIndex !== null && dragIndex < rows.length}
      <div
        class="pointer-events-none fixed z-50 flex h-[50px] w-[280px] items-center gap-2.5 rounded-lg bg-surface pl-1.5 pr-2 opacity-90 shadow-lift"
        style="left:{listLeft}px;top:{pointerY - grabOffset}px"
        aria-hidden="true"
      >
        <Cover id={rows[dragIndex].coverId} size={40} radius={8} />
        <span class="flex min-w-0 flex-col gap-[3px]">
          <span class="truncate font-display text-[12px] font-bold leading-none">
            {rows[dragIndex].title}
          </span>
          <span class="truncate text-[10px] leading-none text-muted">{rows[dragIndex].artist}</span>
        </span>
      </div>
    {/if}
  </div>
</div>
