<script lang="ts">
  import { onMount } from "svelte";

  // A one-field dialog for Rename. It is a real dialog rather than window.prompt
  // because the webview's prompt is unstyled and blocks the UI thread.
  let {
    title,
    label,
    value = "",
    onsubmit,
    oncancel,
  }: {
    title: string;
    label: string;
    value?: string;
    onsubmit: (value: string) => void;
    oncancel: () => void;
  } = $props();

  let text = $state("");
  let field = $state<HTMLInputElement | null>(null);

  onMount(() => {
    if (!field) {
      return;
    }
    text = field.value;
    field.focus();
    field.select();
  });

  const trimmed = $derived(text.trim());
  const canSubmit = $derived(trimmed !== "" && trimmed !== value);

  function submit() {
    if (canSubmit) {
      onsubmit(trimmed);
    }
  }

  function key(e: KeyboardEvent) {
    // Escape belongs to the dialog, not to the sheet behind it.
    e.stopPropagation();
    if (e.key === "Escape") {
      e.preventDefault();
      oncancel();
    } else if (e.key === "Enter") {
      e.preventDefault();
      submit();
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
    <label class="mt-3 flex flex-col gap-1 text-xs text-muted">
      {label}
      <input
        bind:this={field}
        value={value}
        oninput={(e) => (text = e.currentTarget.value)}
        class="rounded-[4px] border border-line bg-bg px-2.5 py-1.5 text-xs text-fg transition-colors hover:bg-hover"
        type="text"
        autocomplete="off"
        spellcheck="false"
        maxlength="120"
      />
    </label>
    <div class="mt-4 flex justify-end gap-2">
      <button
        class="h-7 rounded-md px-3 text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg"
        onclick={oncancel}
      >
        Cancel
      </button>
      <button
        class="h-7 rounded-md bg-fg px-3 text-[12px] font-medium text-bg-dark transition-opacity disabled:opacity-40"
        disabled={!canSubmit}
        onclick={submit}
      >
        Save
      </button>
    </div>
  </div>
</div>
