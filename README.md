# MoLow

## What is this?

MoLow is a feature-rich yet lightweight audio player. It
plays most common audio formats and codecs (see the
[codec support table](#codec-and-container-support)).

It covers the basics of an audio player like playback control, parsing and
loading local songs from a single file or a folder. It can also stream music
directly from online music libraries notably YouTube Music through
[ytm-go](https://github.com/dlcuy22/ytm-go) and network, and it supports system media
protocols such as MPRIS2 and SMTC.

MoLow is cross-platform. The library behind it,
[molo](https://github.com/dlcuy22/molo), is built with portability in mind, and
the UI is built with [Wails](https://v3.wails.io), which makes building for
other platforms straightforward (an Android version is coming soon).

MoLow is extensible: you can write your own effects in Lua through its Effects
API. Some of the effects bundled in `effects/` are written in Lua, and it also
ships built-in effects written in Go, EQ among them.

## Project state

MoLow is at its earliest stage of development. I spent my time building a
ready-to-use audio library before focusing on this UI implementation.

The current UI is functional and every control is in place, but it is still not
perfect, and I am working on it.

## Codec and container support
| Name | Support | Note |
|---|---|---|
| MP3 | Yes | MPEG-1/2/2.5 Layer III |
| FLAC | Yes | Lossless, 16- and 24-bit |
| WAV | Yes | Integer PCM 8/16/24/32-bit and IEEE float 32/64-bit |
| Opus (`.opus`, `.ogg`) | Yes | Pure Go; the optional `libopusfile` path seeks faster |
| Ogg Vorbis | No | Only Ogg Opus is read |
| AAC (`.aac`) | Yes | AAC-LC in ADTS|
| M4A / MP4 | Yes | AAC-LC only |
| ALAC | No | Not implemented |
| WebM / Matroska | Yes | Opus audio only |
| AIFF | No | Not implemented |
| WMA | No | Not implemented |
| APE | No | Not implemented |
| DSD (`.dsf`, `.dff`) | No | Not implemented |

Special thanks to [tphakala](https://github.com/tphakala) for providing most of
the codec implementations in pure Go.

Every format is normalized to 48 kHz stereo float32 before it reaches the audio
device, so one output stream serves every track.


## Developing

First, make sure the Wails 3 tooling is installed on your system:
https://v3.wails.io/quick-start/installation/

If you are not familiar with Wails 3, I strongly suggest reading its beginner
tutorial to understand the basics: https://v3.wails.io/tutorials/03-notes-vanilla/

After that, you can use `wails3 dev` and `wails3 build` to
work in this repository.

## Stack

- Backend and system language: Go
- Scripting language: Lua
- Frontend: TypeScript + Tailwind CSS + Svelte 5 + Vite + Bun

## License

MoLow is released under the [BSD 3-Clause License](LICENSE).

It links two components whose licenses ask more than the BSD notice does, and
both are listed in [NOTICE](NOTICE): `go-aac` is LGPL-2.1-or-later, so a
redistributed binary has to ship that license text and point at the library's
source, and `ebitengine/oto` is Apache-2.0. Everything else, including the molo
engine and ytm-go, is MIT or BSD.
