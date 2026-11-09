<script lang="ts">
  import Cover from "./Cover.svelte";
  import type { Track } from "../types";

  let {
    track,
    onplay = (_t: Track) => {},
  }: { track: Track; onplay?: (t: Track) => void } = $props();
</script>

<button
  class="flex h-[50px] w-[170px] shrink-0 items-center gap-2.5 rounded-lg bg-surface pl-1.5 pr-2 text-left transition-colors hover:bg-hover"
  title="{track.title} — {track.artist}"
  onclick={() => onplay(track)}
>
  <Cover id={track.coverId} size={40} radius={8} class="shrink-0" />
  <span class="flex min-w-0 flex-col gap-[3px]">
    <span class="truncate font-display text-[12px] font-bold leading-none">{track.title}</span>
    <span class="truncate text-[10px] leading-none text-muted">{track.artist}</span>
  </span>
</button>
