import { describe, expect, it } from "vitest";
import {
  artUrl,
  avatarArtUrl,
  resizeRemoteArtUrl,
  snapArtSize,
  ytmSearchArtUrl,
} from "./artwork";

describe("snapArtSize", () => {
  it("quantizes common slot sizes across display pixel ratios", () => {
    const cases: Array<[number, number, 64 | 128 | 256 | 512]> = [
      [24, 1, 64],
      [32, 1, 64],
      [40, 1, 64],
      [64, 1, 64],
      [72, 1, 128],
      [96, 1, 128],
      [166, 1, 256],
      [220, 1, 256],
      [520, 1, 512],

      [24, 1.25, 64],
      [40, 1.25, 64],
      [72, 1.25, 128],
      [166, 1.25, 256],
      [220, 1.25, 512],

      [24, 1.5, 64],
      [40, 1.5, 64],
      [72, 1.5, 128],
      [96, 1.5, 256],
      [166, 1.5, 256],
      [220, 1.5, 512],

      [24, 2, 64],
      [32, 2, 64],
      [40, 2, 128],
      [64, 2, 128],
      [72, 2, 256],
      [96, 2, 256],
      [166, 2, 512],
      [520, 2, 512],
    ];
    for (const [cssPx, dpr, want] of cases) {
      expect(snapArtSize(cssPx, dpr)).toBe(want);
    }
  });
});

describe("artUrl", () => {
  it("returns empty string for empty id", () => {
    expect(artUrl("", 40, 1)).toBe("");
    expect(artUrl("", 40, 1, "/music/song.flac")).toBe("");
  });

  it("includes snapped size and encodes path cleanly without double ?", () => {
    expect(artUrl("abc123", 40, 1)).toBe("/art/abc123?s=64");
    expect(artUrl("abc123", 40, 1, "/music/a&b.mp3")).toBe(
      "/art/abc123?s=64&path=%2Fmusic%2Fa%26b.mp3",
    );
    expect(artUrl("abc123", 166, 1).match(/\?/g)?.length).toBe(1);
    expect(artUrl("abc123", 166, 1, "ytm:vid").match(/\?/g)?.length).toBe(1);
  });

  it("formats search and avatar URLs with snapped size", () => {
    expect(ytmSearchArtUrl("vid/1", 32, 1)).toBe("/art/ytm-search/vid%2F1?s=64");
    expect(avatarArtUrl("UC123", 24, 1)).toBe("/art/avatar/UC123?s=64");
  });
});

describe("resizeRemoteArtUrl", () => {
  it("rewrites Google image CDN size parameters", () => {
    expect(
      resizeRemoteArtUrl(
        "https://lh3.googleusercontent.com/abc=w544-h544-l90-rj",
        166,
        1,
      ),
    ).toBe("https://lh3.googleusercontent.com/abc=w256-h256-l90-rj");

    expect(
      resizeRemoteArtUrl(
        "https://lh3.googleusercontent.com/banner=w1440-h600-p-k-no-nu",
        112,
        1,
      ),
    ).toBe("https://lh3.googleusercontent.com/banner=w128-h128-p-k-no-nu");

    expect(resizeRemoteArtUrl("https://yt3.ggpht.com/avatar=s120", 36, 1)).toBe(
      "https://yt3.ggpht.com/avatar=w64-h64",
    );

    expect(
      resizeRemoteArtUrl(
        "https://lh3.googleusercontent.com/abc=s120-c-k-no?authuser=0",
        40,
        2,
      ),
    ).toBe("https://lh3.googleusercontent.com/abc=w128-h128-c-k-no?authuser=0");
  });

  it("leaves non-Google and non-HTTP URLs untouched", () => {
    const ytImg = "https://i.ytimg.com/vi/abc123/hqdefault.jpg";
    expect(resizeRemoteArtUrl(ytImg, 240, 1)).toBe(ytImg);
    expect(resizeRemoteArtUrl("/art/local?s=64", 40, 1)).toBe("/art/local?s=64");
    expect(resizeRemoteArtUrl("data:image/png;base64,AAAA", 40, 1)).toBe(
      "data:image/png;base64,AAAA",
    );
    expect(resizeRemoteArtUrl("", 40, 1)).toBe("");
  });
});
