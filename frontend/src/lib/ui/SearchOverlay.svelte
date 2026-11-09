<script lang="ts">
  import { MagnifyingGlass } from "phosphor-svelte";
  import { fly } from "svelte/transition";
  import { cubicInOut } from "svelte/easing";
  import Cover from "./Cover.svelte";
  import { searchLibrary, searchPage } from "../library";
  import { formatTime } from "../format";
  import type { Track } from "../types";

  let {
    left = 12,
    onclose = () => {},
    onplay = (_t: Track) => {},
  }: { left?: number; onclose?: () => void; onplay?: (t: Track) => void } = $props();

  let query = $state("");
  let selected = $state(0);
  let input: HTMLInputElement | undefined = $state();

  const results = $derived($searchPage.shelves.flatMap((s) => s.items));

  $effect(() => {
    input?.focus();
  });

  // One request per settled query. The store owns the call; this only owns the
  // pause between keys.
  $effect(() => {
    const q = query;
    const timer = setTimeout(() => {
      void searchLibrary(q);
      selected = 0;
    }, 160);

    return () => clearTimeout(timer);
  });

  function key(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      onclose();

      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      selected = Math.min(results.length - 1, selected + 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      selected = Math.max(0, selected - 1);
    } else if (e.key === "Enter" && results[selected]) {
      e.preventDefault();
      onplay(results[selected]);
      onclose();
    }
  }
</script>

<div
  class="absolute z-40 w-[380px] rounded-lg bg-surface shadow-menu"
  style="left:{left}px; top:42px"
  transition:fly={{ y: -6, duration: 140, easing: cubicInOut }}
  role="dialog"
  aria-label="Search"
  tabindex="-1"
  data-search
  onpointerdown={(e) => e.stopPropagation()}
>
  <div class="flex h-[52px] items-center gap-3 px-4">
    <MagnifyingGlass size="18" class="shrink-0 text-muted" />
    <input
      bind:this={input}
      bind:value={query}
      class="min-w-0 flex-1 bg-transparent text-[14px] text-fg outline-none placeholder:text-muted focus-visible:outline-none"
      style="outline: none"
      placeholder="Search your library"
      aria-label="Search your library"
      onkeydown={key}
    />
    <kbd class="text-[10px] text-muted">Esc</kbd>
  </div>

  <div class="h-px bg-line"></div>

  <div class="scroll-thin h-[254px] overflow-y-auto p-2">
    {#if query.trim() === ""}
      <p class="px-2 py-2 text-[11px] text-muted">Songs, albums and artists in this library.</p>
    {:else if results.length === 0}
      <p class="px-2 py-2 text-[11px] text-muted">No matches for “{query}”.</p>
    {:else}
      <h3 class="px-2 pb-1 pt-1 text-[10px] font-medium uppercase tracking-[0.06em] text-muted">
        Songs
      </h3>
      {#each results as row, i (row.ref)}
        <button
          class="flex h-11 w-full items-center gap-2 rounded-md pl-2 pr-2 text-left"
          class:bg-selected={i === selected}
          onmouseenter={() => (selected = i)}
          onclick={() => {
            onplay(row);
            onclose();
          }}
        >
          <Cover id={row.coverId} size={32} radius={4} class="shrink-0" />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="truncate text-[12px] font-medium leading-none">{row.title}</span>
            <span class="truncate text-[10px] leading-none text-muted">{row.artist}</span>
          </span>
          <span class="shrink-0 text-[10px] tabular-nums text-muted">{formatTime(row.durationMs)}</span>
        </button>
      {/each}
    {/if}
  </div>

  <div class="h-px bg-line"></div>

  <div class="flex h-8 items-center justify-center text-[10px] text-muted">
    Enter to play · Esc to close
  </div>
</div>
