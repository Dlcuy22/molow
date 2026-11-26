<script lang="ts">
  import User from "phosphor-svelte/lib/User";
  import { artUrl, resizeRemoteArtUrl } from "../artwork";
  import type { Artist } from "../types";

  let {
    artist,
    subtitle = "",
    onopen,
  }: {
    artist: Artist;
    subtitle?: string;
    onopen?: (artist: Artist) => void;
  } = $props();

  const src = $derived(
    artist.imageUrl
      ? resizeRemoteArtUrl(artist.imageUrl, 166)
      : artUrl(artist.coverId, 166),
  );
  const sub = $derived(subtitle || artist.subtitle || "");

  let failed = $state(false);
  $effect(() => {
    void src;
    failed = false;
  });
</script>

{#snippet avatar(hoverable: boolean)}
  <span class="relative block size-[166px] shrink-0 overflow-hidden rounded-full bg-surface">
    {#if src && !failed}
      <img
        {src}
        alt=""
        loading="lazy"
        decoding="async"
        draggable="false"
        class={hoverable
          ? "size-full object-cover transition-[filter] duration-150 group-hover:brightness-110"
          : "size-full object-cover"}
        onerror={() => {
          failed = true;
        }}
      />
    {:else}
      <span class="grid size-full place-items-center text-muted" aria-hidden="true">
        <User size="52" weight="regular" />
      </span>
    {/if}
  </span>
{/snippet}

{#if onopen}
  <button
    type="button"
    class="group flex w-[166px] shrink-0 cursor-pointer flex-col items-center rounded-lg text-center focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
    aria-label={artist.name}
    onclick={() => onopen(artist)}
  >
    {@render avatar(true)}
    <span class="mt-2.5 w-full truncate font-display text-[15px] font-bold leading-snug text-fg">
      {artist.name}
    </span>
    {#if sub !== ""}
      <span class="mt-0.5 w-full truncate text-[12px] leading-snug text-muted">
        {sub}
      </span>
    {/if}
  </button>
{:else}
  <div class="flex w-[166px] shrink-0 flex-col items-center text-center">
    {@render avatar(false)}
    <p class="mt-2.5 w-full truncate font-display text-[15px] font-bold leading-snug text-fg">
      {artist.name}
    </p>
    {#if sub !== ""}
      <p class="mt-0.5 w-full truncate text-[12px] leading-snug text-muted">
        {sub}
      </p>
    {/if}
  </div>
{/if}
