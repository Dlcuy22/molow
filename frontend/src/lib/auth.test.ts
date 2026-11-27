import { get } from "svelte/store";
import { beforeEach, describe, expect, it, vi } from "vitest";

const checkKeyringCall = vi.fn();
const connectedLibrariesCall = vi.fn();
const createBrowserCall = vi.fn();
const createCookieCall = vi.fn();
const reSignInCall = vi.fn();
const reSignInCookieCall = vi.fn();
const refreshLibraryAuthCall = vi.fn();
const signOutLibraryCall = vi.fn();
const refreshLibrariesCall = vi.fn();
const selectLibraryCall = vi.fn();

vi.mock("../../bindings/github.com/dlcuy22/molow/backend", () => ({
  PlayerService: {
    CheckKeyring: () => checkKeyringCall(),
    ConnectedLibraries: () => connectedLibrariesCall(),
    CreateYTMLibraryWithBrowser: (name: string, desc: string) => createBrowserCall(name, desc),
    CreateYTMLibraryWithCookie: (name: string, desc: string, header: string) =>
      createCookieCall(name, desc, header),
    ReSignInLibrary: (id: string) => reSignInCall(id),
    ReSignInLibraryWithCookie: (id: string, header: string) => reSignInCookieCall(id, header),
    RefreshLibraryAuth: (id: string) => refreshLibraryAuthCall(id),
    SignOutLibrary: (id: string, del: boolean) => signOutLibraryCall(id, del),
  },
}));

vi.mock("./library", async () => {
  const { writable } = await import("svelte/store");
  return {
    libraries: writable<{ id: string; kind: string; name: string }[]>([]),
    refreshLibraries: () => refreshLibrariesCall(),
    selectLibrary: (id: string) => selectLibraryCall(id),
  };
});

import { libraries as mockLibraries } from "./library";

import {
  authBusy,
  authDialog,
  authError,
  connectedLibraries,
  createYTMLibraryWithBrowser,
  createYTMLibraryWithCookie,
  keyringError,
  openReauthDialog,
  openSignOutDialog,
  refreshLibraryAuth,
  reSignInWithCookieHeader,
  reSignInWithWindow,
  signOutDialog,
  signOutLibrary,
} from "./auth";

