<script lang="ts">
  import { appSettings, commands, discordStatus, spectrumConfig, spectrumSchema } from "../../store";
  import { showToast } from "../../toast";
  import Check from "phosphor-svelte/lib/Check";
  import WarningCircle from "phosphor-svelte/lib/WarningCircle";
  import { PlayerService } from "../../../../bindings/github.com/dlcuy22/molow/backend";
  import type { CrossfadeSettings, LyricsSettings, SpectrumConfig } from "../../../../bindings/github.com/dlcuy22/molow/backend/models";
  import type { Param } from "../../../../bindings/github.com/dlcuy22/molow/internal/spectrum/models";
  import { fftLabel } from "../../spectrum-fft";
  import { visualizerBars } from "../../visualizer";
  import DiscordCard from "../DiscordCard.svelte";
  import Switch from "./Switch.svelte";
  import Slider from "./Slider.svelte";
  import Picker from "./Picker.svelte";
  import SettingGroup from "./SettingGroup.svelte";
  import SettingRow from "./SettingRow.svelte";
  import TextField from "./TextField.svelte";
  import Button from "./Button.svelte";

  // The settings panel: a modal overlay opened with Ctrl+I, with a tab per
  // category.
  let { open, onclose }: { open: boolean; onclose: () => void } = $props();

  type Tab = "general" | "playback" | "integration" | "stream";
  let tab = $state<Tab>("general");
  let panelEl = $state<HTMLDivElement | null>(null);

  const crossfade = $derived($appSettings.playback.crossfade);
  // The visualizer switch reads the persisted flag, not the live pump's flag,
  // so the two cannot disagree after a reload: the pump is rebuilt from this
  // value on startup.
  const visualizerOn = $derived($appSettings.general.visualizerEnabled);
  // Off is not a stored style: the panel shows Off when the switch is off, and
  // the chosen layout stays ready for the next turn-on, including the v key.
  const visualizerStyle = $derived($appSettings.general.visualizerStyle ?? "minimal");
  const visualizerStyleValue = $derived(visualizerOn ? visualizerStyle : "off");
  const dynamicScale = $derived(($spectrumConfig.scale ?? "dynamic") !== "fixed");

  // The Discord status line and toggle read the store the same way the panel
  // read them when they lived in the main window's right column.
  const discordStatusLine = $derived(
    !$discordStatus.enabled
      ? "Off"
      : $discordStatus.connected
        ? "Connected to Discord"
        : "Waiting for Discord",
  );

  function toggleDiscord() {
    commands.setDiscordEnabled(!$discordStatus.enabled);
  }

  // spectrumValue reads one visualizer knob. The panel is built from the schema
  // the engine reports, so a new knob is one Go entry and no frontend change.
  function spectrumValue(p: Param): number {
    const cfg = $spectrumConfig;
    if (p.key === "bars") return cfg.bars;
    if (p.key === "fft") return cfg.fft;
    if (p.key === "minHz") return cfg.minHz;
    if (p.key === "maxHz") return cfg.maxHz;

    return p.default;
  }

  // The spectrum setters are optimistic like the crossfade ones: the store
  // drives the control, and the engine confirms on the next snapshot tick.
  // These knobs cannot fail server-side (low cut tops out below high cut's
  // floor, and the transform size falls back to the default if it is not on
  // the list), so there is no rollback path to maintain.
  function setSpectrum(patch: Partial<SpectrumConfig>) {
    const next = { ...$spectrumConfig, ...patch };
    spectrumConfig.set(next);
    commands.configureSpectrum(next);
  }

  // onSpectrumParam writes one schema knob back. A knob the engine adds needs a
  // matching line here, the same as spectrumValue above.
  function onSpectrumParam(p: Param, value: number) {
    const patch: Partial<SpectrumConfig> = {};
    if (p.key === "bars") patch.bars = value;
    if (p.key === "fft") patch.fft = value;
    if (p.key === "minHz") patch.minHz = value;
    if (p.key === "maxHz") patch.maxHz = value;
    setSpectrum(patch);
  }

  // setVisualizerDelay persists the compensating delay in the document. Go
  // reads the delay from the document on every runner rebuild and the save
  // reconfigures the spectrum (the same path the crossfade uses), so no second
  // call is needed; the shape payload does not carry the field, so the knob
  // cannot be erased by echoing the shape back.
  function setVisualizerDelay(ms: number) {
    commands.setAppSettings({
      ...$appSettings,
      general: { ...$appSettings.general, visualizerDelayMs: ms },
    });
  }

  // Reset to the default tab each time the panel opens, so it does not reopen
  // on a tab the user left it on.
  let wasOpen = false;
  $effect(() => {
    if (open && !wasOpen) {
      tab = "general";
      panelEl?.focus();
    }
    wasOpen = open;
  });

  // setCrossfade writes the whole settings document back with one field
  // changed. The panel holds no draft state: the store is the one place a
  // setting lives, and the Go side persists what it is given.
  function setCrossfade(patch: Partial<CrossfadeSettings>) {
    commands.setAppSettings({
      ...$appSettings,
      playback: {
        ...$appSettings.playback,
        crossfade: { ...$appSettings.playback.crossfade, ...patch },
      },
    });
  }

  // setPlayPauseStyle writes which play/pause control the dock draws: the
  // inverted disc it shipped with, or the album player's bare glyph.
  function setPlayPauseStyle(style: string) {
    commands.setAppSettings({
      ...$appSettings,
      playback: { ...$appSettings.playback, playPauseStyle: style },
    });
  }

  // setLyrics updates one field of the lyric-provider group: a toggle, the
  // save location, or the YouTube Music cache folder.
  function setLyrics(patch: Partial<LyricsSettings>) {
    commands.setAppSettings({
      ...$appSettings,
      playback: {
        ...$appSettings.playback,
        lyrics: { ...$appSettings.playback.lyrics, ...patch },
      },
    });
  }

  function toggleProvider(name: string, on: boolean) {
    setLyrics({ providers: { ...$appSettings.playback.lyrics.providers, [name]: on } });
  }

  // browseLyricsFolder opens the native picker and stores the choice.
  // Cancelling changes nothing.
  async function browseLyricsFolder() {
    try {
      const path = await PlayerService.PickLyricsFolder();
      if (path) {
        setLyrics({ folder: path });
      }
    } catch {
      // Cancelled: no setting to write.
    }
  }

  // The result goes to a toast, so the row description stays fixed. Each press
  // is its own answer, so it gets its own card.
  async function clearLyricsCache() {
    try {
      const { files, bytes } = await PlayerService.ClearLyricsCache();
      showToast({
        key: "lyrics-cache",
        kind: "message",
        stack: true,
        label:
          files === 0
            ? "Lyrics cache is already empty"
            : `Cache cleared, ${cacheSize(bytes)} freed`,
        icon: Check,
      });
    } catch {
      showToast({
        key: "lyrics-cache",
        kind: "message",
        stack: true,
        label: "Could not clear the lyrics cache",
        icon: WarningCircle,
      });
    }
  }

  function cacheSize(bytes: number): string {
    if (bytes < 1024) {
      return `${bytes} B`;
    }
    if (bytes < 1024 * 1024) {
      return `${Math.round(bytes / 1024)} KB`;
    }

    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }

  const providers = $derived($appSettings.playback.lyrics.providers ?? {});
  const saveLocation = $derived($appSettings.playback.lyrics.saveLocation);
  const lyricsFolder = $derived($appSettings.playback.lyrics.folder);

  // setVisualizerStyle persists the chosen layout and pokes the running pump
  // at once. Off is the same switch the v key flips, and the layout is kept so
  // turning the visualizer back on restores it. The point count follows the
  // layout, so switching to full fills the wider sheet.
  function setVisualizerStyle(style: string) {
    const enabled = style !== "off";
    commands.setAppSettings({
      ...$appSettings,
      general: {
        ...$appSettings.general,
        visualizerEnabled: enabled,
        visualizerStyle: enabled ? style : visualizerStyle,
      },
    });
    setSpectrum({
      enabled,
      bars: visualizerBars(enabled ? style : visualizerStyle),
    });
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      // The panel sits over the album player, which also closes on Escape.
      // Stopping here keeps the press from reaching that handler.
      e.stopPropagation();
      onclose();
    }
  }
