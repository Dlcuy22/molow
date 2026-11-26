<script lang="ts">
  import MusicNotes from "phosphor-svelte/lib/MusicNotes";
  import { artUrl, resizeRemoteArtUrl } from "../artwork";

  let {
    id = "",
    src: srcOverride = "",
    alt = "",
    size = 40,
    radius = 8,
    fill = false,
    class: cls = "",
  }: {
    id?: string;
    src?: string;
    alt?: string;
    size?: number;
    radius?: number;
    fill?: boolean;
    class?: string;
  } = $props();

  const src = $derived(
    srcOverride ? resizeRemoteArtUrl(srcOverride, size) : artUrl(id, size),
  );
  const style = $derived(
    fill
      ? `width:100%;height:auto;aspect-ratio:1;border-radius:${radius}px`
      : `width:${size}px;height:${size}px;border-radius:${radius}px`,
  );

  let failed = $state(false);
  $effect(() => {
    void src;
    failed = false;
  });
</script>

{#if src && !failed}
  <img
    {src}
    {alt}
    loading="lazy"
    decoding="async"
    class="{cls} object-cover"
    style={style}
    draggable="false"
    onerror={() => {
      failed = true;
    }}
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
