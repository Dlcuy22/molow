import { get, writable } from "svelte/store";
import CheckCircle from "phosphor-svelte/lib/CheckCircle";
import WarningCircle from "phosphor-svelte/lib/WarningCircle";
import { PlayerService } from "../../bindings/github.com/dlcuy22/molow/backend";
import type { LibraryAuthStatus } from "../../bindings/github.com/dlcuy22/molow/backend/models";
import { libraries, refreshLibraries, selectLibrary } from "./library";
import { showToast } from "./toast";
import type { LibraryInfo } from "./types";

export type { LibraryAuthStatus };

export interface AuthDialogTarget {
  libraryId: string;
  libraryName: string;
  kind: string;
}

export interface SignOutDialogTarget {
  libraryId: string;
  libraryName: string;
}

export const connectedLibraries = writable<LibraryAuthStatus[]>([]);
export const authDialog = writable<AuthDialogTarget | null>(null);
export const signOutDialog = writable<SignOutDialogTarget | null>(null);
export const authBusy = writable(false);
export const authError = writable("");
export const keyringError = writable("");

function errorText(err: unknown, fallback: string): string {
  if (err instanceof Error && err.message.trim() !== "") {
    return err.message;
  }
  if (typeof err === "string" && err.trim() !== "") {
    return err;
  }
  return fallback;
}

const KEYRING_UNAVAILABLE_FALLBACK =
  "System keyring is unavailable. Start a Secret Service daemon (GNOME Keyring or KWallet) and try again.";

export function statusForLibrary(
  list: LibraryAuthStatus[],
  libraryID: string,
): LibraryAuthStatus | undefined {
  return list.find((item) => item.libraryId === libraryID);
}

/** loadConnectedLibraries reads remote library auth and health states without hitting the network. */
export async function loadConnectedLibraries(): Promise<LibraryAuthStatus[]> {
  try {
    const list = (await PlayerService.ConnectedLibraries()) ?? [];
    connectedLibraries.set(list);
    return list;
  } catch {
    connectedLibraries.set([]);
    return [];
  }
}

/** checkKeyring verifies that the platform secret store can seal credentials. */
export async function checkKeyring(): Promise<boolean> {
  keyringError.set("");
  try {
    await PlayerService.CheckKeyring();
    return true;
  } catch (err) {
    keyringError.set(errorText(err, KEYRING_UNAVAILABLE_FALLBACK));
    return false;
  }
}

/** openReauthDialog opens the re-sign-in dialog for an existing remote library. */
export async function openReauthDialog(
  libraryID: string,
  libraryName: string,
  kind = "ytm",
): Promise<void> {
  authError.set("");
  keyringError.set("");
  authDialog.set({ libraryId: libraryID, libraryName, kind });
  await checkKeyring();
}

export function closeAuthDialog(): void {
  if (get(authBusy)) {
    return;
  }
  authDialog.set(null);
  authError.set("");
}

export function openSignOutDialog(libraryID: string, libraryName: string): void {
  authError.set("");
  signOutDialog.set({ libraryId: libraryID, libraryName });
}

export function closeSignOutDialog(): void {
  if (get(authBusy)) {
    return;
  }
  signOutDialog.set(null);
  authError.set("");
}

async function activateCreatedLibrary(createdId: string, createdName: string): Promise<LibraryInfo | null> {
  await refreshLibraries();
  await loadConnectedLibraries();
  await selectLibrary(createdId);
  const found = get(libraries).find((l) => l.id === createdId) ?? null;
  showToast({
    key: "ytm-auth",
    kind: "message",
    icon: CheckCircle,
    label: createdName ? `Connected ${createdName}` : "Connected YouTube Music library",
  });
  return found;
}

/** createYTMLibraryWithBrowser authenticates via the isolated login window and creates a library. */
export async function createYTMLibraryWithBrowser(
  name: string,
  description: string,
): Promise<LibraryInfo | null> {
  authBusy.set(true);
  authError.set("");
  if (!(await checkKeyring())) {
    authBusy.set(false);
    return null;
  }

  try {
    const created = await PlayerService.CreateYTMLibraryWithBrowser(name, description);
    return await activateCreatedLibrary(created.id, created.name);
  } catch (err) {
    authError.set(errorText(err, "YouTube Music sign-in did not complete"));
    return null;
  } finally {
    authBusy.set(false);
  }
}

