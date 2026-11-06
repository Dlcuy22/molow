# Changelog

All notable changes to MoLow are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the major version stays at 0, a minor release may break the settings file
without a deprecation cycle.

## [Unreleased]

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

[Unreleased]: https://github.com/Dlcuy22/molow/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Dlcuy22/molow/releases/tag/v0.1.0
