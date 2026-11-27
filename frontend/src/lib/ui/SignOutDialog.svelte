<script lang="ts">
  import { onMount } from "svelte";
  import { authBusy, closeSignOutDialog, signOutDialog, signOutLibrary } from "../auth";

  let deleteLibrary = $state(false);
  let cancelBtn = $state<HTMLButtonElement | null>(null);

  onMount(() => {
    cancelBtn?.focus();
  });

  const target = $derived($signOutDialog);

  function handleKeydown(e: KeyboardEvent) {
    e.stopPropagation();
    if (e.key === "Escape" && !$authBusy) {
      e.preventDefault();
      closeSignOutDialog();
    }
  }

  function handleBackdropPointerDown(e: PointerEvent) {
    if (e.target === e.currentTarget && !$authBusy) {
      closeSignOutDialog();
    }
  }
</script>

{#if target}
  <div
    class="motion-scrim fixed inset-0 z-[60] grid place-items-center bg-black/50 p-4"
    role="presentation"
    onpointerdown={handleBackdropPointerDown}
  >
    <div
      class="motion-dialog w-[min(390px,calc(100vw-32px))] rounded-lg border border-line bg-surface p-5 shadow-menu"
      role="dialog"
      aria-modal="true"
      aria-labelledby="signout-dialog-title"
      aria-describedby="signout-dialog-desc"
      tabindex="-1"
      onkeydown={handleKeydown}
    >
      <h2 id="signout-dialog-title" class="text-sm font-medium text-fg">
        Sign out of {target.libraryName}?
      </h2>
      <p id="signout-dialog-desc" class="mt-2 text-[12px] leading-relaxed text-muted">
        You can keep cached Liked Music and playlists for offline browsing, or remove this library
        from Molow.
      </p>

      <label
        class="mt-4 flex cursor-pointer items-start gap-2.5 rounded-[6px] border border-line/70 bg-bg/60 px-3 py-2.5 text-[12px] text-fg select-none"
      >
        <input
          type="checkbox"
          bind:checked={deleteLibrary}
          disabled={$authBusy}
          class="mt-0.5 size-3.5 shrink-0 accent-fg"
        />
        <span class="leading-snug">
          Also remove cached library from Molow
        </span>
      </label>

      <div class="mt-4 flex justify-end gap-2">
        <button
          bind:this={cancelBtn}
          type="button"
          class="h-8 rounded-[6px] px-3 text-[12px] text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={$authBusy}
          onclick={closeSignOutDialog}
        >
          Cancel
        </button>
        <button
          type="button"
          class="h-8 rounded-[6px] bg-danger px-3 text-[12px] font-medium text-white transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={$authBusy}
          onclick={() => void signOutLibrary(deleteLibrary)}
        >
          {$authBusy ? "Signing out..." : "Sign out"}
        </button>
      </div>
    </div>
  </div>
{/if}
