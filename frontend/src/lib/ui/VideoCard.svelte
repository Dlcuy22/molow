<script lang="ts">
  import FilmStrip from "phosphor-svelte/lib/FilmStrip";
  import CardPlayButton from "./CardPlayButton.svelte";
  import { formatTime } from "../format";
  import type { YtmVideo } from "../types";

  let {
    video,
    onplay = (_v: YtmVideo) => {},
  }: {
    video: YtmVideo;
    onplay?: (v: YtmVideo) => void;
  } = $props();

  const src = $derived(
    video.thumbnailUrl ||
      (video.coverId ? `/art/${encodeURIComponent(video.coverId)}` : ""),
  );
  const meta = $derived.by(() => {
    const parts: string[] = [];
    if (video.subtitle) {
      parts.push(video.subtitle);
    }
    if (video.durationMs && video.durationMs > 0) {
      parts.push(formatTime(video.durationMs));
    }

    return parts.join(" · ");
  });

  let failed = $state(false);
  $effect(() => {
    void src;
    failed = false;
  });
</script>

<div class="group w-[240px] shrink-0">
  <button
    type="button"
    class="relative block aspect-video w-[240px] cursor-pointer overflow-hidden rounded-lg bg-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
    aria-label="Play {video.title}"
    onclick={() => onplay(video)}
  >
    {#if src && !failed}
      <img
        {src}
        alt=""
        loading="lazy"
        decoding="async"
        draggable="false"
        class="size-full object-cover transition-opacity group-hover:opacity-90"
        onerror={() => {
          failed = true;
        }}
      />
    {:else}
      <span class="grid size-full place-items-center text-muted" aria-hidden="true">
        <FilmStrip size="36" />
      </span>
    {/if}
    <CardPlayButton onplay={() => onplay(video)} label="Play {video.title}" />
  </button>

  <div class="pt-1.5">
    <button
      type="button"
      class="block max-w-full cursor-pointer truncate text-left font-display text-[15px] font-bold leading-tight text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
      onclick={() => onplay(video)}
    >
      {video.title}
    </button>
    {#if meta !== ""}
      <p class="mt-0.5 truncate text-[11px] tabular-nums text-muted">
        {meta}
      </p>
    {/if}
  </div>
</div>
