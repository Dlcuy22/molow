<script lang="ts">
  import { onMount } from "svelte";
  import WarningCircle from "phosphor-svelte/lib/WarningCircle";
  import {
    authBusy,
    authDialog,
    authError,
    closeAuthDialog,
    keyringError,
    reSignInWithCookieHeader,
    reSignInWithWindow,
  } from "../auth";

  let cookieInput = $state("");
  let browserBtn = $state<HTMLButtonElement | null>(null);

  onMount(() => {
    browserBtn?.focus();
  });

  const target = $derived($authDialog);
  const blockedByKeyring = $derived($keyringError !== "");

  function handleKeydown(e: KeyboardEvent) {
    e.stopPropagation();
    if (e.key === "Escape" && !$authBusy) {
      e.preventDefault();
      closeAuthDialog();
    }
  }

  function handleBackdropPointerDown(e: PointerEvent) {
    if (e.target === e.currentTarget && !$authBusy) {
      closeAuthDialog();
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
      class="motion-dialog w-[min(440px,calc(100vw-32px))] rounded-lg border border-line bg-surface p-5 shadow-menu"
      role="dialog"
      aria-modal="true"
      aria-labelledby="ytm-auth-title"
      aria-describedby="ytm-auth-subtitle"
      tabindex="-1"
      onkeydown={handleKeydown}
    >
      <h2 id="ytm-auth-title" class="font-display text-[20px] font-bold leading-tight text-fg">
        Reconnect {target.libraryName}
      </h2>
      <p id="ytm-auth-subtitle" class="mt-2 text-[12px] leading-relaxed text-muted">
        Sign in with the same YouTube account linked to this library. Session cookies are encrypted
        locally using your system keyring.
      </p>

      {#if blockedByKeyring}
        <div
          class="mt-4 flex items-start gap-2 rounded-[6px] border border-danger bg-bg px-3 py-2.5 text-[12px] text-fg"
          role="alert"
        >
          <WarningCircle size="16" class="mt-0.5 shrink-0 text-danger" aria-hidden="true" />
          <div class="min-w-0">
            <p class="font-medium">System keyring unavailable</p>
            <p class="mt-0.5 leading-relaxed text-muted">{$keyringError}</p>
          </div>
        </div>
      {:else if $authError !== ""}
        <div
          class="mt-4 flex items-start gap-2 rounded-[6px] border border-danger bg-bg px-3 py-2 text-[12px] text-fg"
          role="alert"
        >
          <WarningCircle size="16" class="mt-0.5 shrink-0 text-danger" aria-hidden="true" />
          <span>{$authError}</span>
        </div>
      {/if}

      <div class="mt-4 flex flex-col gap-3">
        <button
          bind:this={browserBtn}
          type="button"
          class="h-9 w-full rounded-[6px] bg-fg px-3 text-[12px] font-medium text-bg-dark transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={$authBusy || blockedByKeyring}
          onclick={() => void reSignInWithWindow()}
        >
          {$authBusy ? "Waiting for sign-in..." : "Sign in with YouTube"}
        </button>

        <details class="rounded-[6px] border border-line/70 bg-bg/60">
          <summary
            class="cursor-pointer select-none px-3 py-2 text-[12px] text-muted transition-colors hover:text-fg focus:text-fg focus:outline-none"
          >
            Having trouble? Paste cookie header
          </summary>
          <form
            class="flex flex-col gap-2.5 border-t border-line/70 p-3"
            onsubmit={(e) => {
              e.preventDefault();
              void reSignInWithCookieHeader(cookieInput);
            }}
          >
            <textarea
              bind:value={cookieInput}
              rows="3"
              class="resize-none rounded-[6px] border border-line bg-surface px-2.5 py-2 text-[11px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus:border-muted focus:outline-none"
              placeholder="SAPISID=...; SID=...; __Secure-3PSID=..."
              spellcheck="false"
              disabled={$authBusy || blockedByKeyring}
            ></textarea>
            <div class="flex justify-end">
              <button
                type="submit"
                class="h-8 rounded-[6px] border border-line bg-surface px-3 text-[12px] text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
                disabled={$authBusy || blockedByKeyring || cookieInput.trim() === ""}
              >
                {$authBusy ? "Verifying..." : "Verify & save"}
              </button>
            </div>
          </form>
        </details>
      </div>

      <div class="mt-4 flex justify-end">
        <button
          type="button"
          class="h-8 rounded-[6px] px-3 text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={$authBusy}
          onclick={closeAuthDialog}
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
{/if}