/** createYTMLibraryWithCookie validates a pasted Cookie header and creates a YouTube Music library. */
export async function createYTMLibraryWithCookie(
  name: string,
  description: string,
  header: string,
): Promise<LibraryInfo | null> {
  const trimmed = header.trim();
  if (trimmed === "") {
    authError.set("Paste a full Cookie header from music.youtube.com first.");
    return null;
  }

  authBusy.set(true);
  authError.set("");
  if (!(await checkKeyring())) {
    authBusy.set(false);
    return null;
  }

  try {
    const created = await PlayerService.CreateYTMLibraryWithCookie(name, description, trimmed);
    return await activateCreatedLibrary(created.id, created.name);
  } catch (err) {
    authError.set(errorText(err, "Could not verify the pasted Cookie header"));
    return null;
  } finally {
    authBusy.set(false);
  }
}

/** reSignInWithWindow refreshes credentials for the target library using the login window. */
export async function reSignInWithWindow(): Promise<boolean> {
  const target = get(authDialog);
  if (!target) {
    return false;
  }

  authBusy.set(true);
  authError.set("");
  if (!(await checkKeyring())) {
    authBusy.set(false);
    return false;
  }

  try {
    await PlayerService.ReSignInLibrary(target.libraryId);
    await Promise.all([loadConnectedLibraries(), refreshLibraries()]);
    authDialog.set(null);
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: CheckCircle,
      label: `Reconnected ${target.libraryName}`,
    });
    return true;
  } catch (err) {
    authError.set(errorText(err, "YouTube Music sign-in did not complete"));
    return false;
  } finally {
    authBusy.set(false);
  }
}

/** reSignInWithCookieHeader refreshes credentials for the target library from a Cookie header. */
export async function reSignInWithCookieHeader(header: string): Promise<boolean> {
  const target = get(authDialog);
  if (!target) {
    return false;
  }
  const trimmed = header.trim();
  if (trimmed === "") {
    authError.set("Paste a full Cookie header from music.youtube.com first.");
    return false;
  }

  authBusy.set(true);
  authError.set("");
  if (!(await checkKeyring())) {
    authBusy.set(false);
    return false;
  }

  try {
    await PlayerService.ReSignInLibraryWithCookie(target.libraryId, trimmed);
    await Promise.all([loadConnectedLibraries(), refreshLibraries()]);
    authDialog.set(null);
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: CheckCircle,
      label: `Reconnected ${target.libraryName}`,
    });
    return true;
  } catch (err) {
    authError.set(errorText(err, "Could not verify the pasted Cookie header"));
    return false;
  } finally {
    authBusy.set(false);
  }
}

/** refreshLibraryAuth probes one connected library against YouTube Music. */
export async function refreshLibraryAuth(libraryID: string): Promise<LibraryAuthStatus | null> {
  authBusy.set(true);
  authError.set("");
  try {
    const status = await PlayerService.RefreshLibraryAuth(libraryID);
    await Promise.all([loadConnectedLibraries(), refreshLibraries()]);
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: status.expired || !status.signedIn ? WarningCircle : CheckCircle,
      label: status.expired
        ? `${status.libraryName} session expired`
        : status.signedIn
          ? `${status.libraryName} session is active`
          : `${status.libraryName} is signed out`,
    });
    return status;
  } catch (err) {
    const msg = errorText(err, "Could not verify library session");
    authError.set(msg);
    showToast({ key: "ytm-auth", kind: "message", icon: WarningCircle, label: msg });
    return null;
  } finally {
    authBusy.set(false);
  }
}

/** signOutLibrary signs out one remote library, optionally removing its local cache. */
export async function signOutLibrary(deleteLibrary = false): Promise<boolean> {
  const target = get(signOutDialog);
  if (!target) {
    return false;
  }

  authBusy.set(true);
  authError.set("");
  try {
    await PlayerService.SignOutLibrary(target.libraryId, deleteLibrary);
    signOutDialog.set(null);
    await Promise.all([loadConnectedLibraries(), refreshLibraries()]);
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: CheckCircle,
      label: deleteLibrary
        ? `Signed out and removed ${target.libraryName}`
        : `Signed out of ${target.libraryName}`,
    });
    return true;
  } catch (err) {
    const msg = errorText(err, "Could not sign out");
    authError.set(msg);
    showToast({ key: "ytm-auth", kind: "message", icon: WarningCircle, label: msg });
    return false;
  } finally {
    authBusy.set(false);
  }
}
