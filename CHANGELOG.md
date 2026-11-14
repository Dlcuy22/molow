# Changelog

All notable changes to MoLow are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the major version stays at 0, a minor release may break the settings file
without a deprecation cycle.

## [Unreleased]

## [0.5.0] - 2026-11-14

### Added

- A frame-rate dial for the visualizer: 30, 60, 120 or 180 frames a second,
  drawn as a slider over its stops. Changing the rate reschedules the analysis
  pump without rebuilding the transform, so the display keeps its live window,
  and the pause and scale motion stay the same in wall time at any rate.
- A visualizer delay setting: the strip renders ahead of what the speakers
  output by the analysis window's lead, so a small delay (25 ms by default)
  lines the bars up with the sound.
- Arrow keybinds: Right and Left nudge the seek position, Up and Down step the
  volume.
- A one-shot action reports itself through a toast: saving an effect preset,
  clearing the fetched lyric cache, and the like confirm what they did.
- A way to clear the fetched lyric cache from the Lyrics group, so sheets
  gathered from a provider can be fetched again from scratch.
- The debug console tints its levels, collapses repeated lines and matches the
  Wails CLI's timestamp style, so a `MOLOW_DEBUG` run reads like the rest of
  the toolchain.

### Changed

- The pumps idle when nothing is drawing: a minimised main window or a closed
  album player stops the spectrum pump, a closed effects window stops the
  effect meters, and an unmounted display no longer costs the transform, the
  bridge and the repaint. Measured on Windows, an idle tree drops from about
  11 % CPU to about 3 %.
- The seek bar's fill glides as a compositor transform instead of a width
  transition, so playing no longer re-runs layout for every animation frame.
- The settings panel groups its controls into section-labelled groups.
- The toast cards were reworked: each message is its own card, stacked with
  its own id and lifetime.
- The packaging metadata is rebranded to molow.
- The thin scrollbar takes its colour through `scrollbar-color` instead of a
  custom-painted thumb.
- The sliders no longer paint their focus ring; a control's own state carries
  the feedback.
- The main window opens 100 px wider.
- Icons import from their deep paths, which trims the bundle the webview has
  to load.

### Fixed

- A YouTube Music stream refusal rotates the stored session instead of failing
  the track, so an expired cookie set recovers on the next play.
- A shared artist tail no longer matches every track in the folder: lyric
  sidecars resolve against the track's own stem first.

## [0.4.0] - 2026-11-11

### Added

- A lyric provider chain in `internal/lyrics`: PetitLyrics, BetterLyrics,
  LyricsPlus, Kugou and LRCLIB, all on by default and each toggled in a new
  Lyrics group under Playback. The first provider that answers with a sheet
  wins, and the sheet is written as a sidecar, so the next play reads it from
  disk.
- Word-synced lyrics: a sheet that carries per-word marks is drawn word by word,
  and a sheet with only line timing still plays line by line.
- A save location for the sheets a provider returns: a `lyrics/` folder beside
  the track or the `.lrc` next to the file, with the YouTube Music cache in a
  folder of its own.
- Two visualizer layouts: Full draws 256 points across the whole album sheet,
  Minimal keeps the 72 px strip at 150. The layout is stored apart from the
  on/off switch, so turning the visualizer back on restores the chosen one.
- `MOLOW_DEBUG` raises the log level to debug for a run, so the lines below INFO
  reach the log.

### Changed

- Artwork thumbnails decode at 512 px on the longest side instead of 320, which
  keeps the now-playing tile crisp above 1x display scaling.
- A YouTube Music thumbnail takes the largest still the video offers instead of
  a fixed size.
- The browser focus outline no longer draws, since it appeared around the whole
  settings dialog as soon as Ctrl+I focused the panel; a control's own state
  carries the feedback.

### Fixed

- Escape closes only the topmost surface. The settings panel and the command
  palette take the press first, so the album player no longer minimizes under an
  open dialog.

## [0.3.0] - 2026-11-10

### Added

- YouTube Music lyrics: lyrics resolve for streamed tracks, fetched from the
  stream through a new `ytm_lyrics` module.
- A visualizer on the album player: the app's spectrum pump draws a 72 px strip
  in the foreground colour at half opacity over the artwork gradient, hidden
  when the visualizer switch is off.
- A vertical slide for the album player: the sheet glides up when opened and
  back down when minimized (340 ms ease, disabled under reduced motion).
