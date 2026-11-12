<script lang="ts">
  import { flip } from "svelte/animate";
  import { fade, fly } from "svelte/transition";
  import { cubicInOut } from "svelte/easing";
  import MusicNotes from "phosphor-svelte/lib/MusicNotes";
  import { clearToast, TOAST_TTL, toasts } from "../toast";
  import { cover, player } from "../store";
  import { displayTitle } from "../format";
  import Toast from "./Toast.svelte";

  // Toaster is the host for the stack: anchored at the bottom edge, oldest
  // first, so a new card pushes the older ones up. z-[60] sits above the
  // settings dialog's z-50, because a setting reports its one-shot action
  // through a card. The host ignores the pointer; the message card opts back in
  // so hover can hold it.
  const stack = $derived($toasts);
  // The art card reads the track from the live snapshot, so a track change
  // updates the title in place while the card is up.
  const nowPlaying = $derived(displayTitle($player.title, $player.path));

  const reduced =
    typeof window !== "undefined" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const shiftMs = reduced ? 120 : 180;

  // A card with another below it fades, because it has nowhere to slide to; the
  // last one slides down out of the window.
  function exit(node: HTMLElement) {
    if (node.nextElementSibling !== null) {
      return fade(node, { duration: shiftMs, easing: cubicInOut });
    }

    return fly(node, { y: reduced ? 0 : 56, duration: shiftMs, easing: cubicInOut });
  }

  // The live region reads the newest card, the one not announced yet. The node
  // is always in the DOM, since some screen readers skip one that arrives
  // already populated.
  const announcement = $derived.by(() => {
    const cur = stack.at(-1) ?? null;
    if (cur === null) {
      return "";
    }
    if (cur.kind === "bar") {
      return `${cur.label} ${cur.text ?? ""}`.trim();
    }
    if (cur.kind === "art") {
      return nowPlaying || cur.label;
    }

    return cur.label;
  });
</script>

<!--
  The region is rendered unconditionally so its text can change inside a node
  that already exists; the cards come and go within it. It takes no space and
  shows no text, so the visual cards never double up in the a11y tree.
-->
<div
  class="pointer-events-none fixed bottom-6 left-1/2 z-[60] flex -translate-x-1/2 flex-col items-center gap-2"
  role="status"
  aria-live="polite"
>
  <span class="sr-only">{announcement}</span>

  {#each stack as t (t.id)}
    <!-- A card that only changes content keeps its key and its place. -->
    <div
      class="flex justify-center"
      animate:flip={{ duration: shiftMs, easing: cubicInOut }}
      in:fly|global={{ y: reduced ? 0 : 16, duration: shiftMs, easing: cubicInOut }}
      out:exit
      aria-hidden="true"
    >
      {#if t.kind === "message"}
        <!-- The card owns its clock; a repeat under the same key remounts it,
             which restarts the bar. -->
        {#key t}
          <Toast
            message={t.label}
            icon={t.icon}
            layout={t.layout}
            duration={TOAST_TTL.message}
            onclose={() => clearToast(t.id)}
          />
        {/key}
      {:else if t.kind === "bar"}
        <!-- Interactive: the card stays mounted so the level can ease, and the
             card restarts its own clock on each new level, in step with the
             store's timer, so a held key keeps it up. -->
        <Toast
          message={t.label}
          icon={t.icon}
          value={t.text}
          progress={t.value}
          eased
          duration={TOAST_TTL.bar}
          onclose={() => clearToast(t.id)}
        />
      {:else if t.kind === "tile"}
        {#key t}
          <Toast
            message={t.label}
            icon={t.icon}
            layout="stacked"
            duration={TOAST_TTL.tile}
            onclose={() => clearToast(t.id)}
          />
        {/key}
      {:else}
        <div class="border border-line bg-surface">
          <div class="flex flex-col items-center gap-2 px-4 py-3">
            <div class="grid size-24 place-items-center overflow-hidden bg-hover">
              {#if $cover}
                <img src={$cover} alt="" class="size-full object-cover" />
              {:else}
                <MusicNotes size="40" class="text-muted" aria-hidden="true" />
              {/if}
            </div>
            <span class="max-w-48 truncate text-xs text-fg">{nowPlaying || t.label}</span>
          </div>
        </div>
      {/if}
    </div>
  {/each}
</div>
