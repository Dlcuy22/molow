<script lang="ts">
  import { Browser } from "@wailsio/runtime";
  import DiscordLogo from "phosphor-svelte/lib/DiscordLogo";
  import Cover from "../ui/Cover.svelte";
  import { discordStatus, player } from "../store";
  import { displayArtist, displayTitle, formatTime, isRemoteRef, ytmVideoId } from "../format";

  // The card mirrors what Discord draws from the activity: the verb and the
  // artist on the header line, the cover with the artist avatar as a small
  // badge, the title and artist, the progress bar, and the two links. It reads
  // the same snapshot the rest of the window does, so what it shows is what the
  // presence will send.
  const snap = $derived($player);

  const title = $derived(displayTitle(snap.title, snap.path));
  const isRemote = $derived(isRemoteRef(snap.path));
  const listeningTo = $derived(displayArtist(snap.artist) || "Unknown artist");

  // The listen link exists only for a remote track; a local file has no watch
  // page, so the button is not offered rather than pointing nowhere.
  const listenUrl = $derived(
    isRemote ? `https://music.youtube.com/watch?v=${ytmVideoId(snap.path)}` : "",
  );
  const projectUrl = "https://github.com/dlcuy22/molo";

  const progress = $derived(
    snap.duration > 0 ? Math.min(1, Math.max(0, snap.position / snap.duration)) : 0,
  );

  const avatarSrc = $derived(
    snap.artistId !== "" ? `/art/avatar/${encodeURIComponent(snap.artistId)}` : "",
  );
  let avatarFailed = $state(false);
  $effect(() => {
    void avatarSrc;
    avatarFailed = false;
  });

  function open(url: string) {
    if (url !== "") {
      void Browser.OpenURL(url);
    }
  }
</script>

<!--
  The preview of the card Discord publishes. Rich Presence is invisible from
  inside the app, so showing what Discord will draw is the only honest way to
  let a listener confirm it before opening their profile. The card is dimmed
  while presence is off, so it reads as "this is what it will look like" rather
  than a live state.
-->
<div
  class="rounded-[6px] border border-line bg-bg/60 p-3 transition-opacity"
  class:opacity-50={!$discordStatus.enabled}
  inert={!$discordStatus.enabled}
>
  {#if title === ""}
    <p class="py-6 text-center text-xs text-muted">
      Play a track to see the Discord card.
    </p>
  {:else}
    <p class="text-[11px] font-semibold uppercase tracking-wide text-muted">
      Listening to {listeningTo}
    </p>

    <div class="mt-2 flex items-start gap-3">
      <div class="relative size-[72px] shrink-0">
        <Cover id={snap.coverId} alt="" size={72} radius={4} class="border border-line" />
        {#if isRemote}
          <span
            class="absolute -bottom-1 -right-1 grid size-6 place-items-center rounded-full border-2 border-bg bg-surface"
          >
            {#if avatarSrc && !avatarFailed}
              <img
                src={avatarSrc}
                alt=""
                loading="lazy"
                decoding="async"
                class="size-full rounded-full object-cover"
                onerror={() => {
                  avatarFailed = true;
                }}
              />
            {:else}
              <DiscordLogo size="12" class="text-muted" aria-hidden="true" />
            {/if}
          </span>
        {/if}
      </div>

      <div class="min-w-0 flex-1 pt-0.5">
        <p class="truncate text-sm font-semibold text-fg" title={title}>{title}</p>
        <p class="truncate text-xs text-muted">{listeningTo}</p>
        {#if snap.album.trim() !== ""}
          <p class="truncate text-[11px] text-muted">{snap.album}</p>
        {/if}
        {#if snap.duration > 0}
          <p class="mt-1 text-[11px] tabular-nums text-muted">
            {formatTime(snap.position)} / {formatTime(snap.duration)}
          </p>
          <span class="mt-1 block h-[3px] w-full overflow-hidden rounded-full bg-track">
            <span
              class="block h-full rounded-full bg-accent transition-[width] duration-150 ease-linear"
              style="width: {progress * 100}%"
            ></span>
          </span>
        {/if}
      </div>
    </div>

    <div class="mt-3 flex flex-col gap-1.5">
      {#if listenUrl !== ""}
        <button
          class="w-full rounded-[4px] border border-line bg-surface px-2 py-1.5 text-xs text-fg transition-colors hover:bg-hover"
          onclick={() => open(listenUrl)}
        >
          Listen on YouTube Music
        </button>
      {/if}
      <button
        class="w-full rounded-[4px] border border-line bg-surface px-2 py-1.5 text-xs text-fg transition-colors hover:bg-hover"
        onclick={() => open(projectUrl)}
      >
        Visit molo
      </button>
    </div>
  {/if}
</div>
