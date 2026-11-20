import { beforeEach, describe, expect, it, vi } from "vitest";

const coverAccentsCall = vi.fn();

vi.mock("../../bindings/github.com/dlcuy22/molow/backend", () => ({
  PlayerService: {
    CoverAccents: (id: string) => coverAccentsCall(id),
  },
}));

import { cachedAccents, loadAccents } from "./library";

const samplePalette = [
  { r: 209, g: 91, b: 91 },
  { r: 236, g: 211, b: 147 },
];

describe("loadAccents", () => {
  beforeEach(() => {
    coverAccentsCall.mockReset();
  });

  it("caches resolved stops by coverId and serves cachedAccents synchronously", async () => {
    coverAccentsCall.mockResolvedValue(samplePalette);

    const first = await loadAccents("cover-a");
    expect(first).toEqual(["#ecd393", "#d15b5b"]);
    expect(cachedAccents("cover-a")).toEqual(["#ecd393", "#d15b5b"]);

    const second = await loadAccents("cover-a");
    expect(second).toEqual(first);
    expect(coverAccentsCall).toHaveBeenCalledTimes(1);
  });

  it("coalesces concurrent requests for the same coverId", async () => {
    let resolve!: (v: typeof samplePalette) => void;
    coverAccentsCall.mockReturnValue(
      new Promise<typeof samplePalette>((r) => {
        resolve = r;
      }),
    );

    const p1 = loadAccents("cover-concurrent");
    const p2 = loadAccents("cover-concurrent");
    expect(coverAccentsCall).toHaveBeenCalledTimes(1);

    resolve(samplePalette);
    expect(await Promise.all([p1, p2])).toEqual([
      ["#ecd393", "#d15b5b"],
      ["#ecd393", "#d15b5b"],
    ]);
  });

  it("does not cache empty misses or errors", async () => {
    coverAccentsCall.mockResolvedValueOnce([]);
    await expect(loadAccents("cover-miss")).resolves.toEqual([]);
    expect(cachedAccents("cover-miss")).toBeUndefined();

    coverAccentsCall.mockResolvedValueOnce(samplePalette);
    await expect(loadAccents("cover-miss")).resolves.toEqual(["#ecd393", "#d15b5b"]);
    expect(coverAccentsCall).toHaveBeenCalledTimes(2);
  });
});
