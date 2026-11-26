<script lang="ts">
  import { tick } from "svelte";
  import Books from "phosphor-svelte/lib/Books";
  import CaretUpDown from "phosphor-svelte/lib/CaretUpDown";
  import Check from "phosphor-svelte/lib/Check";
  import Faders from "phosphor-svelte/lib/Faders";
  import GearSix from "phosphor-svelte/lib/GearSix";
  import House from "phosphor-svelte/lib/House";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlass";
  import MusicNotes from "phosphor-svelte/lib/MusicNotes";
  import Plus from "phosphor-svelte/lib/Plus";
  import UserCircle from "phosphor-svelte/lib/UserCircle";
  import type { Playlist } from "../types";
  import Cover from "./Cover.svelte";
  import { openContextMenu } from "./context-menu.svelte";

  export interface LibraryCard {
    id: string;
    kind?: string;
    name: string;
    description?: string;
    status?: string;
    coverId: string;
    trackCount?: number;
  }

  let {
    active = "home",
    width = $bindable(200),
    libraries = [],
    activeLibrary = "",
    playlists = [],
    playlistsLoading = false,
    activePlaylistId = "",
    ytmStatus = null,
    onnav = (_tab: "home" | "library" | "search") => {},
    onsearch = () => {},
    onswitch = (_id: string) => {},
    onopenplaylist = (_list: Playlist) => {},
    onnewlibrary = () => {},
    onyoutubeaccount,
    oneffects = () => {},
    onsettings = () => {},
    onwidthcommit = (_width: number) => {},
  }: {
    active?: "home" | "library" | "search";
    width?: number;
    libraries?: LibraryCard[];
    activeLibrary?: string;
    playlists?: Playlist[];
    playlistsLoading?: boolean;
    activePlaylistId?: string;
    ytmStatus?: { signedIn?: boolean; expired?: boolean; name?: string } | null;
    onnav?: (tab: "home" | "library" | "search") => void;
    onsearch?: () => void;
    onswitch?: (id: string) => void;
    onopenplaylist?: (list: Playlist) => void;
    onnewlibrary?: () => void;
    onyoutubeaccount?: () => void;
    oneffects?: () => void;
    onsettings?: () => void;
    onwidthcommit?: (width: number) => void;
  } = $props();

  const collapsed = $derived(width < 100);
  const currentLibrary = $derived(
    libraries.find((lib) => lib.id === activeLibrary) ?? libraries[0] ?? null,
  );
  const hasActivePlaylist = $derived(
    activePlaylistId !== "" && playlists.some((list) => list.id === activePlaylistId),
  );

  const nav = [
    { id: "home" as const, label: "Home", icon: House },
    { id: "search" as const, label: "Search", icon: MagnifyingGlass },
    { id: "library" as const, label: "Library", icon: Books },
  ];

  // The empty library's avatar art is a gradient seeded by the id, so it is the
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

  function librarySubtitle(lib: LibraryCard): string {
    if (lib.status === "scanning") {
      return "Syncing...";
    }
    if (lib.status === "expired") {
      return "Session expired";
    }
    if (lib.status === "signedOut") {
      return "Signed out";
    }
    if (lib.description) {
      return lib.description;
    }
    if (typeof lib.trackCount === "number") {
      if (lib.trackCount === 0) {
        return "Empty library";
      }

      return lib.trackCount === 1 ? "1 song" : `${lib.trackCount} songs`;
    }

    return "Library";
  }

  function playlistCoverId(list: Playlist): string {
    if (list.trackCount === 0 && !list.customCover && !list.remoteId) {
      return "";
    }

    return list.coverId || list.coverIds?.[0] || "";
  }

  const SWITCHER_ANIM_MS = 250;

  let switcherOpen = $state(false);
  let switcherMounted = $state(false);
  let switcherClosing = $state(false);
  let switcherCloseTimer: ReturnType<typeof setTimeout> | undefined;
  let switcherLeft = $state(12);
  let switcherTop = $state(56);
  let switcherWidth = $state(224);
  let triggerEl = $state<HTMLButtonElement | null>(null);
  let menuEl = $state<HTMLDivElement | null>(null);

  function positionSwitcher() {
    if (!triggerEl) {
      return;
    }
    const rect = triggerEl.getBoundingClientRect();
    const margin = 8;
    const menuW = collapsed ? 224 : Math.max(220, Math.round(rect.width));
    const rawLeft = collapsed ? rect.right + 8 : rect.left;
    const rawTop = collapsed ? rect.top : rect.bottom + 6;
    switcherWidth = menuW;
    switcherLeft = Math.max(margin, Math.min(rawLeft, window.innerWidth - menuW - margin));
    switcherTop = Math.max(margin, rawTop);
  }

  async function openSwitcher() {
    clearTimeout(switcherCloseTimer);
    positionSwitcher();
    switcherClosing = false;
    switcherMounted = true;
    switcherOpen = true;
    await tick();
    const activeItem =
      menuEl?.querySelector<HTMLElement>('[role="menuitem"][aria-current="true"]') ??
      menuEl?.querySelector<HTMLElement>('[role="menuitem"]');
    activeItem?.focus();
  }

  function finishCloseSwitcher() {
    clearTimeout(switcherCloseTimer);
    if (!switcherOpen) {
      switcherClosing = false;
      switcherMounted = false;
    }
  }

  function closeSwitcher(restoreFocus = false) {
    if (!switcherOpen) {
      return;
    }
    switcherOpen = false;
    if (restoreFocus) {
      triggerEl?.focus();
    }
    const reduced =
      typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    if (reduced) {
      finishCloseSwitcher();

      return;
    }
    switcherClosing = true;
    clearTimeout(switcherCloseTimer);
    switcherCloseTimer = setTimeout(finishCloseSwitcher, SWITCHER_ANIM_MS + 40);
  }

  function toggleSwitcher() {
    if (switcherOpen) {
      closeSwitcher(false);
    } else {
      void openSwitcher();
    }
  }

  function menuItems(): HTMLElement[] {
    return Array.from(menuEl?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
  }

  function handleMenuKeydown(e: KeyboardEvent) {
    e.stopPropagation();
    const items = menuItems();
    if (e.key === "Escape") {
      e.preventDefault();
      closeSwitcher(true);

      return;
    }
    if (items.length === 0) {
      return;
    }
    const at = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === "ArrowDown") {
      e.preventDefault();
      items[at === -1 ? 0 : (at + 1) % items.length].focus();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      items[at === -1 ? items.length - 1 : (at - 1 + items.length) % items.length].focus();
    } else if (e.key === "Home") {
      e.preventDefault();
      items[0].focus();
    } else if (e.key === "End") {
      e.preventDefault();
      items[items.length - 1].focus();
    }
  }

  $effect(() => {
    if (!switcherOpen) {
      return;
    }
    const onPointerDown = (e: PointerEvent) => {
      const node = e.target as Node | null;
      if (node && (menuEl?.contains(node) || triggerEl?.contains(node))) {
        return;
      }
      closeSwitcher(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeSwitcher(true);
      }
    };
    const onDismiss = () => closeSwitcher(false);

    window.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeyDown, true);
    window.addEventListener("resize", onDismiss);
    window.addEventListener("blur", onDismiss);

    return () => {
      window.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("keydown", onKeyDown, true);
      window.removeEventListener("resize", onDismiss);
      window.removeEventListener("blur", onDismiss);
    };
  });

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
  <div class="px-3 pt-3">
    {#if currentLibrary}
      <button
        bind:this={triggerEl}
        type="button"
        class="flex w-full items-center gap-2.5 rounded-lg border border-line/70 bg-surface/60 text-left transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        class:justify-center={collapsed}
        class:p-1={collapsed}
        class:px-2={!collapsed}
        class:py-1.5={!collapsed}
        class:bg-hover={switcherOpen}
        aria-haspopup="menu"
        aria-expanded={switcherOpen}
        aria-label={collapsed ? `Switch library (${currentLibrary.name})` : undefined}
        title={currentLibrary.description
          ? `${currentLibrary.name}: ${currentLibrary.description}`
          : currentLibrary.name}
        onclick={toggleSwitcher}
        oncontextmenu={(e) => {
          closeSwitcher(false);
          openContextMenu(e, {
            kind: "library",
            id: currentLibrary.id,
            label: currentLibrary.name || "this library",
            libraryId: currentLibrary.id,
          });
        }}
      >
        <span
          class="relative shrink-0 overflow-hidden rounded-full"
          style="width:{collapsed ? 28 : 30}px;height:{collapsed ? 28 : 30}px"
        >
          {#if currentLibrary.coverId}
            <Cover
              id={currentLibrary.coverId}
              size={collapsed ? 28 : 30}
              radius={999}
              class="absolute inset-0"
            />
          {:else}
            <span
              class="absolute inset-0 rounded-full"
              style="background:{gradientFor(currentLibrary.id)}"
            ></span>
          {/if}
        </span>
        {#if !collapsed}
          <span class="min-w-0 flex-1">
            <span class="block truncate text-[12px] font-medium leading-tight text-fg">
              {currentLibrary.name}
            </span>
            <span class="mt-0.5 block truncate text-[10px] leading-tight text-muted">
              {librarySubtitle(currentLibrary)}
            </span>
          </span>
          <CaretUpDown size="14" class="shrink-0 text-muted" />
        {/if}
      </button>
    {:else}
      <button
        type="button"
        class="flex w-full items-center gap-2.5 rounded-lg border border-dashed border-line text-left text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        class:justify-center={collapsed}
        class:p-1.5={collapsed}
        class:px-2={!collapsed}
        class:py-1.5={!collapsed}
        title="Create library"
        aria-label="Create library"
        onclick={onnewlibrary}
      >
        <span class="grid size-7 shrink-0 place-items-center rounded-full bg-surface text-muted">
          <Plus size="14" />
        </span>
        {#if !collapsed}
          <span class="truncate text-[12px] font-medium">Create library</span>
        {/if}
      </button>
    {/if}
  </div>

  <nav class="mt-3 flex flex-col gap-1 px-3" aria-label="Main">
    {#each nav as item (item.id)}
      {@const isCurrent = !hasActivePlaylist && active === item.id}
      <button
        type="button"
        class="flex h-8 items-center gap-4 rounded-md text-[12px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus {isCurrent
          ? ''
          : 'hover:bg-hover'}"
        class:bg-selected={isCurrent}
        class:text-fg={isCurrent}
        class:text-muted={!isCurrent}
        class:justify-center={collapsed}
        class:px-2={collapsed}
        class:px-2.5={!collapsed}
        aria-current={isCurrent ? "page" : undefined}
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
      <div class="flex items-center justify-between px-3">
        <h2 class="text-[10px] text-muted">Playlists</h2>
      </div>
    {/if}
    <div class="scroll-thin mt-2 flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto px-3">
      {#if playlistsLoading && playlists.length === 0}
        {#if !collapsed}
          <p class="px-1 text-[11px] text-muted">Loading playlists...</p>
        {/if}
      {:else if playlists.length === 0}
        {#if !collapsed && currentLibrary}
          <p class="px-1 text-[11px] text-muted">No playlists yet.</p>
        {/if}
      {:else}
        {#each playlists as list (list.id)}
          {@const coverId = playlistCoverId(list)}
          {@const selected = list.id === activePlaylistId}
          <button
            type="button"
            class="flex h-9 shrink-0 items-center gap-2.5 rounded-md text-left text-[12px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus {selected
              ? 'bg-selected text-fg'
              : 'text-fg hover:bg-hover'}"
            class:justify-center={collapsed}
            class:px-1={collapsed}
            class:px-1.5={!collapsed}
            title={list.name}
            aria-current={selected ? "page" : undefined}
            onclick={() => onopenplaylist(list)}
            oncontextmenu={(e) => {
              openContextMenu(e, {
                kind: "playlist",
                id: list.id,
                label: list.name || "this playlist",
                libraryId: list.libraryId,
                playlist: list,
              });
            }}
          >
            <span
              class="relative shrink-0 overflow-hidden rounded-[4px] bg-surface"
              style="width:{collapsed ? 28 : 30}px;height:{collapsed ? 28 : 30}px"
            >
              {#if coverId}
                <Cover
                  id={coverId}
                  size={collapsed ? 28 : 30}
                  radius={4}
                  class="absolute inset-0"
                />
              {:else}
                <span class="grid size-full place-items-center text-muted">
                  <MusicNotes size="14" />
                </span>
              {/if}
            </span>
            {#if !collapsed}
              <span class="min-w-0 flex-1 truncate">{list.name}</span>
            {/if}
          </button>
        {/each}
      {/if}
    </div>
  </div>

  <div class="px-3 pb-3">
    <div class="flex flex-col gap-1">
      <button
        type="button"
        class="flex h-8 w-full items-center gap-4 rounded-md text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        class:justify-center={collapsed}
        class:px-2={collapsed}
        class:px-2.5={!collapsed}
        title="Effects"
        aria-label="Effects"
        onclick={oneffects}
      >
        <Faders size="18" />
        {#if !collapsed}<span>Effects</span>{/if}
      </button>
      <button
        type="button"
        class="flex h-8 w-full items-center gap-4 rounded-md text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        class:justify-center={collapsed}
        class:px-2={collapsed}
        class:px-2.5={!collapsed}
        title="Settings"
        aria-label="Settings"
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
    type="button"
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

{#if switcherMounted}
  <div
    bind:this={menuEl}
    class="switcher-menu fixed z-40 rounded-lg border border-line bg-surface p-1.5 shadow-menu"
    class:switcher-collapsed={collapsed}
    class:switcher-closing={switcherClosing}
    class:pointer-events-none={switcherClosing}
    style="left:{switcherLeft}px; top:{switcherTop}px; width:{switcherWidth}px"
    role="menu"
    aria-label="Switch library"
    tabindex="-1"
    onkeydown={handleMenuKeydown}
    onanimationend={() => {
      if (switcherClosing) {
        finishCloseSwitcher();
      }
    }}
  >
    <div class="px-2 py-1 text-[10px] font-medium text-muted">Libraries</div>
    <div class="scroll-thin flex max-h-[260px] flex-col gap-0.5 overflow-y-auto">
      {#each libraries as lib (lib.id)}
        {@const isActive = lib.id === activeLibrary}
        <button
          type="button"
          role="menuitem"
          aria-current={isActive ? "true" : undefined}
          class="flex h-9 w-full items-center gap-2.5 rounded-md px-2 text-left text-[12px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus {isActive
            ? 'bg-selected text-fg'
            : 'text-fg hover:bg-hover'}"
          title={lib.description ? `${lib.name}: ${lib.description}` : lib.name}
          onclick={() => {
            closeSwitcher(false);
            if (!isActive) {
              onswitch(lib.id);
            }
          }}
          oncontextmenu={(e) => {
            closeSwitcher(false);
            openContextMenu(e, {
              kind: "library",
              id: lib.id,
              label: lib.name || "this library",
              libraryId: lib.id,
            });
          }}
        >
          <span class="relative size-6 shrink-0 overflow-hidden rounded-full">
            {#if lib.coverId}
              <Cover id={lib.coverId} size={24} radius={999} class="absolute inset-0" />
            {:else}
              <span class="absolute inset-0 rounded-full" style="background:{gradientFor(lib.id)}"
              ></span>
            {/if}
          </span>
          <span class="min-w-0 flex-1 truncate">{lib.name}</span>
          {#if isActive}
            <Check size="14" class="shrink-0 text-fg" />
          {/if}
        </button>
      {/each}
    </div>
    <div class="my-1.5 h-px bg-line"></div>
    <button
      type="button"
      role="menuitem"
      class="flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
      onclick={() => {
        closeSwitcher(false);
        onnewlibrary();
      }}
    >
      <Plus size="14" class="shrink-0" />
      <span class="truncate">New library</span>
    </button>
    {#if onyoutubeaccount}
      <button
        type="button"
        role="menuitem"
        class="mt-0.5 flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        onclick={() => {
          closeSwitcher(false);
          onyoutubeaccount();
        }}
      >
        <UserCircle size="14" class="shrink-0" />
        <span class="min-w-0 flex-1 truncate">
          {ytmStatus?.expired
            ? "YouTube Music (Expired)"
            : ytmStatus?.signedIn
              ? ytmStatus.name || "YouTube Music"
              : "Sign in to YouTube Music"}
        </span>
      </button>
    {/if}
  </div>
{/if}

<style>
  .switcher-menu {
    transform-origin: top left;
    will-change: transform, opacity;
    animation: switcher-spring-y var(--dur-popover) var(--ease-spring) both;
  }

  .switcher-menu.switcher-collapsed {
    animation: switcher-spring-x var(--dur-popover) var(--ease-spring) both;
  }

  .switcher-menu.switcher-closing {
    animation: switcher-spring-y-out var(--dur-popover) var(--ease-spring) both;
  }

  .switcher-menu.switcher-collapsed.switcher-closing {
    animation: switcher-spring-x-out var(--dur-popover) var(--ease-spring) both;
  }

  @keyframes switcher-spring-y {
    from {
      opacity: 0;
      transform: translateY(-4px) scale(0.94);
    }
    to {
      opacity: 1;
      transform: translateY(0) scale(1);
    }
  }

  @keyframes switcher-spring-y-out {
    from {
      opacity: 1;
      transform: translateY(0) scale(1);
    }
    to {
      opacity: 0;
      transform: translateY(-4px) scale(0.94);
    }
  }

  @keyframes switcher-spring-x {
    from {
      opacity: 0;
      transform: translateX(-4px) scale(0.94);
    }
    to {
      opacity: 1;
      transform: translateX(0) scale(1);
    }
  }

  @keyframes switcher-spring-x-out {
    from {
      opacity: 1;
      transform: translateX(0) scale(1);
    }
    to {
      opacity: 0;
      transform: translateX(-4px) scale(0.94);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .switcher-menu {
      animation: none;
    }
  }
</style>