describe("per-library ytm auth store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    connectedLibraries.set([]);
    authDialog.set(null);
    signOutDialog.set(null);
    authBusy.set(false);
    authError.set("");
    keyringError.set("");
    (mockLibraries as unknown as { set: (v: { id: string; kind: string; name: string }[]) => void }).set(
      [],
    );
  });

  it("blocks re-sign-in and surfaces warning when keyring pre-flight fails", async () => {
    checkKeyringCall.mockRejectedValue(
      new Error("keyring: system keyring is unavailable: org.freedesktop.secrets missing"),
    );

    await openReauthDialog("ytm_1", "Personal YTM");

    expect(get(authDialog)).toEqual({ libraryId: "ytm_1", libraryName: "Personal YTM", kind: "ytm" });
    expect(get(keyringError)).toContain("org.freedesktop.secrets");

    const ok = await reSignInWithWindow();
    expect(ok).toBe(false);
    expect(reSignInCall).not.toHaveBeenCalled();
    expect(get(keyringError)).toContain("org.freedesktop.secrets");
  });

  it("creates a YouTube Music library via browser and activates it", async () => {
    checkKeyringCall.mockResolvedValue(undefined);
    createBrowserCall.mockResolvedValue({ id: "ytm_a1b2", kind: "ytm", name: "Personal YTM" });
    connectedLibrariesCall.mockResolvedValue([
      { libraryId: "ytm_a1b2", libraryName: "Personal YTM", kind: "ytm", signedIn: true },
    ]);
    refreshLibrariesCall.mockImplementation(async () => {
      (mockLibraries as unknown as { set: (v: { id: string; kind: string; name: string }[]) => void }).set(
        [{ id: "ytm_a1b2", kind: "ytm", name: "Personal YTM" }],
      );
    });

    const created = await createYTMLibraryWithBrowser("Personal YTM", "Likes");

    expect(createBrowserCall).toHaveBeenCalledWith("Personal YTM", "Likes");
    expect(created).toMatchObject({ id: "ytm_a1b2", name: "Personal YTM" });
    expect(get(connectedLibraries)).toHaveLength(1);
    expect(selectLibraryCall).toHaveBeenCalledWith("ytm_a1b2");
    expect(get(authError)).toBe("");
  });

  it("rejects empty pasted cookie header without calling backend", async () => {
    const created = await createYTMLibraryWithCookie("Personal", "", "   ");

    expect(created).toBeNull();
    expect(checkKeyringCall).not.toHaveBeenCalled();
    expect(createCookieCall).not.toHaveBeenCalled();
    expect(get(authError)).toContain("Cookie header");
  });

  it("creates a YouTube Music library from cookie header", async () => {
    checkKeyringCall.mockResolvedValue(undefined);
    createCookieCall.mockResolvedValue({ id: "ytm_c3d4", kind: "ytm", name: "Cookie YTM" });
    connectedLibrariesCall.mockResolvedValue([
      { libraryId: "ytm_c3d4", libraryName: "Cookie YTM", kind: "ytm", signedIn: true },
    ]);
    refreshLibrariesCall.mockImplementation(async () => {
      (mockLibraries as unknown as { set: (v: { id: string; kind: string; name: string }[]) => void }).set(
        [{ id: "ytm_c3d4", kind: "ytm", name: "Cookie YTM" }],
      );
    });

    const created = await createYTMLibraryWithCookie("Cookie YTM", "", "SAPISID=a; SID=b");

    expect(createCookieCall).toHaveBeenCalledWith("Cookie YTM", "", "SAPISID=a; SID=b");
    expect(created).toMatchObject({ id: "ytm_c3d4" });
  });

  it("re-signs in for an existing library via browser or cookie and closes authDialog", async () => {
    checkKeyringCall.mockResolvedValue(undefined);
    reSignInCall.mockResolvedValue(undefined);
    reSignInCookieCall.mockResolvedValue(undefined);
    connectedLibrariesCall.mockResolvedValue([
      { libraryId: "ytm_1", libraryName: "Personal YTM", kind: "ytm", signedIn: true, expired: false },
    ]);
    refreshLibrariesCall.mockResolvedValue(undefined);

    await openReauthDialog("ytm_1", "Personal YTM");
    expect(await reSignInWithWindow()).toBe(true);
    expect(reSignInCall).toHaveBeenCalledWith("ytm_1");
    expect(get(authDialog)).toBeNull();

    await openReauthDialog("ytm_1", "Personal YTM");
    expect(await reSignInWithCookieHeader("SAPISID=new; SID=new")).toBe(true);
    expect(reSignInCookieCall).toHaveBeenCalledWith("ytm_1", "SAPISID=new; SID=new");
    expect(get(authDialog)).toBeNull();
  });

  it("refreshes auth status for one connected library", async () => {
    refreshLibraryAuthCall.mockResolvedValue({
      libraryId: "ytm_1",
      libraryName: "Personal YTM",
      kind: "ytm",
      signedIn: true,
      expired: false,
    });
    connectedLibrariesCall.mockResolvedValue([
      { libraryId: "ytm_1", libraryName: "Personal YTM", kind: "ytm", signedIn: true, expired: false },
    ]);
    refreshLibrariesCall.mockResolvedValue(undefined);

    const status = await refreshLibraryAuth("ytm_1");

    expect(refreshLibraryAuthCall).toHaveBeenCalledWith("ytm_1");
    expect(status?.signedIn).toBe(true);
    expect(get(connectedLibraries)).toHaveLength(1);
  });

  it("passes libraryId and deleteLibrary on signOutLibrary and closes confirmation dialog", async () => {
    signOutLibraryCall.mockResolvedValue(undefined);
    connectedLibrariesCall.mockResolvedValue([]);
    refreshLibrariesCall.mockResolvedValue(undefined);

    openSignOutDialog("ytm_1", "Personal YTM");
    expect(get(signOutDialog)).toEqual({ libraryId: "ytm_1", libraryName: "Personal YTM" });
    await signOutLibrary(false);

    expect(signOutLibraryCall).toHaveBeenCalledWith("ytm_1", false);
    expect(get(signOutDialog)).toBeNull();

    openSignOutDialog("ytm_1", "Personal YTM");
    await signOutLibrary(true);
    expect(signOutLibraryCall).toHaveBeenLastCalledWith("ytm_1", true);
  });
});
