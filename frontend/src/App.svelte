<script lang="ts">
  import { onMount } from "svelte";
  import { Events, Window } from "@wailsio/runtime";
  import { get } from "svelte/store";
  import SpeakerHigh from "phosphor-svelte/lib/SpeakerHigh";
  import SpeakerSlash from "phosphor-svelte/lib/SpeakerSlash";
  import {
    connect,
    commands,
    player,
    appSettings,
    sessionState,
    spectrumConfig,
    waitForSettingsSave,
  } from "./lib/store";
  import {
    connectLibrary,
    homePage,
    libraryPage,
    libraryLoading,
    libraries,
    libraryId,
    libraryPlaylists,
    activePlaylist,
    activePlaylistTracks,
    activePlaylistLoading,
    activePlaylistError,
    loadLibrary,
    selectLibrary,
    openAlbumView,
    openArtistView,
    openPlaylist,
    closePlaylist,
    playTrack,
    playAlbum,
    playPlaylist,
    playRefs,
    albumRefs,
    setLiked,
    queueNext,
    queueLast,
    addToPlaylist,
    renamePlaylist,
    deletePlaylist,
    likeRef,
    isLiked,
    cachedAccents,
    loadAccents,
    loadLyrics,
    addLibrary,
    createLibrary,
    updateLibrary,
    removeLibrary,
    rescanLibrary,
  } from "./lib/library";
  import type { Album, LibraryInfo, Lyrics, Playlist, Track } from "./lib/types";
  import { isPlaylistQueueActive, type PlaylistPlayback } from "./lib/playlist-view";
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
  import ContextMenu from "./lib/ui/ContextMenu.svelte";
  import LibraryDialog from "./lib/ui/LibraryDialog.svelte";
  import RenameDialog from "./lib/ui/RenameDialog.svelte";
  import ConfirmDialog from "./lib/ui/ConfirmDialog.svelte";
  import { closeContextMenu, contextMenu } from "./lib/ui/context-menu.svelte";
  import { menuItemsFor, type MenuDeps } from "./lib/ui/menu-items";
  import HomePage from "./pages/HomePage.svelte";
  import LibraryPage from "./pages/LibraryPage.svelte";
  import AlbumPlayer from "./pages/AlbumPlayer.svelte";
  import PlaylistView from "./lib/ui/PlaylistView.svelte";
  import YtmArtistView from "./lib/ui/YtmArtistView.svelte";
  import {
    activeYtmArtist,
    activeYtmArtistError,
    activeYtmArtistLoading,
    activeYtmArtistOpen,
    backFromYtmArtist,
    closeYtmArtist,
    openYtmAlbumView,
    openYtmArtistById,
    openYtmArtistForTrack,
    playYtmAlbum,
    restoreYtmArtistFromAlbum,
    retryYtmArtist,
    shuffleYtmArtistTracks,
    ytmArtistReturn,
    ytmArtistStack,
  } from "./lib/ytm-artist";
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
  // The sidebar's size is session state, not a panel preference, so a restart
  // reopens the rail where the user left it. The store's copy is the truth;
  // the local state follows it and only commits back when the handle lets go.
  let sidebarWidth = $state(200);
  $effect(() => {
    sidebarWidth = $sessionState.sidebarWidth;
  });

  function openYtmArtistFromTrack(ref: string, artistName = "", artistId = "") {
    if (!ref.startsWith("ytm:")) {
      return;
    }
    if (playerOpen) {
      closePlayer();
    }
    searchOpen = false;
    void openYtmArtistForTrack(ref, artistName, artistId);
  }

  function commitSidebarWidth(w: number) {
    commands.setSidebarWidth(w);
  }

  let filter = $state("all");
  let searchOpen = $state(false);
  let addOpen = $state(false);
  let queueOpen = $state(false);
  let queuePinned = $state(false);
  let playerOpen = $state(false);
  // Mounted stays true through the closing transition, so the sheet can
  // animate down before it is removed. Covered hides the background shell once
  // the opaque sheet has finished sliding in, stopping hidden layout/paint work.
  let playerMounted = $state(false);
  let playerCovered = $state(false);
  let playerTimer: ReturnType<typeof setTimeout> | undefined;
  let coverTimer: ReturnType<typeof setTimeout> | undefined;

  let lyricsDoc = $state<{ path: string; doc: Lyrics | null }>({ path: "", doc: null });
  let lyricsSettledPath = $state("");
  const lyrics = $derived(lyricsDoc.path === snap.path ? lyricsDoc.doc : null);
  const lyricsLoading = $derived(
    snap.path !== "" && lyricsSettledPath !== snap.path,
  );
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

  // The album player sheet is the only consumer of the lyric column and the
  // spectrum pane, so the Go pumps idle while it is down. The report follows
  // the mount, not the open flag: the sheet keeps drawing through its closing
  // transition and only unmounts once it has slid away.
  $effect(() => {
    commands.setAlbumPlayerOpen(playerMounted);
  });

  // The lyric column and the backdrop follow the track, not the page.
  let lyricPath = "";
  let lyricsFetchedWithMeta = "";
  let lyricsSeq = 0;
  let lyricsProbeTimer: ReturnType<typeof setTimeout> | undefined;

  // Backend syncLyrics waits for duration (and title on local files) before starting providers.
  const lyricsMetaReady = $derived(
    snap.path !== "" &&
      snap.duration > 0 &&
      (snap.title !== "" || snap.path.startsWith("ytm:")),
  );

  const setLyrics = (path: string, doc: Awaited<ReturnType<typeof loadLyrics>>) => {
    lyricsDoc = {
      path,
      doc: doc
        ? {
            lines: (doc.lines ?? []).map((l) => ({
              timeMs: l.timeMs,
              text: l.text,
              translation: l.translation ?? "",
              romanized: l.romanized || undefined,
              words:
                l.words?.map((w) => ({
                  text: w.text,
                  romanized: w.romanized || undefined,
                  startMs: w.startMs,
                  endMs: w.endMs,
                })) ?? undefined,
            })),
            synced: doc.synced,
            source: doc.source,
            provider: doc.provider || undefined,
          }
        : null,
    };
  };

  $effect(() => {
    const path = snap.path;
    const ready = lyricsMetaReady;
    const newTrack = path !== lyricPath;
    const metaJustReady = !newTrack && ready && lyricsFetchedWithMeta !== path && lyricsSettledPath !== path;
    if (!newTrack && !metaJustReady) {
      return;
    }

    clearTimeout(lyricsProbeTimer);
    if (newTrack) {
      lyricPath = path;
      lyricsFetchedWithMeta = "";
      lyricsSettledPath = "";
      lyricsDoc = { path, doc: null };
    }
    const seq = ++lyricsSeq;
    if (path === "") {
      lyricsSettledPath = "";
      return;
    }
    if (ready) {
      lyricsFetchedWithMeta = path;
    }
    void loadLyrics(path).then((doc) => {
      if (lyricPath !== path || lyricsSeq !== seq) {
        return;
      }
      if (doc !== null) {
        setLyrics(path, doc);
        lyricsSettledPath = path;
        return;
      }
      if (ready) {
        setLyrics(path, null);
        lyricsSettledPath = path;
        return;
      }
      // Keep loading until duration/tags land so the provider chain can run;
      // cap the wait in case a stream never reports a duration.
      lyricsProbeTimer = setTimeout(() => {
        if (lyricPath === path && lyricsSettledPath !== path) {
          setLyrics(path, null);
          lyricsSettledPath = path;
        }
      }, 4000);
    });
  });

  const romanizationKey = $derived(
    `${$appSettings.playback.lyrics.autoRomanization}:${$appSettings.playback.lyrics.romanizationLanguage}:${$appSettings.playback.lyrics.autoSpacing}`,
  );
  let lastRomanizationKey: string | undefined;

  // Reload the open sheet after romanization settings commit on Go.
  $effect(() => {
    const key = romanizationKey;
    if (lastRomanizationKey === undefined) {
      lastRomanizationKey = key;
      return;
    }
    if (key === lastRomanizationKey) {
      return;
    }
    lastRomanizationKey = key;
    if (lyricPath === "") {
      return;
    }
    const path = lyricPath;
    const seq = ++lyricsSeq;
    void waitForSettingsSave().then(() => {
      if (lyricPath !== path || lyricsSeq !== seq) {
        return;
      }
      return loadLyrics(path).then((doc) => {
        if (lyricPath === path && lyricsSeq === seq) {
          setLyrics(path, doc);
          lyricsSettledPath = path;
        }
      });
    });
  });

  // Pick up a late sidecar write silently without re-triggering the spinner.
  onMount(() =>
    Events.On("molo:lyrics", (ev) => {
      const path = ev.data as string;
      if (path !== lyricPath || lyrics !== null) {
        return;
      }
      const seq = ++lyricsSeq;
      void loadLyrics(path).then((doc) => {
        if (lyricPath === path && lyricsSeq === seq) {
          setLyrics(path, doc);
          lyricsSettledPath = path;
        }
      });
    }),
  );

  let accentId = "";
  $effect(() => {
    const id = snap.coverId;
    if (id === "") {
      // Hold the current backdrop across transient empty-metadata frames during
      // track switches; clear once idle/stopped or a loaded track has no cover.
      if (snap.path === "" || snap.title !== "") {
        accentId = "";
        accents = [];
      }
      return;
    }
    if (id === accentId) {
      return;
    }
    accentId = id;
    const hit = cachedAccents(id);
    if (hit) {
      if (hit[0] !== accents[0] || hit[1] !== accents[1]) {
        accents = hit;
      }
      return;
    }
    void loadAccents(id).then((hex) => {
      if (accentId === id && (hex[0] !== accents[0] || hex[1] !== accents[1])) {
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

  function showPlayer() {
    clearTimeout(playerTimer);
    clearTimeout(coverTimer);
    playerOpen = true;
    playerMounted = true;
    coverTimer = setTimeout(() => {
      if (playerOpen) {
        playerCovered = true;
      }
    }, 340);
  }

  function openPlayer() {
    if (hasQueue) {
      closeQueue();
      showPlayer();
    }
  }

  function openAlbum(album: Album) {
    void openAlbumView(album);
  }

  // closePlayer lowers the sheet before unmounting it: the CSS transition owns
  // the motion, and the timeout only decides when the node can go away.
  function closePlayer() {
    clearTimeout(coverTimer);
    playerCovered = false;
    playerOpen = false;
    clearTimeout(playerTimer);
    playerTimer = setTimeout(() => {
      if (!playerOpen) {
        playerMounted = false;
      }
    }, 340);
  }

  function playLibraryTrack(track: Track) {
    playlistPlayback = null;
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

  let renaming = $state<Playlist | null>(null);
  let confirming = $state<{ title: string; body: string; run: () => void } | null>(null);
  let libraryDialog = $state<{ mode: "create" } | { mode: "edit"; library: LibraryInfo } | null>(
    null,
  );
  let libraryDialogBusy = $state(false);
  let libraryDialogError = $state("");

  const activeLibraryInfo = $derived($libraries.find((l) => l.id === $libraryId) ?? null);

  function openCreateLibrary() {
    addOpen = false;
    libraryDialogError = "";
    libraryDialogBusy = false;
    libraryDialog = { mode: "create" };
  }

  function openEditLibrary(id: string) {
    const lib = $libraries.find((l) => l.id === id);
    if (!lib) {
      return;
    }
    libraryDialogError = "";
    libraryDialogBusy = false;
    libraryDialog = { mode: "edit", library: lib };
  }

  function closeLibraryDialog() {
    if (libraryDialogBusy) {
      return;
    }
    libraryDialog = null;
    libraryDialogError = "";
  }

  async function submitLibraryDialog(name: string, description: string) {
    const dialog = libraryDialog;
    if (!dialog || libraryDialogBusy) {
      return;
    }
    libraryDialogBusy = true;
    libraryDialogError = "";
    try {
      const res =
        dialog.mode === "create"
          ? await createLibrary(name, description)
          : await updateLibrary(dialog.library.id, name, description);
      if (res.error !== "") {
        libraryDialogError = res.error;

        return;
      }
      libraryDialog = null;
      if (dialog.mode === "create") {
        filter = "all";
        tab = "library";
      }
    } finally {
      libraryDialogBusy = false;
    }
  }

  function handleLikeTrack(track: Track, nextLiked: boolean) {
    void setLiked(track, nextLiked).then((ok) => {
      if (ok && track.ref === snap.path) {
        liked = nextLiked;
      }
    });
  }

  let playlistPlayback = $state<PlaylistPlayback | null>(null);

  const queuePaths = $derived(queue.map((row) => row.path));
  const playlistQueueActive = $derived(
    $activePlaylist
      ? isPlaylistQueueActive(
          $activePlaylist.id,
          $activePlaylistTracks,
          queuePaths,
          snap.state,
          playlistPlayback,
        )
      : false,
  );
  const playlistQueuePlaying = $derived(playlistQueueActive && snap.state === "playing");

  function playPlaylistTracks(tracks: Track[], startIndex = 0) {
    const refs = tracks.slice(startIndex).map((t) => t.ref).filter((r) => r !== "");
    playlistPlayback = $activePlaylist ? { playlistId: $activePlaylist.id, refs } : null;
    void playRefs(refs);
  }

  const menuDeps = $derived<MenuDeps>({
    playlists: $libraryPlaylists,
    next: (refs) => void queueNext(refs),
    last: (refs) => void queueLast(refs),
    like: (track, liked) => handleLikeTrack(track, liked),
    addToPlaylist: (id, refs) => void addToPlaylist(id, refs),
    albumRefs,
    play: (refs) => {
      playlistPlayback = null;
      void playRefs(refs);
      openPlayer();
    },
    openAlbum,
    openPlaylist: (id) => {
      tab = "library";
      void openPlaylist(id);
    },
    renamePlaylist: (list) => {
      renaming = list;
    },
    deletePlaylist: (list) => {
      confirming = {
        title: "Delete playlist",
        body: `"${list.name}" leaves Molow. Its songs stay in the library.`,
        run: () => void deletePlaylist(list.id),
      };
    },
    openLibrary: (id) => {
      tab = "library";
      void selectLibrary(id);
    },
    editLibrary: (id) => openEditLibrary(id),
    rescanLibrary: (id) => void rescanLibrary(id),
    removeLibrary: (id) => {
      const name = $libraries.find((l) => l.id === id)?.name ?? "this library";
      confirming = {
        title: "Remove library",
        body: `${name} leaves Molow. The audio files on disk are not touched.`,
        run: () => void removeLibrary(id),
      };
    },
  });

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
  // paletteIntent is the verb the palette was opened with: the queue and the
  // ordinary search queue tracks, while the "+" menu's YouTube import adds the
  // highlighted hit to the open library.
  let paletteIntent = $state<"queue" | "import">("queue");
  let previewOnOpen = $state(false);
  const isMac = /mac/i.test(navigator.platform || navigator.userAgent);
  const shortcutLabel = isMac ? "⌘P" : "Ctrl P";
  let settingsOpen = $state(false);

  function openPalette(
    source: "queue" | "ytm",
    preview: boolean,
    intent: "queue" | "import" = "queue",
  ) {
    paletteSource = source;
    paletteIntent = intent;
    previewOnOpen = preview;
    paletteOpen = true;
  }

  function closePalette() {
    paletteOpen = false;
    paletteIntent = "queue";
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

  // The card shows on the first press and every press after updates it in
  // place, reading the optimistic value, so the bar moves with the key rather
  // than reporting once after the hold ends. The store restarts the card's
  // clock on each update, so the hold keeps it up.
  function showVolumeToast() {
    const v = volume();
    showToast({
      key: "volume",
      kind: "bar",
      label: "Volume",
      value: v,
      text: `${percent(v)}%`,
      icon: SpeakerHigh,
    });
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

  // Seek step per arrow keypress, 5 seconds.
  const SEEK_STEP_MS = 5000;

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
      case "seek-forward":
      case "seek-back": {
        // The engine seek is absolute, so the target is the snapshot position
        // plus the step, clamped to the track when its length is known.
        if (!hasQueue) {
          return;
        }
        const step = a === "seek-forward" ? SEEK_STEP_MS : -SEEK_STEP_MS;
        const at = Math.max(0, snap.position + step);
        commands.seek(snap.duration > 0 ? Math.min(at, snap.duration) : at);

        return;
      }
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
      case "fullscreen":
        void Window.ToggleFullscreen();

        return;
    }
  }

  onMount(() =>
    installKeyboard({
      // The album player sheet is not a modal to the keyboard: it is the
      // playback view, so the playback keys belong to it. Only the surfaces
      // that actually take the keyboard (the palette, settings) gate it.
      isModalOpen: () =>
        paletteOpen || settingsOpen || libraryDialog !== null || renaming !== null || confirming !== null,
      isPaletteOpen: () => paletteOpen,
      ownsKey: (k) => shouldIgnoreTarget(document.activeElement, k),
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
    if (e.key !== "Escape" || e.defaultPrevented) return;
    // A surface opened over the sheet owns Escape, so the sheet does not
    // minimize under it. A dialog with focus closes itself and stops the
    // event; this chain covers a press while focus sits outside the dialog.
    if (contextMenu.open) {
      closeContextMenu(true);
    } else if (libraryDialog !== null) {
      closeLibraryDialog();
    } else if (renaming !== null) {
      renaming = null;
    } else if (confirming !== null) {
      confirming = null;
    } else if (settingsOpen) {
      settingsOpen = false;
    } else if (paletteOpen) {
      closePalette();
    } else if (playerOpen) {
      closePlayer();
    } else if (searchOpen) {
      searchOpen = false;
    } else if ($activePlaylist !== null) {
      closePlaylist();
    }
  }}
/>

<div
  class="flex h-full flex-col bg-bg-dark text-fg"
  style={playerCovered ? "content-visibility: hidden" : ""}
  inert={playerMounted}
>
  <div class="flex min-h-0 flex-1">
    <Sidebar
      bind:width={sidebarWidth}
      active={tab}
      libraries={$libraries.map((l) => ({
        id: l.id,
        name: l.name,
        description: l.description,
        coverId: l.coverId || l.logoCovers[0] || "",
      }))}
      activeLibrary={$libraryId}
      onswitch={(id) => {
        closeYtmArtist();
        void selectLibrary(id);
      }}
      onnewlibrary={openCreateLibrary}
      onwidthcommit={commitSidebarWidth}
      onnav={(next) => {
        closeYtmArtist();
        closePlaylist();
        if (next === "library") {
          tab = "library";
        } else {
          tab = "home";
        }
        searchOpen = false;
      }}
      onsearch={() => {
        searchOpen = !searchOpen;
        addOpen = false;
      }}
      onsettings={() => (settingsOpen = true)}
      oneffects={() => void commands.openEffectWindow()}
    />

    <main class="relative min-w-0 flex-1 overflow-hidden">
      {#if $activeYtmArtistOpen}
        <YtmArtistView
          artist={$activeYtmArtist}
          loading={$activeYtmArtistLoading}
          error={$activeYtmArtistError}
          currentPath={snap.path}
          backLabel={$ytmArtistStack.length > 0
            ? $ytmArtistStack[$ytmArtistStack.length - 1].name
            : $activePlaylist
              ? $activePlaylist.name
              : tab === "home"
                ? "Home"
                : "Your Library"}
          onback={backFromYtmArtist}
          onretry={() => void retryYtmArtist()}
          onshuffle={() => {
            if ($activeYtmArtist) {
              playlistPlayback = null;
              void shuffleYtmArtistTracks($activeYtmArtist.topSongs);
            }
          }}
          onplayTrack={(_track, index, tracks) => playPlaylistTracks(tracks, index)}
          onlike={handleLikeTrack}
          onopenAlbum={(album) => void openYtmAlbumView(album)}
          onplayAlbum={(album) => {
            playlistPlayback = null;
            void playYtmAlbum(album);
          }}
          onplayVideo={(video) => {
            playlistPlayback = null;
            if (video.track) {
              void playTrack(video.track);
            } else if (video.id) {
              void playRefs([`ytm:${video.id}`]);
            }
          }}
          onopenArtist={(artist) => void openYtmArtistById(artist.id)}
        />
      {:else if $activePlaylist}
        <PlaylistView
          playlist={$activePlaylist}
          tracks={$activePlaylistTracks}
          loading={$activePlaylistLoading}
          error={$activePlaylistError}
          currentPath={snap.path}
          isActiveQueue={playlistQueueActive}
          isPlaying={playlistQueuePlaying}
          backLabel={$ytmArtistReturn
            ? $ytmArtistReturn.name
            : tab === "home"
              ? "Home"
              : "Your Library"}
          onback={() => {
            closePlaylist();
            restoreYtmArtistFromAlbum();
          }}
          onretry={() => {
            if ($activePlaylist?.libraryId === "ytm" && $activePlaylist.album) {
              void openYtmAlbumView($activePlaylist.album);
            } else if ($activePlaylist) {
              void openPlaylist($activePlaylist);
            }
          }}
          onplayAll={(tracks) => playPlaylistTracks(tracks, 0)}
          ontogglePause={() => commands.togglePause()}
          onplayTrack={(_track, index, ordered) => playPlaylistTracks(ordered, index)}
          onlike={handleLikeTrack}
          onrename={(list) => (renaming = list)}
          onopenTrackArtist={(t) => openYtmArtistFromTrack(t.ref, t.artist)}
        />
      {:else if tab === "home"}
        <HomePage
          hasLibraries={$libraries.length > 0}
          shelves={$homePage.shelves}
          {greeting}
          onplay={playLibraryTrack}
          onopenAlbum={openAlbum}
          onopenTrackArtist={(t) => openYtmArtistFromTrack(t.ref, t.artist)}
          onplayAlbum={(a) => {
            playlistPlayback = null;
            void playAlbum(a);
          }}
          onadd={() => void addLibrary()}
          oncreateLibrary={openCreateLibrary}
        />
      {:else}
        <LibraryPage
          library={activeLibraryInfo}
          shelves={$libraryPage.shelves}
          {filter}
          loading={$libraryLoading}
          activePlaylist={$activePlaylist}
          playlistTracks={$activePlaylistTracks}
          playlistLoading={$activePlaylistLoading}
          playlistError={$activePlaylistError}
          currentPath={snap.path}
          playlistActive={playlistQueueActive}
          playlistPlaying={playlistQueuePlaying}
          onfilter={setFilter}
          onadd={() => (addOpen = !addOpen)}
          oncreateLibrary={openCreateLibrary}
          onimportFolder={() => void addLibrary()}
          onimportYTM={() => openPalette("ytm", false, "import")}
          onopenAlbum={openAlbum}
          onopenArtist={(a) => void openArtistView(a)}
          onopenTrackArtist={(t) => openYtmArtistFromTrack(t.ref, t.artist)}
          onplayAlbum={(a) => {
            playlistPlayback = null;
            void playAlbum(a);
          }}
          onplay={playLibraryTrack}
          onopenPlaylist={(list) => void openPlaylist(list)}
          onplayPlaylistCard={(list) => {
            playlistPlayback = null;
            void playPlaylist(list);
          }}
          onclosePlaylist={closePlaylist}
          onretryPlaylist={() => {
            if ($activePlaylist) {
              void openPlaylist($activePlaylist);
            }
          }}
          onplayPlaylist={(tracks) => playPlaylistTracks(tracks, 0)}
          ontogglePlaylistPause={() => commands.togglePause()}
          onplayPlaylistTrack={(_track, index, ordered) => playPlaylistTracks(ordered, index)}
          onlikeTrack={handleLikeTrack}
          onrenamePlaylist={(list) => (renaming = list)}
        />
      {/if}

      {#if searchOpen}
        <SearchOverlay
          left={12}
          onclose={() => (searchOpen = false)}
          onplay={(t) => {
            playlistPlayback = null;
            void playTrack(t);
            searchOpen = false;
          }}
          onopenArtist={(t) => openYtmArtistFromTrack(t.ref, t.artist)}
        />
      {/if}

      {#if queueOpen}
        <QueueWindow
          rows={queue}
          pinned={queuePinned}
          onpin={() => (queuePinned = !queuePinned)}
          onplay={(i) => commands.playIndex(i)}
          onmove={(from, to) => commands.moveQueue(from, to)}
          onopenArtist={(path, artist) => openYtmArtistFromTrack(path, artist)}
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
    onopenArtist={openYtmArtistFromTrack}
  />

  {#if addOpen}
    <AddMenu
      onimport={() => {
        void addLibrary();
        addOpen = false;
      }}
      onytm={() => {
        openPalette("ytm", false, "import");
        addOpen = false;
      }}
      onclose={() => (addOpen = false)}
    />
  {/if}
</div>

{#if playerMounted}
  <AlbumPlayer
    {snap}
    {lyrics}
    {lyricsLoading}
    stops={accents}
    open={playerOpen}
    onclose={closePlayer}
    onseek={(ms) => commands.seek(ms)}
    onopenArtist={openYtmArtistFromTrack}
  />
{/if}

<CommandPalette
  open={paletteOpen}
  source={paletteSource}
  intent={paletteIntent}
  {queue}
  preview={snap.preview}
  startPreview={previewOnOpen}
  onclose={closePalette}
/>

<SettingsPanel open={settingsOpen} livePreview={playerOpen} onclose={() => (settingsOpen = false)} />
<Toaster />

<!-- One menu for the whole app. It sits outside the sheet's inert wrapper so a
     row inside the sheet still gets a working menu. -->
{#if contextMenu.open && contextMenu.target}
  <ContextMenu
    target={contextMenu.target}
    items={menuItemsFor(contextMenu.target, menuDeps)}
  />
{/if}

{#if libraryDialog}
  <LibraryDialog
    mode={libraryDialog.mode}
    initialName={libraryDialog.mode === "edit" ? libraryDialog.library.name : ""}
    initialDescription={libraryDialog.mode === "edit" ? libraryDialog.library.description : ""}
    busy={libraryDialogBusy}
    error={libraryDialogError}
    onsubmit={(name, description) => void submitLibraryDialog(name, description)}
    oncancel={closeLibraryDialog}
  />
{/if}

{#if renaming}
  <RenameDialog
    title="Rename playlist"
    label="Playlist name"
    value={renaming.name}
    onsubmit={(name) => {
      const list = renaming;
      renaming = null;
      if (list) {
        void renamePlaylist(list.id, name);
      }
    }}
    oncancel={() => (renaming = null)}
  />
{/if}

{#if confirming}
  <ConfirmDialog
    title={confirming.title}
    body={confirming.body}
    onconfirm={() => {
      const pick = confirming;
      confirming = null;
      pick?.run();
    }}
    oncancel={() => (confirming = null)}
  />
{/if}
