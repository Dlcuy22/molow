<script lang="ts">
  import MusicNotes from "phosphor-svelte/lib/MusicNotes";
  import { coverUrls, requestCover } from "../covers";

  let {
    id = "",
    alt = "",
    size = 40,
    radius = 8,
    fill = false,
    class: cls = "",
  }: {
    id?: string;
    alt?: string;
    size?: number;
    radius?: number;
    fill?: boolean;
    class?: string;
  } = $props();

  const src = $derived(id === "" ? "" : ($coverUrls[id] ?? ""));
  const style = $derived(
    fill
      ? `width:100%;height:auto;aspect-ratio:1;border-radius:${radius}px`
      : `width:${size}px;height:${size}px;border-radius:${radius}px`,
  );

  $effect(() => {
    if (id !== "") {
      requestCover(id);
    }
  });
</script>

{#if src}
  <img
    {src}
    {alt}
    class="{cls} object-cover"
    style={style}
    draggable="false"
  />
{:else}
  <div
    class="{cls} grid place-items-center bg-surface text-muted"
    style={style}
    aria-hidden="true"
  >
    <MusicNotes size={Math.max(12, Math.round(size * 0.42))} />
  </div>
{/if}
