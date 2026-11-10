<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { SpeakerHigh, SpeakerSlash } from "phosphor-svelte";
  import {
    connect,
    commands,
    player,
    appSettings,
    spectrumConfig,
  } from "./lib/store";
  import {
    connectLibrary,
    homePage,
    libraryPage,
    libraryLoading,
    libraries,
    libraryId,
    playlistRows,
    loadLibrary,
    selectLibrary,
    playTrack,
    playAlbum,
    likeRef,
    isLiked,
    loadAccents,
    loadLyrics,
    addLibrary,
  } from "./lib/library";
  import type { Album, Lyrics, Track } from "./lib/types";
  import { percent } from "./lib/format";
  import { installKeyboard, shouldIgnoreTarget, type ShortcutAction } from "./lib/shortcuts";
  import { showToast } from "./lib/toast";
  import { muted, toggleMute, volume, volumeBy, VOLUME_STEP } from "./lib/volume";
  import { nextShuffleTarget } from "./lib/shuffle";
  import { nextRepeatTarget, type RepeatMode } from "./lib/repeat";
  import Sidebar from "./lib/ui/Sidebar.svelte";
  import Dock from "./lib/ui/Dock.svelte";
  import SearchOverlay from "./lib/ui/SearchOverlay.svelte";
  import AddMenu from "./lib/ui/AddMenu.svelte";
  import QueueWindow from "./lib/ui/QueueWindow.svelte";
  import HomePage from "./pages/HomePage.svelte";
  import LibraryPage from "./pages/LibraryPage.svelte";
  import AlbumPlayer from "./pages/AlbumPlayer.svelte";
  import CommandPalette from "./lib/components/CommandPalette.svelte";
  import SettingsPanel from "./lib/components/settings/SettingsPanel.svelte";
  import Toaster from "./lib/components/Toaster.svelte";

  // One subscription to the Go push stream; connect returns its teardown.
  onMount(connect);
  onMount(connectLibrary);

  const snap = $derived($player);
  const queue = $derived(snap.queue ?? []);
  const hasQueue = $derived(queue.length > 0);

  let tab = $state<"home" | "library">("home");
  let sidebarWidth = $state(200);
  let filter = $state("all");
  let searchOpen = $state(false);
  let addOpen = $state(false);
  let queueOpen = $state(false);
  let queuePinned = $state(false);
  let playerOpen = $state(false);

  let lyrics = $state<Lyrics | null>(null);
  let accents = $state<string[]>([]);
  let liked = $state(false);

  const greeting = $derived.by(() => {    const h = new Date().getHours();
    if (h < 12) {
      return "For you this morning";
    }
    if (h < 18) {
      return "For you this afternoon";
    }

    return "For you this evening";
  });

  const filterTab = (f: string) => {    switch (f) {
      case "artists":
        return "artists";
      case "playlists":
        return "playlists";
      case "albums":
        return "albums";
      default:
        return "all";
    }
  };

  function setFilter(f: string) {
    filter = f;
    void loadLibrary(filterTab(f));
  }

  // The lyric column and the backdrop follow the track, not the page.
  let lyricPath = "";
  $effect(() => {
    const path = snap.path;
    if (path === lyricPath) {
      return;
    }
    lyricPath = path;
    lyrics = null;
    if (path === "") {
      return;
    }
    void loadLyrics(path).then((doc) => {
      if (lyricPath !== path) {
        return;
      }
      lyrics = doc
        ? {
            lines: (doc.lines ?? []).map((l) => ({
              timeMs: l.timeMs,
              text: l.text,
              translation: l.translation ?? "",
            })),
            synced: doc.synced,
            source: doc.source,
          }
        : null;
    });
  });

  let accentId = "";
  $effect(() => {
    const id = snap.coverId;
    if (id === accentId) {
      return;
    }
    accentId = id;
    accents = [];
    if (id === "") {
      return;
    }
    void loadAccents(id).then((hex) => {
      if (accentId === id) {
        accents = hex;
      }
    });
  });

  let likedPath = "";
  $effect(() => {
    const path = snap.path;
    if (path === likedPath) {
      return;
    }
    likedPath = path;
    liked = false;
    if (path === "") {
      return;
    }
    void isLiked(path).then((on) => {
      if (snap.path === path) {
        liked = on;
      }
    });
  });

  function toggleLike() {
    const path = snap.path;
    void likeRef(path).then((on) => {
      if (snap.path === path) {
        liked = on;
      }
    });
  }

  function openPlayer() {
    if (hasQueue) {
      closeQueue();
      playerOpen = true;
    }
  }

  function openAlbum(album: Album) {
    void playAlbum(album);
    playerOpen = true;
  }

  function playLibraryTrack(track: Track) {
    void playTrack(track);
  }

  function closeQueue() {
    queueOpen = false;
    queuePinned = false;
  }

  function toggleQueue() {
    if (queueOpen) {
      closeQueue();
    } else {
      queueOpen = true;
    }
  }

  function outside(e: PointerEvent) {
    const target = e.target as HTMLElement | null;
    const inside = (sel: string) => target?.closest(sel) != null;
    // Each toggle and its panel are one unit: a click inside either one is not
    // an outside click, and the toggle's own handler closes it.
    if (queueOpen && !queuePinned && !inside('[data-queue]')) {
      closeQueue();
    }
    if (searchOpen && !inside('[data-search]')) {
      searchOpen = false;
    }
    if (addOpen && !inside('[data-add]')) {
      addOpen = false;
    }
  }

  // ---- existing feature surface: palette, settings, shortcuts ------------------

  let shuffleIntent = $state<boolean | null>(null);
  $effect(() => {
    if (shuffleIntent !== null && snap.shuffled === shuffleIntent) {
      shuffleIntent = null;
    }
  });

  let repeatIntent = $state<RepeatMode | null>(null);
  $effect(() => {
    if (repeatIntent !== null && snap.repeat === repeatIntent) {
      repeatIntent = null;
    }
  });

  let paletteOpen = $state(false);
  let paletteSource = $state<"queue" | "ytm">("queue");
  let previewOnOpen = $state(false);
  const isMac = /mac/i.test(navigator.platform || navigator.userAgent);
  const shortcutLabel = isMac ? "⌘P" : "Ctrl P";
  let settingsOpen = $state(false);

  function openPalette(source: "queue" | "ytm", preview: boolean) {
    paletteSource = source;
    previewOnOpen = preview;
    paletteOpen = true;
  }

  function closePalette() {
    paletteOpen = false;
    previewOnOpen = false;
    commands.previewStop();
  }

  function closePaletteOrSwitch(source: "queue" | "ytm") {
    if (paletteOpen && paletteSource === source) {
      closePalette();

      return;
    }
    openPalette(source, false);
  }

  let volumeToastTimer: ReturnType<typeof setTimeout> | null = null;
  function showVolumeToast() {
    if (volumeToastTimer !== null) {
      clearTimeout(volumeToastTimer);
    }
    volumeToastTimer = setTimeout(() => {
      volumeToastTimer = null;
      const v = volume();
      showToast({ key: "volume", kind: "bar", label: "Volume", value: v, text: `${percent(v)}%` });
    }, 400);
  }

  function showMuteToast() {
    const m = get(muted);
    showToast({
      key: "mute",
      kind: "tile",
      label: m ? "Muted" : "Unmuted",
      icon: m ? SpeakerSlash : SpeakerHigh,
    });
  }

  function persistRepeat(mode: RepeatMode) {
    commands.setRepeat(mode);
    commands.setAppSettings({
      ...$appSettings,
      playback: { ...$appSettings.playback, repeat: mode },
    });
  }

  function toggleRepeat(want: Exclude<RepeatMode, "off">) {
    const mode = nextRepeatTarget(repeatIntent, snap.repeat, want);
    repeatIntent = mode;
    persistRepeat(mode);
  }

  function action(a: ShortcutAction) {
    switch (a) {
      case "play-pause":
        if (hasQueue) {
          commands.togglePause();
        }

        return;
      case "volume-up":
        volumeBy(VOLUME_STEP);
        showVolumeToast();

        return;
      case "volume-down":
        volumeBy(-VOLUME_STEP);
        showVolumeToast();

        return;
      case "mute":
        toggleMute();
        showMuteToast();

        return;
      case "next":
        commands.next();

        return;
      case "prev":
        commands.prev();

        return;
      case "shuffle": {
        const on = nextShuffleTarget(shuffleIntent, snap.shuffled);
        shuffleIntent = on;
        commands.shuffle(on);

        return;
      }
      case "visualizer": {
        const on = !$appSettings.general.visualizerEnabled;
        commands.setAppSettings({
          ...$appSettings,
          general: { ...$appSettings.general, visualizerEnabled: on },
        });
        const next = { ...$spectrumConfig, enabled: on };
        spectrumConfig.set(next);
        commands.configureSpectrum(next);

        return;
      }
      case "repeat-all":
        toggleRepeat("all");

        return;
      case "repeat-one":
        toggleRepeat("one");

        return;
    }
  }

  onMount(() =>
    installKeyboard({
      isModalOpen: () => paletteOpen || settingsOpen || playerOpen,
      isPaletteOpen: () => paletteOpen,
      isEditableFocused: () => shouldIgnoreTarget(document.activeElement),
      openSettings: () => (settingsOpen = !settingsOpen),
      openPalette,
      closePaletteOrSwitch,
      action,
    }),
  );
