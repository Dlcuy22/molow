<script lang="ts">
  import Books from "phosphor-svelte/lib/Books";
  import Faders from "phosphor-svelte/lib/Faders";
  import GearSix from "phosphor-svelte/lib/GearSix";
  import House from "phosphor-svelte/lib/House";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlass";
  import Plus from "phosphor-svelte/lib/Plus";
  import Cover from "./Cover.svelte";
  import { openContextMenu } from "./context-menu.svelte";

  export interface LibraryCard {
    id: string;
    name: string;
    description?: string;
    coverId: string;
  }

  let {
    active = "home",
    width = $bindable(200),
    libraries = [],
    activeLibrary = "",
    onnav = (_tab: "home" | "library" | "search") => {},
    onsearch = () => {},
    onswitch = (_id: string) => {},
    onnewlibrary = () => {},
    oneffects = () => {},
    onsettings = () => {},
    onwidthcommit = (_width: number) => {},
  }: {
    active?: "home" | "library" | "search";
    width?: number;
    libraries?: LibraryCard[];
    activeLibrary?: string;
    onnav?: (tab: "home" | "library" | "search") => void;
    onsearch?: () => void;
    onswitch?: (id: string) => void;
    onnewlibrary?: () => void;
    oneffects?: () => void;
    onsettings?: () => void;
    onwidthcommit?: (width: number) => void;
  } = $props();

  const collapsed = $derived(width < 100);
  const nav = [
    { id: "home" as const, label: "Home", icon: House },
    { id: "search" as const, label: "Search", icon: MagnifyingGlass },
    { id: "library" as const, label: "Library", icon: Books },
  ];

  // The empty library's card art is a gradient seeded by the id, so it is the
  // same colour on every launch and differs between libraries. The saturation
  // stays low so it sits in the dark chrome rather than shouting over it.
  function gradientFor(id: string): string {
    let h = 0x811c9dc5;
    for (let i = 0; i < id.length; i++) {
      h ^= id.charCodeAt(i);
      h = Math.imul(h, 0x01000193) >>> 0;
    }
    const hue = h % 360;
    const hue2 = (hue + 42) % 360;

    return `linear-gradient(135deg, hsl(${hue} 22% 27%), hsl(${hue2} 24% 16%))`;
  }

  let dragging = $state(false);
  let startX = 0;
  let startWidth = 0;

  function beginResize(e: PointerEvent) {
    dragging = true;
    startX = e.clientX;
    startWidth = width;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }

  function resize(e: PointerEvent) {
    if (!dragging) {
      return;
    }
    width = Math.min(280, Math.max(56, startWidth + e.clientX - startX));
  }

  function endResize() {
    dragging = false;
    // A drag commit fires once on release, so the session state is not
    // rewritten for every pointermove.
    if (width !== startWidth) {
      onwidthcommit(width);
    }
  }
</script>

<aside
  class="relative flex h-full shrink-0 flex-col border-r border-line bg-bg"
  style="width: {width}px"
>
  <nav class="flex flex-col gap-1 px-3 pt-3.5" aria-label="Main">
    {#each nav as item (item.id)}
      <button
        class="flex h-8 items-center gap-4 rounded-md text-[12px] transition-colors {active === item.id
          ? ''
          : 'hover:bg-hover'}"
        class:bg-selected={active === item.id}
        class:text-fg={active === item.id}
        class:text-muted={active !== item.id}
        class:justify-center={collapsed}
        class:px-2={collapsed}
        class:px-2.5={!collapsed}
        aria-current={active === item.id ? "page" : undefined}
        title={item.label}
        data-search={item.id === "search" ? true : undefined}
        onclick={() => (item.id === "search" ? onsearch() : onnav(item.id))}
      >
        <item.icon size="18" />
        {#if !collapsed}<span class="truncate">{item.label}</span>{/if}
      </button>
    {/each}
  </nav>

  <div class="mt-5 flex min-h-0 flex-1 flex-col">
    {#if !collapsed}
      <div class="relative flex items-center justify-between pr-2 pl-3">
        <h2 class="text-[10px] text-muted">Your Library</h2>
        <button
          class="grid size-6 place-items-center rounded-md text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          aria-label="New library"
          title="New library"
          onclick={onnewlibrary}
        >
          <Plus size="14" />
        </button>
      </div>
    {/if}
    <div class="scroll-thin mt-2 flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-3">
      {#each libraries as lib (lib.id)}
        <button
          class="flex h-8 shrink-0 items-center gap-2 rounded-md text-[12px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus {lib.id === activeLibrary
            ? 'bg-selected text-fg'
            : 'text-fg hover:bg-hover'}"
          class:justify-center={collapsed}
          title={lib.description ? `${lib.name}: ${lib.description}` : lib.name}
          aria-current={lib.id === activeLibrary ? "true" : undefined}
          onclick={() => onswitch(lib.id)}
          oncontextmenu={(e) => {
            openContextMenu(e, {
              kind: "library",
              id: lib.id,
              label: lib.name || "this library",
              libraryId: lib.id,
            });
          }}
        >
          <span
            class="relative shrink-0 overflow-hidden rounded-[4px]"
            style="width:{collapsed ? 28 : 32}px;height:{collapsed ? 28 : 32}px"
          >
            {#if lib.coverId}
              <Cover id={lib.coverId} size={collapsed ? 28 : 32} radius={4} class="absolute inset-0" />
            {:else}
              <span
                class="absolute inset-0 rounded-[4px]"
                style="background:{gradientFor(lib.id)}"
              ></span>
            {/if}
          </span>
          {#if !collapsed}<span class="truncate">{lib.name}</span>{/if}
        </button>
      {/each}
    </div>
  </div>

  <div class="px-3 pb-3">
    <div class="mx-1 mb-2 h-px bg-line"></div>
    <div class="flex flex-col gap-1">
      <button
        class="flex h-8 w-full items-center gap-4 rounded-md px-2.5 text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg"
        class:justify-center={collapsed}
        class:px-2={collapsed}
        title="Effects"
        aria-label="Effects"
        onclick={oneffects}
      >
        <Faders size="18" />
        {#if !collapsed}<span>Effects</span>{/if}
      </button>
      <button
        class="flex h-8 w-full items-center gap-4 rounded-md px-2.5 text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg"
        class:justify-center={collapsed}
        class:px-2={collapsed}
        title="Settings"
        onclick={onsettings}
      >
        <GearSix size="18" />
        {#if !collapsed}<span>Settings</span>{/if}
      </button>
    </div>
  </div>

  <!-- The rail's right edge is the resize handle. The visible line is 1px; the
       hit area is wider so the pointer does not have to be exact. -->
  <button
    class="absolute inset-y-0 -right-1 w-2 cursor-col-resize border-0 bg-transparent p-0"
    class:bg-focus={dragging}
    aria-label="Resize sidebar"
    onpointerdown={beginResize}
    onpointermove={resize}
    onpointerup={endResize}
    onpointercancel={endResize}
    onkeydown={(e) => {
      if (e.key === "ArrowLeft") {
        width = Math.max(56, width - 8);
        onwidthcommit(width);
      }
      if (e.key === "ArrowRight") {
        width = Math.min(280, width + 8);
        onwidthcommit(width);
      }
    }}
  ></button>
</aside>