</script>

{#if open}
  <!-- The gutter grows with the window: a narrow window keeps a tight margin,
       a wide one gives the panel room to sit in. All of it is padding on this
       flex container, so the panel's max-h-full counts the gutter and can never
       overflow the viewport. -->
  <div
    class="fixed inset-0 z-50 flex justify-center bg-black/50 px-[clamp(0.75rem,2vw,1.5rem)] pb-[clamp(0.75rem,2vw,1.5rem)] pt-[clamp(1rem,6vh,3.5rem)]"
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) onclose();
    }}
  >
    <div
      bind:this={panelEl}
      class="flex max-h-full w-full max-w-[clamp(28rem,56vw,52rem)] flex-col overflow-hidden border border-line bg-surface"
      role="dialog"
      aria-modal="true"
      aria-label="Settings"
      tabindex="-1"
      onkeydown={onKeydown}
    >
      <!-- Tabs across the top -->
      <div
        class="flex shrink-0 items-center gap-1 border-b border-line px-[clamp(0.5rem,1.6vw,1.25rem)] py-[clamp(0.375rem,1vh,0.75rem)]"
        role="tablist"
        aria-label="Settings categories"
      >
        {#each [["general", "General"], ["playback", "Playback"], ["integration", "Integration"], ["stream", "Stream"]] as const as [id, label] (id)}
          <button
            class="rounded-[4px] px-2.5 py-1 text-xs transition-colors"
            class:bg-selected={tab === id}
            class:text-fg={tab === id}
            class:text-muted={tab !== id}
            role="tab"
            aria-selected={tab === id}
            aria-controls="settings-panel-body"
            onclick={() => (tab = id)}
          >
            {label}
          </button>
        {/each}
      </div>

      <div
        id="settings-panel-body"
        class="scroll-thin min-h-0 flex-1 overflow-y-auto px-[clamp(0.5rem,1.6vw,1.25rem)] py-[clamp(0.5rem,1.2vh,1rem)]"
        role="tabpanel"
      >
        {#if tab === "playback"}
          <div class="divide-y divide-line">
            <SettingGroup label="Crossfade" description={null}>
              <!-- One feature's rows: the switch, the length it runs for, and
                   the case it leaves alone. -->
              <div class="divide-y divide-line">
                <SettingRow
                  label="Crossfade"
                  description="Fade the end of a track into the start of the next."
                >
                  {#snippet control()}
                    <Switch
                      checked={crossfade.enabled}
                      label="Crossfade"
                      onchange={(enabled) => setCrossfade({ enabled })}
                    />
                  {/snippet}
                </SettingRow>

                <div class="py-2.5" class:opacity-40={!crossfade.enabled}>
                  <Slider
                    label="Duration"
                    value={crossfade.durationMs / 1000}
                    min={1}
                    max={12}
                    step={1}
                    unit="s"
                    disabled={!crossfade.enabled}
                    format={(v) => `${v} s`}
                    onchange={(seconds) => setCrossfade({ durationMs: Math.round(seconds) * 1000 })}
                  />
                </div>

                <SettingRow
                  label="Skip for consecutive album tracks"
                  description="Leave tracks from the same album unblended"
                >
                  {#snippet control()}
                    <Switch
                      checked={crossfade.skipSameAlbum}
                      label="Skip for consecutive album tracks"
                      onchange={(skipSameAlbum) => setCrossfade({ skipSameAlbum })}
                    />
                  {/snippet}
                </SettingRow>
              </div>
            </SettingGroup>

            <SettingGroup label="Controls" description={null}>
              <!-- How the main control is drawn. Repeat lives on the dock's own
                   button and the l/L keys, so the panel does not repeat it. -->
              <div class="divide-y divide-line">
                <SettingRow
                  label="Play/Pause control style"
                  description={null}
                >
                  {#snippet control()}
                    <Picker
                      label={null}
                      value={$appSettings.playback.playPauseStyle}
                      options={[
                        { value: "default", label: "Default" },
                        { value: "simple", label: "Simple" },
                      ]}
                      onchange={(v) => setPlayPauseStyle(v)}
                    />
                  {/snippet}
                </SettingRow>
              </div>
            </SettingGroup>

            <SettingGroup label="Lyrics" description={null}>
              <!-- Where a sheet comes from, and where the app keeps what it
                   fetched: the providers first, then storage. -->
              <div class="divide-y divide-line">
                <SettingRow
                  label="PetitLyrics"
                  description="Word-synced"
                >
                  {#snippet control()}
                    <Switch checked={providers.petitlyrics ?? true} label="PetitLyrics" onchange={(on) => toggleProvider("petitlyrics", on)} />
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="BetterLyrics"
                  description="Word or line timing from a YouTube-anchored search."
                >
                  {#snippet control()}
                    <Switch checked={providers.betterlyrics ?? true} label="BetterLyrics" onchange={(on) => toggleProvider("betterlyrics", on)} />
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="LyricsPlus"
                  description="Word or line synced"
                >
                  {#snippet control()}
                    <Switch checked={providers.lyricsplus ?? true} label="LyricsPlus" onchange={(on) => toggleProvider("lyricsplus", on)} />
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="KuGou"
                  description="Line-synced"
                >
                  {#snippet control()}
                    <Switch checked={providers.kugou ?? true} label="KuGou" onchange={(on) => toggleProvider("kugou", on)} />
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="LrcLib"
                  description="Line-synced"
                >
                  {#snippet control()}
                    <Switch checked={providers.lrclib ?? true} label="LrcLib" onchange={(on) => toggleProvider("lrclib", on)} />
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="Save fetched lyrics"
                  description={
                    saveLocation === "beside"
                      ? "The .lrc lands beside the audio, one file per track in the album's own folder."
                      : "The .lrc lands in a lyrics/ folder inside the album's own folder."
                  }
                >
                  {#snippet control()}
                    <Picker
                      label={null}
                      value={saveLocation}
                      options={[
                        { value: "lyrics", label: "Lyrics folder" },
                        { value: "beside", label: "Beside the track" },
                      ]}
                      onchange={(v) => setLyrics({ saveLocation: v })}
                    />
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="YouTube Music lyrics folder"
                  description="Where streamed tracks cache their lyric."
                >
                  {#snippet control()}
                    <div class="flex items-end gap-1.5">
                      <TextField
                        label={null}
                        ariaLabel="YouTube Music lyrics folder"
                        value={lyricsFolder}
                        placeholder="Default"
                        onchange={(v) => setLyrics({ folder: v })}
                      />
                      <Button label="Browse" onclick={browseLyricsFolder} />
                    </div>
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="Clear lyrics cache"
                  description="Clear Fetched YouTube Music Lyric cache"
                >
                  {#snippet control()}
                    <Button label="Clear" busyLabel="Clearing" onclick={clearLyricsCache} />
                  {/snippet}
                </SettingRow>
              </div>
            </SettingGroup>
          </div>
        {:else if tab === "integration"}
          <div class="divide-y divide-line">
            <SettingRow
              label="Discord Rich Presence"
              description={discordStatusLine}
            >
              {#snippet control()}
                <Switch
                  checked={$discordStatus.enabled}
                  label="Discord Rich Presence"
                  onchange={toggleDiscord}
                />
              {/snippet}
            </SettingRow>

            <div class="py-2.5">
              <DiscordCard />
            </div>
          </div>
        {:else if tab === "stream"}
          <p class="px-1 py-6 text-center text-xs text-muted">
            No stream settings yet. YouTube Music and network options will live here.
          </p>
        {:else}
          <div class="divide-y divide-line">
            <SettingGroup label="Visualizer" description={null}>
              <!-- The rows keep their own labels and dividers inside the
                   group; the shell only names the block, so a later knob is
                   one more row here rather than a new section. -->
              <div class="divide-y divide-line">
                <SettingRow
                  label="Album player visualizer"
                  description="Album player's Visualizer width. Turn this off if you are having a performance issue."
                >
                  {#snippet control()}
                    <Picker
                      label={null}
                      value={visualizerStyleValue}
                      options={[
                        { value: "full", label: "Full" },
                        { value: "minimal", label: "Minimal" },
                        { value: "off", label: "Off" },
                      ]}
                      onchange={setVisualizerStyle}
                    />
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="Visualizer delay"
                  description="Adjust if the visualizer runs ahead of the beat vice versa"
                >
                  {#snippet control()}
                    <div class="w-48 self-center">
                      <Slider
                        label="Delay"
                        value={$appSettings.general.visualizerDelayMs}
                        min={0}
                        max={500}
                        step={5}
                        unit="ms"
                        format={(v) => `${v} ms`}
                        onchange={setVisualizerDelay}
                      />
                    </div>
                  {/snippet}
                </SettingRow>

                <SettingRow
                  label="Dynamic scale"
                  description="When enabled the bars become more 'Punchy'"
                >
                  {#snippet control()}
                    <Switch
                      checked={dynamicScale}
                      label="Dynamic scale"
                      onchange={(on) => setSpectrum({ scale: on ? "dynamic" : "fixed" })}
                    />
                  {/snippet}
                </SettingRow>

                <div class="py-2.5">
                  <!-- The knobs dim together when the visualizer is off,
                       because none of them changes anything the user can see
                       while it is hidden. -->
                  <div
                    class="grid grid-cols-2 gap-x-4 gap-y-3"
                    class:opacity-40={!$spectrumConfig.enabled}
                  >
                    {#each $spectrumSchema as p (p.key)}
                      {#if p.kind === "choice"}
                        <!-- A choice has no continuous range, so the set the
                             schema names is rendered instead of a slider over a
                             span it cannot take. -->
                        <Picker
                          label={p.label}
                          value={String(spectrumValue(p))}
                          disabled={!$spectrumConfig.enabled}
                          options={(p.choices ?? []).map((c) => ({ value: String(c), label: fftLabel(c) }))}
                          onchange={(v) => onSpectrumParam(p, Number(v))}
                        />
                      {:else}
                        <Slider
                          label={p.label}
                          value={spectrumValue(p)}
                          min={p.min}
                          max={p.max}
                          step={p.step}
                          disabled={!$spectrumConfig.enabled}
                          format={(v) => (p.key === "minHz" || p.key === "maxHz" ? `${v} Hz` : String(v))}
                          onchange={(v) => onSpectrumParam(p, v)}
                        />
                      {/if}
                    {/each}
                  </div>
                </div>
              </div>
            </SettingGroup>
          </div>
        {/if}
      </div>

      <div class="flex shrink-0 items-center gap-3 border-t border-line px-[clamp(0.5rem,1.6vw,1.25rem)] py-[clamp(0.375rem,1vh,0.75rem)] text-[11px] text-muted">
        <span>Settings apply as you change them.</span>
        <span class="ml-auto"><kbd class="font-sans text-fg">Esc</kbd> to close</span>
      </div>
    </div>
  </div>
{/if}
