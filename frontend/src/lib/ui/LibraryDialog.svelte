<script lang="ts">
  import { onMount } from "svelte";
  import WarningCircle from "phosphor-svelte/lib/WarningCircle";

  let {
    mode = "create",
    initialName = "",
    initialDescription = "",
    busy = false,
    error = "",
    onsubmit,
    oncancel,
  }: {
    mode?: "create" | "edit";
    initialName?: string;
    initialDescription?: string;
    busy?: boolean;
    error?: string;
    onsubmit: (name: string, description: string) => void;
    oncancel: () => void;
  } = $props();

  let name = $state("");
  let description = $state("");
  let nameField = $state<HTMLInputElement | null>(null);

  onMount(() => {
    name = initialName;
    description = initialDescription;
    if (nameField) {
      nameField.focus();
      if (mode === "edit") {
        nameField.select();
      }
    }
  });

  const title = $derived(mode === "edit" ? "Edit library" : "Let's create a library");
  const subtitle = $derived(
    mode === "edit"
      ? "Update this library's name or description."
      : "Library is where you store and manage your albums, folders with songs, or YouTube Music tracks and playlists.",
  );
  const submitLabel = $derived(
    busy
      ? mode === "edit"
        ? "Saving..."
        : "Creating..."
      : mode === "edit"
        ? "Save"
        : "Create library",
  );

  const trimmedName = $derived(name.trim());
  const trimmedDesc = $derived(description.trim());
  const unchanged = $derived(
    mode === "edit" && trimmedName === initialName.trim() && trimmedDesc === initialDescription.trim(),
  );
  const canSubmit = $derived(!busy && trimmedName !== "" && !unchanged);

  function submit() {
    if (canSubmit) {
      onsubmit(trimmedName, trimmedDesc);
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    // Keep keyboard navigation inside the dialog instead of triggering player shortcuts.
    e.stopPropagation();
    if (e.key === "Escape" && !busy) {
      e.preventDefault();
      oncancel();
    } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      submit();
    }
  }

  function handleNameKeydown(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      submit();
    }
  }

  function handleBackdropPointerDown(e: PointerEvent) {
    if (e.target === e.currentTarget && !busy) {
      oncancel();
    }
  }
</script>

<div
  class="motion-scrim fixed inset-0 z-50 grid place-items-center bg-black/50 p-4"
  role="presentation"
  onpointerdown={handleBackdropPointerDown}
>
  <div
    class="motion-dialog w-[min(420px,calc(100vw-32px))] rounded-lg border border-line bg-surface p-5 shadow-menu"
    role="dialog"
    aria-modal="true"
    aria-labelledby="library-dialog-title"
    aria-describedby="library-dialog-subtitle"
    tabindex="-1"
    onkeydown={handleKeydown}
  >
    <h2 id="library-dialog-title" class="font-display text-[20px] font-bold leading-tight text-fg">
      {title}
    </h2>
    <p id="library-dialog-subtitle" class="mt-2 text-[12px] leading-relaxed text-muted">
      {subtitle}
    </p>

    <form
      class="mt-4 flex flex-col gap-3.5"
      onsubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
        <span>Name</span>
        <input
          bind:this={nameField}
          bind:value={name}
          onkeydown={handleNameKeydown}
          class="h-8 rounded-[6px] border border-line bg-bg px-2.5 text-[12px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          type="text"
          placeholder="Library name"
          autocomplete="off"
          spellcheck="false"
          maxlength="80"
          required
          disabled={busy}
        />
      </label>

      <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
        <span>
          Description <span class="font-normal text-muted">(optional)</span>
        </span>
        <textarea
          bind:value={description}
          rows="3"
          class="resize-none rounded-[6px] border border-line bg-bg px-2.5 py-2 text-[12px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          placeholder="What you keep in this library"
          maxlength="240"
          disabled={busy}
        ></textarea>
      </label>

      {#if error !== ""}
        <div
          class="flex items-start gap-2 rounded-[6px] border border-danger bg-bg px-2.5 py-2 text-[12px] text-fg"
          role="alert"
        >
          <WarningCircle size="16" class="mt-0.5 shrink-0 text-danger" aria-hidden="true" />
          <span>{error}</span>
        </div>
      {/if}

      <div class="mt-1 flex justify-end gap-2">
        <button
          type="button"
          class="h-8 rounded-[6px] px-3 text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={busy}
          onclick={oncancel}
        >
          Cancel
        </button>
        <button
          type="submit"
          class="h-8 rounded-[6px] bg-fg px-3 text-[12px] font-medium text-bg-dark transition-opacity focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={!canSubmit}
        >
          {submitLabel}
        </button>
      </div>
    </form>
  </div>
</div>
