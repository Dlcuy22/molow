import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import type { Album, Artist, Playlist, Track, YtmArtist, YtmVideo } from "../types";
import BigArtistCard from "./BigArtistCard.svelte";
import PlaylistView from "./PlaylistView.svelte";
import TrackTile from "./TrackTile.svelte";
import VideoCard from "./VideoCard.svelte";
import YtmArtistView from "./YtmArtistView.svelte";

function makeTrack(i: number): Track {
  return {
    ref: `ytm:track-${i}`,
    libraryId: "ytm",
    nativeId: `track-${i}`,
    title: `Song ${i}`,
    artist: "40mP",
    album: `Album ${i}`,
    durationMs: 185000,
    coverId: "",
    imageUrl: `https://example.com/song-${i}.jpg`,
    liked: i === 1,
    flags: 0,
    trackNo: i,
  };
}

function makeAlbum(id: string, name: string): Album {
  return {
    id,
    libraryId: "ytm",
    name,
    artist: "40mP",
    coverId: "",
    imageUrl: `https://example.com/${id}.jpg`,
    songCount: 10,
    durationMs: 2400000,
    year: 2024,
  };
}

function makeArtist(id: string, name: string, subtitle = ""): Artist {
  return {
    id,
    libraryId: "ytm",
    name,
    coverId: "",
    imageUrl: `https://example.com/${id}.jpg`,
    subtitle,
    songCount: 0,
    albumCount: 0,
  };
}

function makeVideo(id: string, title: string): YtmVideo {
  return {
    id,
    title,
    subtitle: "1.2M views",
    durationMs: 215000,
    thumbnailUrl: `https://example.com/${id}.jpg`,
  };
}

describe("BigArtistCard", () => {
  it("renders a button when onopen is provided and a static card when omitted", () => {
    const artist = makeArtist("a1", "NANAOAKARI", "6.62M monthly audience");

    const interactive = render(BigArtistCard, {
      props: { artist, onopen: () => {} },
    });
    expect(interactive.body).toContain("<button");
    expect(interactive.body).toContain("rounded-full");
    expect(interactive.body).toContain("NANAOAKARI");
    expect(interactive.body).toContain("6.62M monthly audience");

    const staticCard = render(BigArtistCard, {
      props: { artist },
    });
    expect(staticCard.body).not.toContain("<button");
    expect(staticCard.body).toContain("NANAOAKARI");
  });
});

describe("VideoCard", () => {
  it("renders a 16:9 thumbnail with title, subtitle, and duration", () => {
    const { body } = render(VideoCard, {
      props: { video: makeVideo("v1", "Karakuri Pierrot MV") },
    });
    expect(body).toContain("aspect-video");
    expect(body).toContain("Karakuri Pierrot MV");
    expect(body).toContain("1.2M views");
    expect(body).toContain("3:35");
  });
});

describe("YtmArtistView", () => {
  const fullArtist: YtmArtist = {
    id: "UC40mP",
    name: "40mP",
    bannerUrl: "https://example.com/banner.jpg",
    subscriberText: "1.43M subscribers",
    topSongs: [1, 2, 3, 4, 5, 6].map(makeTrack),
    albums: [makeAlbum("alb1", "Life Size Note")],
    singles: [makeAlbum("sgl1", "Torinoko City")],
    videos: [makeVideo("vid1", "Karakuri Pierrot")],
    relatedArtists: [makeArtist("rel1", "Chinozo", "2.39M monthly audience")],
  };

  it("renders hero, Shuffle button, top 5 songs with Show all, and all populated shelves", () => {
    const { body } = render(YtmArtistView, {
      props: { artist: fullArtist },
    });

    expect(body).toContain("https://example.com/banner.jpg");
    expect(body).toContain("40mP");
    expect(body).toContain("1.43M subscribers");
    expect(body).toContain("Shuffle");
    expect(body).not.toContain(">Subscribe<");

    // Top songs capped at 5 with Show all button, and album shown without repeating artist name.
    expect(body).toContain("Song 1");
    expect(body).toContain("Song 5");
    expect(body).not.toContain("Song 6");
    expect(body).toContain("Show all");

    // Shelves
    expect(body).toContain("Top albums");
    expect(body).toContain("Life Size Note");
    expect(body).toContain("Singles");
    expect(body).toContain("Torinoko City");
    expect(body).toContain("Videos");
    expect(body).toContain("Karakuri Pierrot");
    expect(body).toContain("Related artists");
    expect(body).toContain("Chinozo");
  });

  it("hides empty shelves and shows Subscribe only when showSubscribe is true", () => {
    const minimal: YtmArtist = {
      id: "UCmin",
      name: "DIVELA",
      topSongs: [makeTrack(1)],
      albums: [],
      singles: [],
      videos: [],
      relatedArtists: [],
    };
    const { body } = render(YtmArtistView, {
      props: { artist: minimal, showSubscribe: true, subscribed: true },
    });

    expect(body).toContain("Subscribed");
    expect(body).toContain("Top songs");
    expect(body).not.toContain("Show all");
    expect(body).not.toContain("Top albums");
    expect(body).not.toContain("Singles");
    expect(body).not.toContain("Videos");
    expect(body).not.toContain("Related artists");
  });

  it("renders loading and error states cleanly", () => {
    const loading = render(YtmArtistView, { props: { loading: true } });
    expect(loading.body).toContain("Loading artist...");

    const errored = render(YtmArtistView, { props: { error: "network down" } });
    expect(errored.body).toContain("Could not load this artist");
    expect(errored.body).toContain("network down");
    expect(errored.body).toContain("Try again");
  });
});

describe("PlaylistView local artist mode", () => {
  it("renders circular artist artwork and hides playlist-only rename/options buttons", () => {
    const artist = makeArtist("local:a1", "Local Artist");
    const playlist: Playlist = {
      id: artist.id,
      libraryId: "local-1",
      name: artist.name,
      description: "",
      trackCount: 1,
      coverId: "",
      artist,
    };

    const { body } = render(PlaylistView, {
      props: { playlist, tracks: [makeTrack(1)] },
    });

    expect(body).toContain("rounded-full");
    expect(body).toContain("Local Artist");
    expect(body).toContain("Album order");
    expect(body).not.toContain("Rename playlist");
    expect(body).not.toContain("Playlist options");
    expect(body).not.toContain("Album options");
  });

  it("makes artist names clickable only for ytm tracks", () => {
    const ytmTrack = makeTrack(1);
    const localTrack: Track = { ...makeTrack(2), ref: "local:/music/a.flac", artist: "Local Act" };

    const ytmTile = render(TrackTile, {
      props: { track: ytmTrack, onopenArtist: () => {} },
    });
    expect(ytmTile.body).toContain('role="link"');

    const localTile = render(TrackTile, {
      props: { track: localTrack, onopenArtist: () => {} },
    });
    expect(localTile.body).not.toContain('role="link"');

    const pl: Playlist = {
      id: "pl1",
      libraryId: "lib1",
      name: "Mixed",
      description: "",
      trackCount: 2,
    };
    const ytmList = render(PlaylistView, {
      props: { playlist: pl, tracks: [ytmTrack], onopenTrackArtist: () => {} },
    });
    expect(ytmList.body).toContain('role="link"');

    const localList = render(PlaylistView, {
      props: { playlist: pl, tracks: [localTrack], onopenTrackArtist: () => {} },
    });
    expect(localList.body).not.toContain('role="link"');
  });
});
