<script lang="ts">
  import { onMount, tick } from "svelte";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import { commands } from "../../store";
  import type { EffectPreset, EffectStage } from "../../effect-types";

  // The preset chooser beside the band count. Built-ins and user files share
  // one list but stay in separate sections, because only a user preset can be
  // deleted and the two can carry the same name.
  //
  // The component owns the menu's transient state (open, save form, delete
  // confirmation) and calls the store; the panel owns which preset is applied,
  // so the choice survives the popover closing.
  let {
    stage,
    presets,
    applied,
    dirty,
    onrefresh,
  }: {
    stage: EffectStage;
    presets: EffectPreset[];
    applied: EffectPreset | null;
    dirty: boolean;
    onrefresh: () => Promise<void>;
  } = $props();

  type Mode = "list" | "save" | "delete";
  let open = $state(false);
  let mode = $state<Mode>("list");
  let draft = $state("");
  let confirmOverwrite = $state(false);
  let busy = $state(false);
  let error = $state("");

  let trigger = $state<HTMLButtonElement | null>(null);
  let root = $state<HTMLDivElement | null>(null);
  let nameInput = $state<HTMLInputElement | null>(null);

  const builtins = $derived(presets.filter((p) => p.source === "builtin"));
  const userPresets = $derived(presets.filter((p) => p.source === "user"));

  function openMenu() {
    open = true;
    mode = "list";
    error = "";
    draft = applied?.name ?? "";
    // Re-read on every open so a file added or removed outside the app shows up
    // without restarting the window.
    void onrefresh();
  }

  function close() {
    if (!open) {
      return;
    }
    open = false;
    mode = "list";
    confirmOverwrite = false;
    error = "";
    trigger?.focus();
  }

  onMount(() => {
    const onDown = (e: MouseEvent) => {
      if (!open) {
        return;
      }
      const target = e.target;
      if (root && target instanceof Node && root.contains(target)) {
        return;
      }
      close();
    };
    const onKey = (e: KeyboardEvent) => {
      if (open && e.key === "Escape") {
        e.preventDefault();
        close();
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);

    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  });

  async function applyPreset(p: EffectPreset) {
    if (busy) {
      return;
    }
    busy = true;
    const ok = await commands.applyEffectPreset(stage.id, p);
    busy = false;
    if (!ok) {
      error = "Could not apply the preset.";

      return;
    }
    close();
  }

  async function startSave() {
    mode = "save";
    confirmOverwrite = false;
    error = "";
    draft = applied?.source === "user" ? applied.name : "";
    await tick();
    nameInput?.focus();
    nameInput?.select();
  }

  async function submitSave(e: SubmitEvent) {
    e.preventDefault();
    const name = draft.trim();
    if (name === "" || busy) {
      return;
    }
    const exists = userPresets.some((p) => p.name.toLowerCase() === name.toLowerCase());
    if (exists && !confirmOverwrite) {
      confirmOverwrite = true;

      return;
    }
    busy = true;
    const saved = await commands.saveEffectPreset(stage.id, name);
    busy = false;
    if (!saved) {
      error = "Could not save the preset.";

      return;
    }
    await onrefresh();
    close();
  }

  async function deletePreset() {
    if (busy || !applied || applied.source !== "user") {
      return;
    }
    busy = true;
    const ok = await commands.deleteEffectPreset(stage.id, applied.name);
    busy = false;
    if (!ok) {
      error = "Could not delete the preset.";
      mode = "list";

      return;
    }
    await onrefresh();
    close();
  }

  function openFolder() {
    void commands.openEffectPresetFolder();
    close();
  }

  function isApplied(p: EffectPreset) {
    return applied !== null && applied.source === p.source && applied.name === p.name;
  }
</script>

<div class="relative" bind:this={root}>
  <button
    bind:this={trigger}
    type="button"
    class="flex h-6 max-w-44 items-center gap-1.5 rounded-[4px] border border-line bg-bg/40 px-2 text-[11px] text-fg transition-colors hover:bg-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
    aria-haspopup="true"
    aria-expanded={open}
    title={applied ? `Preset: ${applied.name}` : "Choose a preset"}
    onclick={() => (open ? close() : openMenu())}
  >
    <span class="shrink-0 text-muted">Preset</span>
    <span class="truncate">{applied?.name ?? "Choose"}</span>
    {#if dirty}
      <span class="size-1.5 shrink-0 rounded-full bg-accent" aria-hidden="true"></span>
      <span class="sr-only">Modified since this preset was applied</span>
    {/if}
    <CaretDown size="11" class="shrink-0 text-muted" aria-hidden="true" />
  </button>

  {#if open}
    <div
      class="absolute right-0 top-full z-50 mt-1 w-64 rounded-[6px] border border-line bg-surface py-1 shadow-lg"
      role="group"
      aria-label="Presets"
    >
      {#if mode === "save"}
        <form class="px-3 py-2" onsubmit={submitSave}>
          <label class="text-[10px] uppercase tracking-wide text-muted" for="eq-preset-name">
            Preset name
          </label>
          <input
            id="eq-preset-name"
            bind:this={nameInput}
            bind:value={draft}
            class="mt-1 h-7 w-full rounded-[4px] border border-line bg-bg px-2 text-xs text-fg"
            type="text"
            maxlength="64"
            autocomplete="off"
          />
          {#if confirmOverwrite}
            <p class="mt-2 text-[11px] text-fg">Overwrite "{draft.trim()}"?</p>
            <p class="mt-1 text-[10px] text-muted">The existing file will be replaced.</p>
          {/if}
          {#if error}
            <p class="mt-2 text-[10px] text-danger">{error}</p>
          {/if}
          <div class="mt-2 flex justify-end gap-2">
            <button
              type="button"
              class="rounded-[4px] border border-line px-2 py-1 text-[11px] text-fg transition-colors hover:bg-hover"
              onclick={() => {
                confirmOverwrite = false;
                mode = "list";
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              class="rounded-[4px] border border-line px-2 py-1 text-[11px] text-fg transition-colors hover:bg-hover disabled:opacity-50"
              disabled={busy || draft.trim() === ""}
            >
              {confirmOverwrite ? "Overwrite" : "Save"}
            </button>
          </div>
        </form>
      {:else if mode === "delete"}
        <div class="px-3 py-2">
          <p class="text-[11px] text-fg">Delete "{applied?.name}"?</p>
          <p class="mt-1 text-[10px] text-muted">The file is removed from the presets folder.</p>
          {#if error}
            <p class="mt-2 text-[10px] text-danger">{error}</p>
          {/if}
          <div class="mt-2 flex justify-end gap-2">
            <button
              type="button"
              class="rounded-[4px] border border-line px-2 py-1 text-[11px] text-fg transition-colors hover:bg-hover"
              onclick={() => (mode = "list")}
            >
              Cancel
            </button>
            <button
              type="button"
              class="rounded-[4px] border border-danger px-2 py-1 text-[11px] text-danger transition-colors hover:bg-hover disabled:opacity-50"
              disabled={busy}
              onclick={deletePreset}
            >
              Delete
            </button>
          </div>
        </div>
      {:else}
        <div class="scroll-thin max-h-72 overflow-y-auto">
          {#if builtins.length > 0}
            <p class="px-3 pb-1 pt-1 text-[10px] uppercase tracking-wide text-muted">Built-in</p>
            {#each builtins as p (p.name)}
              <button
                type="button"
                class="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left text-xs text-fg transition-colors hover:bg-hover"
                onclick={() => applyPreset(p)}
              >
                <span class="truncate">{p.name}</span>
                {#if isApplied(p)}
                  <Check size="12" class="shrink-0 text-accent" weight="bold" aria-label="Applied" />
                {/if}
              </button>
            {/each}
          {/if}

          <p class="px-3 pb-1 pt-2 text-[10px] uppercase tracking-wide text-muted">User</p>
          {#if userPresets.length > 0}
            {#each userPresets as p (p.source + "/" + p.name)}
              <button
                type="button"
                class="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left text-xs text-fg transition-colors hover:bg-hover"
                onclick={() => applyPreset(p)}
              >
                <span class="truncate">{p.name}</span>
                {#if isApplied(p)}
                  <Check size="12" class="shrink-0 text-accent" weight="bold" aria-label="Applied" />
                {/if}
              </button>
            {/each}
          {:else}
            <p class="px-3 pb-1 text-[10px] text-muted">No saved presets yet.</p>
          {/if}
        </div>

        {#if error}
          <p class="px-3 pb-1 pt-1 text-[10px] text-danger">{error}</p>
        {/if}

        <div class="my-1 h-px bg-line" role="presentation"></div>

        <button
          type="button"
          class="block w-full px-3 py-1.5 text-left text-xs text-fg transition-colors hover:bg-hover"
          onclick={startSave}
        >
          Save as…
        </button>
        {#if applied?.source === "user"}
          <button
            type="button"
            class="block w-full px-3 py-1.5 text-left text-xs text-danger transition-colors hover:bg-hover"
            onclick={() => (mode = "delete")}
          >
            Delete "{applied.name}"
          </button>
        {/if}
        <button
          type="button"
          class="block w-full px-3 py-1.5 text-left text-xs text-fg transition-colors hover:bg-hover"
          onclick={openFolder}
        >
          Open presets folder
        </button>
      {/if}
    </div>
  {/if}
</div>
