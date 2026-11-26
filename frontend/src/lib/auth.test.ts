import { get } from "svelte/store";
import { beforeEach, describe, expect, it, vi } from "vitest";

const checkKeyringCall = vi.fn();
const authStatusCall = vi.fn();
const accountsCall = vi.fn();
const signInCall = vi.fn();
const signInWithCookieCall = vi.fn();
const signOutCall = vi.fn();
const signOutWithOptionsCall = vi.fn();
const switchChannelCall = vi.fn();
const refreshAuthCall = vi.fn();
const refreshLibrariesCall = vi.fn();
const selectLibraryCall = vi.fn();

vi.mock("../../bindings/github.com/dlcuy22/molow/backend", () => ({
  PlayerService: {
    CheckKeyring: () => checkKeyringCall(),
    AuthStatus: () => authStatusCall(),
    Accounts: () => accountsCall(),
    SignIn: () => signInCall(),
    SignInWithCookieHeader: (header: string) => signInWithCookieCall(header),
    SignOut: () => signOutCall(),
    SignOutWithOptions: (del: boolean) => signOutWithOptionsCall(del),
    SwitchChannel: (id: string) => switchChannelCall(id),
    RefreshAuth: () => refreshAuthCall(),
  },
}));

vi.mock("./library", async () => {
  const { writable } = await import("svelte/store");
  return {
    libraries: writable<{ id: string; kind: string }[]>([]),
    refreshLibraries: () => refreshLibrariesCall(),
    selectLibrary: (id: string) => selectLibraryCall(id),
  };
});

import { libraries as mockLibraries } from "./library";

import {
  authAccounts,
  authBusy,
  authDialogOpen,
  authError,
  authStatus,
  keyringError,
  openAuthDialog,
  openSignOutDialog,
  signInWithCookieHeader,
  signInWithWindow,
  signOut,
  signOutDialogOpen,
} from "./auth";

describe("ytm auth store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authStatus.set({ signedIn: false });
    authAccounts.set([]);
    authBusy.set(false);
    authError.set("");
    keyringError.set("");
    authDialogOpen.set(false);
    signOutDialogOpen.set(false);
    (mockLibraries as unknown as { set: (v: { id: string; kind: string }[]) => void }).set([]);
  });

  it("blocks sign-in and surfaces warning when keyring pre-flight fails", async () => {
    checkKeyringCall.mockRejectedValue(
      new Error("keyring: system keyring is unavailable: org.freedesktop.secrets missing"),
    );

    await openAuthDialog();

    expect(get(authDialogOpen)).toBe(true);
    expect(get(keyringError)).toContain("org.freedesktop.secrets");

    const ok = await signInWithWindow();
    expect(ok).toBe(false);
    expect(signInCall).not.toHaveBeenCalled();
    expect(get(keyringError)).toContain("org.freedesktop.secrets");
  });

  it("completes browser sign-in, populates status, and selects the YTM library", async () => {
    checkKeyringCall.mockResolvedValue(undefined);
    signInCall.mockResolvedValue(undefined);
    authStatusCall.mockResolvedValue({
      signedIn: true,
      name: "Kasaki Nomaraba",
      email: "kasakinomaraba@gmail.com",
      channelId: "UC123",
    });
    accountsCall.mockResolvedValue([
      { name: "Kasaki Nomaraba", channelId: "UC123", selected: true },
    ]);
    refreshLibrariesCall.mockImplementation(async () => {
      (mockLibraries as unknown as { set: (v: { id: string; kind: string }[]) => void }).set([
        { id: "ytm:UC123", kind: "ytm" },
      ]);
    });

    await openAuthDialog();
    const ok = await signInWithWindow();

    expect(ok).toBe(true);
    expect(get(authDialogOpen)).toBe(false);
    expect(get(authStatus)).toMatchObject({ signedIn: true, name: "Kasaki Nomaraba" });
    expect(get(authAccounts)).toHaveLength(1);
    expect(refreshLibrariesCall).toHaveBeenCalledTimes(1);
    expect(selectLibraryCall).toHaveBeenCalledWith("ytm:UC123");
    expect(get(authError)).toBe("");
  });

  it("rejects empty pasted cookie header without calling backend", async () => {
    const ok = await signInWithCookieHeader("   ");

    expect(ok).toBe(false);
    expect(checkKeyringCall).not.toHaveBeenCalled();
    expect(signInWithCookieCall).not.toHaveBeenCalled();
    expect(get(authError)).toContain("Cookie header");
  });

  it("passes deleteLibrary option on signOut and closes confirmation dialog", async () => {
    authStatus.set({ signedIn: true, name: "Kasaki Nomaraba" });
    authAccounts.set([{ name: "Kasaki Nomaraba", channelId: "UC123", selected: true }]);
    signOutWithOptionsCall.mockResolvedValue(undefined);
    refreshLibrariesCall.mockResolvedValue(undefined);

    openSignOutDialog();
    expect(get(signOutDialogOpen)).toBe(true);
    await signOut(false);

    expect(signOutWithOptionsCall).toHaveBeenCalledWith(false);
    expect(get(signOutDialogOpen)).toBe(false);
    expect(get(authStatus)).toEqual({ signedIn: false });
    expect(get(authAccounts)).toEqual([]);
    expect(refreshLibrariesCall).toHaveBeenCalledTimes(1);

    await signOut(true);
    expect(signOutWithOptionsCall).toHaveBeenLastCalledWith(true);
  });
});