</script>

<svelte:window
  onpointerdown={outside}
  onkeydown={(e) => {
    if (e.key === "Escape" && playerOpen) {
      playerOpen = false;
    }
    if (e.key === "Escape" && searchOpen) {
      searchOpen = false;
    }
  }}
/>

{#if playerOpen}
  <AlbumPlayer
    {snap}
    {lyrics}
    stops={accents}
    onclose={() => (playerOpen = false)}
    onseek={(ms) => commands.seek(ms)}
  />
{:else}
  <div class="flex h-full flex-col bg-bg-dark text-fg">
    <div class="flex min-h-0 flex-1">
      <Sidebar
        bind:width={sidebarWidth}
        active={tab}
        playlists={$playlistRows}
        libraries={$libraries.map((l) => ({ id: l.id, name: l.name }))}
        activeLibrary={$libraryId}
        onswitch={(id) => void selectLibrary(id)}
        onnav={(next) => {
          tab = next === "library" ? "library" : "home";
          searchOpen = false;
        }}
        onsearch={() => {
          searchOpen = !searchOpen;
          addOpen = false;
        }}
        onsettings={() => (settingsOpen = true)}
      />

      <main class="relative min-w-0 flex-1 overflow-hidden">
        {#if tab === "home"}
          <HomePage
            shelves={$homePage.shelves}
            {greeting}
            onplay={playLibraryTrack}
            onopenAlbum={openAlbum}
            onplayAlbum={(a) => void playAlbum(a)}
            onadd={() => void addLibrary()}
          />
        {:else}
          <LibraryPage
            shelves={$libraryPage.shelves}
            {filter}
            loading={$libraryLoading}
            onfilter={setFilter}
            onadd={() => (addOpen = !addOpen)}
            onopenAlbum={openAlbum}
            onplayAlbum={(a) => void playAlbum(a)}
            onplay={playLibraryTrack}
            onopenPlaylist={(id) => void loadLibrary("playlist:" + id)}
          />
        {/if}

        {#if searchOpen}
          <SearchOverlay
            left={12}
            onclose={() => (searchOpen = false)}
            onplay={(t) => {
              void playTrack(t);
              searchOpen = false;
            }}
          />
        {/if}

        {#if queueOpen}
          <QueueWindow
            rows={queue}
            pinned={queuePinned}
            onpin={() => (queuePinned = !queuePinned)}
            onplay={(i) => commands.playIndex(i)}
            onmove={(from, to) => commands.moveQueue(from, to)}
          />
        {/if}
      </main>
    </div>

    <Dock
      {snap}
      mode="docked"
      {queueOpen}
      {liked}
      playStyle={$appSettings.playback.playPauseStyle === "simple" ? "simple" : "default"}
      onexpand={openPlayer}
      onqueue={toggleQueue}
      onlyrics={openPlayer}
      onlike={toggleLike}
    />

    {#if addOpen}
      <AddMenu
        onimport={() => {
          void addLibrary();
          addOpen = false;
        }}
        onytm={() => {
          openPalette("ytm", false);
          addOpen = false;
        }}
        onclose={() => (addOpen = false)}
      />
    {/if}
  </div>
{/if}

<CommandPalette
  open={paletteOpen}
  source={paletteSource}
  {queue}
  preview={snap.preview}
  startPreview={previewOnOpen}
  onclose={closePalette}
/>

<SettingsPanel open={settingsOpen} onclose={() => (settingsOpen = false)} />
<Toaster />
