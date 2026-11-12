<script lang="ts">
  import Books from "phosphor-svelte/lib/Books";
  import CaretUpDown from "phosphor-svelte/lib/CaretUpDown";
  import GearSix from "phosphor-svelte/lib/GearSix";
  import House from "phosphor-svelte/lib/House";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlass";
  import Cover from "./Cover.svelte";

  export interface PlaylistRow {
    id: string;
    name: string;
    coverId?: string;
  }

  let {
    active = "home",
    width = $bindable(200),
    playlists = [],
    libraries = [],
    activeLibrary = "",
    onnav = (_tab: "home" | "library" | "search") => {},
    onsearch = () => {},
    onswitch = (_id: string) => {},
    onsettings = () => {},
  }: {
    active?: "home" | "library" | "search";
    width?: number;
    playlists?: PlaylistRow[];
    libraries?: { id: string; name: string }[];
    activeLibrary?: string;
    onnav?: (tab: "home" | "library" | "search") => void;
    onsearch?: () => void;
    onswitch?: (id: string) => void;
    onsettings?: () => void;
  } = $props();

  let switchOpen = $state(false);
  const currentName = $derived(libraries.find((l) => l.id === activeLibrary)?.name ?? "Library");

  const collapsed = $derived(width < 100);
  const nav = [
    { id: "home" as const, label: "Home", icon: House },
    { id: "search" as const, label: "Search", icon: MagnifyingGlass },
    { id: "library" as const, label: "Library", icon: Books },
  ];

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
      {#if libraries.length > 1}
        <div class="relative px-3">
          <button
            class="flex h-7 w-full items-center justify-between rounded-md px-1.5 text-[10px] text-muted transition-colors hover:bg-hover hover:text-fg"
            aria-haspopup="menu"
            aria-expanded={switchOpen}
            onclick={() => (switchOpen = !switchOpen)}
          >
            <span class="truncate">{currentName}</span>
            <CaretUpDown size="12" />
          </button>
          {#if switchOpen}
            <div
              class="absolute left-3 right-3 top-7 z-30 rounded-lg bg-surface p-1.5 shadow-menu"
              role="menu"
            >
              {#each libraries as lib (lib.id)}
                <button
                  class="flex h-[30px] w-full items-center rounded-md px-2 text-left text-[12px] text-fg hover:bg-hover"
                  class:bg-selected={lib.id === activeLibrary}
                  role="menuitemradio"
                  aria-checked={lib.id === activeLibrary}
                  onclick={() => {
                    onswitch(lib.id);
                    switchOpen = false;
                  }}
                >
                  <span class="truncate">{lib.name}</span>
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        <h2 class="px-3 text-[10px] text-muted">Your Library</h2>
      {/if}
    {/if}
    <div class="scroll-thin mt-2 flex flex-col gap-3 overflow-y-auto px-3">
      {#each playlists as row (row.id)}
        <button
          class="flex h-8 items-center gap-2 rounded-md text-[12px] text-fg transition-colors hover:bg-hover"
          class:justify-center={collapsed}
          title={row.name}
          onclick={() => onnav("library")}
        >
          {#if row.coverId}
            <Cover id={row.coverId} size={collapsed ? 28 : 32} radius={4} class="shrink-0" />
          {:else}
            <span
              class="grid shrink-0 place-items-center rounded-[4px]"
              style="width:{collapsed ? 28 : 32}px;height:{collapsed ? 28 : 32}px;background:linear-gradient(135deg,hsl(240 3.1% 26%),hsl(240 3.1% 17%))"
            >
              <span class="size-1.5 rounded-full bg-muted"></span>
            </span>
          {/if}
          {#if !collapsed}<span class="truncate">{row.name}</span>{/if}
        </button>
      {/each}
    </div>
  </div>

  <div class="px-3 pb-3">
    <div class="mx-1 mb-2 h-px bg-line"></div>
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
      if (e.key === "ArrowLeft") width = Math.max(56, width - 8);
      if (e.key === "ArrowRight") width = Math.min(280, width + 8);
    }}
  ></button>
</aside>
