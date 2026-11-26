export type ArtBucket = 64 | 128 | 256 | 512;

const ART_BUCKETS: readonly ArtBucket[] = [64, 128, 256, 512];
const GOOGLE_ART_HOST = /(?:^|\.)(?:googleusercontent\.com|ggpht\.com)$/i;
const GOOGLE_SIZE_SUFFIX = /=(?:w\d+-h\d+|s\d+)([^/?#]*)(?=[?#]|$)/;

export function deviceDpr(): number {
  if (typeof window === "undefined") {
    return 1;
  }
  const dpr = window.devicePixelRatio;
  return Number.isFinite(dpr) && dpr > 0 ? dpr : 1;
}

// Quantize physical pixels to the four Go-cached buckets so WebKitGTK stays within ~1x-2x.
export function snapArtSize(cssPx: number, dpr = deviceDpr()): ArtBucket {
  const target = Math.ceil(Math.max(1, cssPx) * Math.max(1, dpr));
  for (const bucket of ART_BUCKETS) {
    if (target <= bucket) {
      return bucket;
    }
  }
  return 512;
}

export function artUrl(
  id: string,
  cssPx: number,
  dpr = deviceDpr(),
  pathQuery = "",
): string {
  if (id === "") {
    return "";
  }
  const params = new URLSearchParams();
  params.set("s", String(snapArtSize(cssPx, dpr)));
  if (pathQuery !== "") {
    params.set("path", pathQuery);
  }
  return `/art/${encodeURIComponent(id)}?${params.toString()}`;
}

export function ytmSearchArtUrl(
  videoId: string,
  cssPx: number,
  dpr = deviceDpr(),
): string {
  if (videoId === "") {
    return "";
  }
  return `/art/ytm-search/${encodeURIComponent(videoId)}?s=${snapArtSize(cssPx, dpr)}`;
}

export function avatarArtUrl(
  artistId: string,
  cssPx: number,
  dpr = deviceDpr(),
): string {
  if (artistId === "") {
    return "";
  }
  return `/art/avatar/${encodeURIComponent(artistId)}?s=${snapArtSize(cssPx, dpr)}`;
}

export function resizeRemoteArtUrl(
  url: string,
  cssPx: number,
  dpr = deviceDpr(),
): string {
  if (url === "") {
    return "";
  }
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return url;
  }
  if (!GOOGLE_ART_HOST.test(parsed.hostname)) {
    return url;
  }
  const edge = snapArtSize(cssPx, dpr);
  return url.replace(
    GOOGLE_SIZE_SUFFIX,
    (_m, tail: string) => `=w${edge}-h${edge}${tail}`,
  );
}
