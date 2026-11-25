<script lang="ts">
  import { onMount } from "svelte";
  import {
    closeContextMenu,
    contextMenu,
    type MenuItem,
    type MenuTarget,
  } from "./context-menu.svelte";

  let {
    target,
    items,
  }: {
    target: MenuTarget;
    items: MenuItem[];
  } = $props();

  // Measure after mount so viewport clamping uses the rendered menu size.
  const MARGIN = 8;
  let el = $state<HTMLDivElement | null>(null);
  let placed = $state(false);
  let left = $state(0);
  let top = $state(0);

  onMount(() => {
    const width = el?.offsetWidth ?? 0;
    const height = el?.offsetHeight ?? 0;
    left = Math.max(MARGIN, Math.min(contextMenu.x, window.innerWidth - width - MARGIN));
    top = Math.max(MARGIN, Math.min(contextMenu.y, window.innerHeight - height - MARGIN));
    placed = true;

    // Capture scroll because it does not bubble; dismiss before position goes stale.
    const outside = (e: PointerEvent) => {
      const node = e.target as Node | null;
      if (node && el?.contains(node)) {
        return;
      }
      closeContextMenu(false);
    };
    const dismiss = () => closeContextMenu(false);

    window.addEventListener("pointerdown", outside, true);
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    window.addEventListener("blur", dismiss);

    return () => {
      window.removeEventListener("pointerdown", outside, true);
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
      window.removeEventListener("blur", dismiss);
    };
  });

  $effect(() => {
    if (placed) {
      el?.querySelector<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')?.focus();
    }
  });

  function rows(): HTMLElement[] {
    return Array.from(
      el?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])') ?? [],
    );
  }

  function move(step: 1 | -1) {
    const list = rows();
    if (list.length === 0) {
      return;
    }
    const at = list.indexOf(document.activeElement as HTMLElement);
    // A menu opened by a right-click has focus on nothing yet, so the first
    // arrow lands on the near end rather than skipping a row.
    const next = at === -1 ? (step === 1 ? 0 : list.length - 1) : at + step;
    list[(next + list.length) % list.length].focus();
  }

  function activate(e: KeyboardEvent) {
    // Escape and the arrows belong to the menu, not to a surface behind it.
    e.stopPropagation();
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        closeContextMenu(true);

        return;
      case "ArrowDown":
        e.preventDefault();
        move(1);

        return;
      case "ArrowUp":
        e.preventDefault();
        move(-1);

        return;
      case "Home":
        e.preventDefault();
        rows()[0]?.focus();

        return;
      case "End": {
        e.preventDefault();
        const list = rows();
        list[list.length - 1]?.focus();

        return;
      }
    }
  }

  function pick(row: MenuItem) {
    if (row.kind !== "item" || row.disabled) {
      return;
    }
    closeContextMenu(true);
    row.run?.();
  }
</script>

<div
  bind:this={el}
  class="fixed z-50 min-w-48 rounded-[6px] border border-line bg-surface py-1 shadow-menu"
  class:invisible={!placed}
  class:motion-popover={placed}
  style="left:{left}px; top:{top}px"
  role="menu"
  aria-label="Actions for {target.label}"
  tabindex="-1"
  data-context-menu
  onkeydown={activate}
>
  {#each items as row, i (row.kind + (row.label ?? "") + i)}
    {#if row.kind === "separator"}
      <div class="my-1 h-px bg-line" role="separator"></div>
    {:else}
      <button
        class="flex h-[30px] w-full items-center gap-2 px-3 text-left text-xs transition-colors focus:bg-hover"
        class:text-danger={row.danger}
        class:text-fg={!row.danger}
        class:hover:bg-hover={!row.disabled}
        class:text-disabled={row.disabled}
        role="menuitem"
        aria-disabled={row.disabled ? "true" : undefined}
        onclick={() => pick(row)}
      >
        <span class="min-w-0 flex-1 truncate">{row.label}</span>
        {#if row.hint}
          <span class="shrink-0 text-[11px] text-muted">{row.hint}</span>
        {/if}
      </button>
    {/if}
  {/each}
</div>