- Clickable lyrics: every timed line is a seek control that jumps the track to
  that second, and the lyric column scrolls freely, gliding back to the sung
  line two seconds after the browsing stops.
- The pointer fades out over the album player when the mouse rests for two
  seconds; any input brings it back.
- A playback setting "Play/Pause control style": the dock's inverted disc
  (Default) or the album player's bare glyph (Simple), persisted in the
  settings file.

### Changed

- Lyrics without timestamps are rejected on load instead of rendering unplayed
  lines.
- The album player matches the design: a bare 32 px play glyph, dock-style
  hover tiles, a marquee title, and the seek rail and artist name dimmed to the
  frame's 27 % and 56 % opacities.
- The sidebar's Home, Search, Library and Settings rows take the same hover
  tint as the rest of the app.
- The album player's seek bar caps are flattened and shaped by a single clip
  edge, so filling and track never mismatch at high zoom.
- The app identity is MoLow, "music player and library manager", in the
  window, the about box and the bundle metadata; dependencies upgraded (ytm-go
  1.6.0).

### Fixed

- The dock's seek hover no longer fires from the rest of the dock: the thickened
  progress line now reacts only to the bar's own hit field, not to the cover
  thumb or title.
- The README carries current screenshots of the library and now playing views.

## [0.2.0] - 2026-11-10

### Added

- The library overhaul: a SQLite catalogue at `<config>/molo/library.db`, a
  folder scanner with file identity, full-text search, likes and playlist rows,
  and the `Libraries`, `LibraryFeed`, `LibrarySearch`, `SetLiked` and playlist
  bindings from `docs/plan/library-overhaul.md`.
- A rebuilt interface that follows `DESIGN.md`: sidebar, content and dock, with
  Home, Library and the full album player. Work Sans, Inter, Noto Sans and Noto
  Sans JP ship with the frontend.
- The queue window: opened from the dock, unpinned or pinned, with drag
  reordering that keeps the playing track by identity (`MoveQueue` in the
  engine) and a vertical ease-in-out open.
- Lyrics: sidecar `.lrc` beside the track or in a `lyrics/` folder, matched with
  a leading track index removed, with bilingual pairs and translation files.
- Cover-derived album player gradients, ported from
  `tools/dominant_color/dominant_color.py` into `internal/color`.
- A draggable progress bar in the dock, the floating bar and the album player,
  with a hover-revealed thumb; docked bars put it on the edge facing the content.
- A marquee dock title, shown only when the name does not fit before the
  transport.

### Changed

- The window plane is `bg-dark` (`#131315`); the dock and sidebar are `bg` and
  panels are `surface`, in the order `DESIGN.md` defines.
- `app.css` keeps every colour in HSL and adds the four bundled faces, the
  radius scale (4/6/8/16) and the panel shadows.
- Dock controls have hit areas larger than their glyphs, scale on hover, and the
  toggles and icon buttons take the same rounded tile as the like button.
- The volume rail reveals its thumb only on hover or keyboard focus, like the
  progress bar.

## [0.1.0] - 2026-11-06

First release.

### Added

- Playback of MP3, FLAC, WAV, Opus (`.opus`, `.ogg`), AAC (`.aac`), M4A/MP4, and
  Ogg Opus in WebM/Matroska. Every format is resampled to 48 kHz stereo float32
  before the audio device, so one stream serves every track.
- Local playback from a single file, a whole folder, or files dropped on the
  window, with a persistent queue and a hover-revealed seek bar.
- YouTube Music streaming with cookie-based sign-in, session restore across
  launches, account and channel switching, and playlist import.
- Equalizer built on RBJ biquads, with presets, a fader panel, ballistic level
  meters, and on-demand bands.
- A Lua effects API, with bundled Go and Lua effects.
- Spectrum visualizer, shuffle and repeat modes, and space as a play/pause key.
- System media integration through MPRIS2 on Linux and SMTC on Windows.
- Builds for Linux, Windows, and macOS, plus a server build that runs the same
  backend as an HTTP server.

[Unreleased]: https://github.com/Dlcuy22/molow/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/Dlcuy22/molow/releases/tag/v0.4.0
[0.3.0]: https://github.com/Dlcuy22/molow/releases/tag/v0.3.0
[0.2.0]: https://github.com/Dlcuy22/molow/releases/tag/v0.2.0
[0.1.0]: https://github.com/Dlcuy22/molow/releases/tag/v0.1.0
