import { get, writable } from "svelte/store";
import CheckCircle from "phosphor-svelte/lib/CheckCircle";
import WarningCircle from "phosphor-svelte/lib/WarningCircle";
import { PlayerService } from "../../bindings/github.com/dlcuy22/molow/backend";
import type { AccountInfo, AuthStatus } from "../../bindings/github.com/dlcuy22/molow/backend/models";
import { libraries, refreshLibraries, selectLibrary } from "./library";
import { showToast } from "./toast";

export const authStatus = writable<AuthStatus>({ signedIn: false });
export const authAccounts = writable<AccountInfo[]>([]);
export const authBusy = writable(false);
export const authError = writable("");
export const keyringError = writable("");
export const authDialogOpen = writable(false);
export const signOutDialogOpen = writable(false);

function errorText(err: unknown, fallback: string): string {
  if (err instanceof Error && err.message.trim() !== "") {
    return err.message;
  }
  if (typeof err === "string" && err.trim() !== "") {
    return err;
  }
  return fallback;
}

/** loadAuthStatus reads the stored session without touching the network. */
export async function loadAuthStatus(): Promise<AuthStatus> {
  try {
    const status = await PlayerService.AuthStatus();
    authStatus.set(status);
    if (status.signedIn && !status.expired) {
      void loadAccounts();
    } else {
      authAccounts.set([]);
    }
    return status;
  } catch {
    const empty: AuthStatus = { signedIn: false };
    authStatus.set(empty);
    authAccounts.set([]);
    return empty;
  }
}

/** loadAccounts fetches channels owned by the signed-in account. */
export async function loadAccounts(): Promise<AccountInfo[]> {
  try {
    const list = (await PlayerService.Accounts()) ?? [];
    authAccounts.set(list);
    return list;
  } catch {
    authAccounts.set([]);
    return [];
  }
}

/** refreshAuth validates the stored session against YouTube Music. */
export async function refreshAuth(): Promise<AuthStatus> {
  authBusy.set(true);
  authError.set("");
  try {
    const status = await PlayerService.RefreshAuth();
    authStatus.set(status);
    if (status.signedIn && !status.expired) {
      await loadAccounts();
    } else {
      authAccounts.set([]);
    }
    return status;
  } catch (err) {
    authError.set(errorText(err, "Could not verify YouTube Music session"));
    return get(authStatus);
  } finally {
    authBusy.set(false);
  }
}

/** openAuthDialog verifies the OS keyring before presenting sign-in controls. */
export async function openAuthDialog(): Promise<void> {
  authError.set("");
  keyringError.set("");
  authDialogOpen.set(true);
  try {
    await PlayerService.CheckKeyring();
  } catch (err) {
    keyringError.set(
      errorText(
        err,
        "System keyring is unavailable. Start a Secret Service daemon (GNOME Keyring or KWallet) and try again.",
      ),
    );
  }
}

export function closeAuthDialog(): void {
  if (get(authBusy)) {
    return;
  }
  authDialogOpen.set(false);
}

export function openSignOutDialog(): void {
  authError.set("");
  signOutDialogOpen.set(true);
}

export function closeSignOutDialog(): void {
  if (get(authBusy)) {
    return;
  }
  signOutDialogOpen.set(false);
}

async function selectActiveYTMLibrary(channelId?: string): Promise<void> {
  await refreshLibraries();
  const list = get(libraries);
  const match =
    (channelId ? list.find((l) => l.id === `ytm:${channelId}`) : undefined) ??
    list.find((l) => l.kind === "ytm");
  if (match) {
    await selectLibrary(match.id);
  }
}

/** signInWithWindow opens the isolated login window after keyring pre-flight. */
export async function signInWithWindow(): Promise<boolean> {
  authBusy.set(true);
  authError.set("");
  keyringError.set("");
  try {
    await PlayerService.CheckKeyring();
  } catch (err) {
    const msg = errorText(
      err,
      "System keyring is unavailable. Sign in was aborted to keep credentials safe.",
    );
    keyringError.set(msg);
    authBusy.set(false);
    return false;
  }

  try {
    await PlayerService.SignIn();
    const status = await loadAuthStatus();
    if (status.signedIn && !status.expired) {
      await loadAccounts();
      await selectActiveYTMLibrary(status.channelId);
    }
    authDialogOpen.set(false);
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: CheckCircle,
      label: status.name ? `Signed in as ${status.name}` : "Signed in to YouTube Music",
    });
    return true;
  } catch (err) {
    authError.set(errorText(err, "YouTube Music sign-in did not complete"));
    return false;
  } finally {
    authBusy.set(false);
  }
}

/** signInWithCookieHeader signs in from a pasted Cookie header after keyring pre-flight. */
export async function signInWithCookieHeader(header: string): Promise<boolean> {
  const trimmed = header.trim();
  if (trimmed === "") {
    authError.set("Paste a full Cookie header from music.youtube.com first.");
    return false;
  }

  authBusy.set(true);
  authError.set("");
  keyringError.set("");
  try {
    await PlayerService.CheckKeyring();
  } catch (err) {
    const msg = errorText(
      err,
      "System keyring is unavailable. Sign in was aborted to keep credentials safe.",
    );
    keyringError.set(msg);
    authBusy.set(false);
    return false;
  }

  try {
    await PlayerService.SignInWithCookieHeader(trimmed);
    const status = await loadAuthStatus();
    if (status.signedIn && !status.expired) {
      await loadAccounts();
      await selectActiveYTMLibrary(status.channelId);
    }
    authDialogOpen.set(false);
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: CheckCircle,
      label: status.name ? `Signed in as ${status.name}` : "Signed in to YouTube Music",
    });
    return true;
  } catch (err) {
    authError.set(errorText(err, "Could not verify the pasted Cookie header"));
    return false;
  } finally {
    authBusy.set(false);
  }
}

export async function switchChannel(channelId: string): Promise<void> {
  if (!channelId) {
    return;
  }
  authBusy.set(true);
  authError.set("");
  try {
    await PlayerService.SwitchChannel(channelId);
    const status = await loadAuthStatus();
    await loadAccounts();
    await selectActiveYTMLibrary(status.channelId);
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: CheckCircle,
      label: "Switched YouTube Music channel",
    });
  } catch (err) {
    const msg = errorText(err, "Could not switch channel");
    authError.set(msg);
    showToast({ key: "ytm-auth", kind: "message", icon: WarningCircle, label: msg });
  } finally {
    authBusy.set(false);
  }
}

export async function signOut(deleteLibrary = false): Promise<void> {
  authBusy.set(true);
  authError.set("");
  try {
    await PlayerService.SignOutWithOptions(deleteLibrary);
    authStatus.set({ signedIn: false });
    authAccounts.set([]);
    signOutDialogOpen.set(false);
    await refreshLibraries();
    showToast({
      key: "ytm-auth",
      kind: "message",
      icon: CheckCircle,
      label: deleteLibrary
        ? "Signed out and deleted YouTube Music library"
        : "Signed out of YouTube Music",
    });
  } catch (err) {
    const msg = errorText(err, "Could not sign out");
    authError.set(msg);
    showToast({ key: "ytm-auth", kind: "message", icon: WarningCircle, label: msg });
  } finally {
    authBusy.set(false);
  }
}
