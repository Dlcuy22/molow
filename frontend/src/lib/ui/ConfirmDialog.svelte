<script lang="ts">
  import { onMount } from "svelte";

  let {
    title,
    body,
    confirmLabel = "Delete",
    onconfirm,
    oncancel,
  }: {
    title: string;
    body: string;
    confirmLabel?: string;
    onconfirm: () => void;
    oncancel: () => void;
  } = $props();

  let cancel = $state<HTMLButtonElement | null>(null);

  // Focus lands on Cancel, so a stray Enter keeps the row it was aiming at.
  onMount(() => cancel?.focus());

  function key(e: KeyboardEvent) {
    e.stopPropagation();
    if (e.key === "Escape") {
      e.preventDefault();
      oncancel();
    }
  }
</script>

<div class="fixed inset-0 z-50 grid place-items-center bg-black/40" role="presentation">
  <div
    class="w-[min(360px,calc(100vw-32px))] rounded-lg border border-line bg-surface p-4 shadow-menu"
    role="dialog"
    aria-modal="true"
    aria-label={title}
    tabindex="-1"
    onkeydown={key}
  >
    <h2 class="text-sm font-medium text-fg">{title}</h2>
    <p class="mt-2 text-xs text-muted">{body}</p>
    <div class="mt-4 flex justify-end gap-2">
      <button
        bind:this={cancel}
        class="h-7 rounded-md px-3 text-[12px] text-fg transition-colors hover:bg-hover"
        onclick={oncancel}
      >
        Cancel
      </button>
      <button
        class="h-7 rounded-md bg-danger px-3 text-[12px] font-medium text-fg transition-opacity hover:opacity-90"
        onclick={onconfirm}
      >
        {confirmLabel}
      </button>
    </div>
  </div>
</div>
