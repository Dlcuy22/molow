<script lang="ts">
  import { onMount } from "svelte";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import WarningCircle from "phosphor-svelte/lib/WarningCircle";
  import {
    authBusy,
    authError,
    checkKeyring,
    createYTMLibraryWithBrowser,
    createYTMLibraryWithCookie,
    keyringError,
  } from "../auth";
  import type { LibraryInfo } from "../types";

  type LibraryKindOption = "local" | "ytm" | "navidrome";

  const LIBRARY_TYPES: { value: LibraryKindOption; label: string }[] = [
    { value: "local", label: "Local" },
    { value: "ytm", label: "YouTube Music" },
    { value: "navidrome", label: "Navidrome" },
  ];

  let {
    mode = "create",
    initialKind = "local",
    initialName = "",
    initialDescription = "",
    initialServerUrl = "",
    initialUsername = "",
    busy = false,
    error = "",
    ontest,
    onsubmitnavidrome,
    onsubmit,
    oncreated,
    oncancel,
  }: {
    mode?: "create" | "edit";
    initialKind?: LibraryKindOption;
    initialName?: string;
    initialDescription?: string;
    initialServerUrl?: string;
    initialUsername?: string;
    busy?: boolean;
    error?: string;
    ontest?: (serverUrl: string, username: string, password: string) => Promise<string>;
    onsubmitnavidrome?: (payload: {
      name: string;
      description: string;
      serverUrl: string;
      username: string;
      password: string;
    }) => void;
    onsubmit: (name: string, description: string) => void;
    oncreated?: (info: LibraryInfo) => void;
    oncancel: () => void;
  } = $props();

  let kind = $state<LibraryKindOption>("local");
  let name = $state("");
  let description = $state("");
  let cookieInput = $state("");
  let serverUrl = $state("");
  let username = $state("");
  let password = $state("");
  let testing = $state(false);
  let testMessage = $state("");
  let testFailed = $state(false);
  let nameField = $state<HTMLInputElement | null>(null);

  onMount(() => {
    kind = initialKind;
    name = initialName;
    description = initialDescription;
    serverUrl = initialServerUrl;
    username = initialUsername;
    password = "";
    authError.set("");
    keyringError.set("");
    if (kind === "ytm" || kind === "navidrome") {
      void checkKeyring();
    }
    if (nameField) {
      nameField.focus();
      if (mode === "edit") {
        nameField.select();
      }
    }
  });

  function selectKind(next: LibraryKindOption) {
    if (effectiveBusy || kind === next) {
      return;
    }
    kind = next;
    authError.set("");
    testMessage = "";
    testFailed = false;
    if (next === "ytm" || next === "navidrome") {
      void checkKeyring();
    }
  }

  const effectiveBusy = $derived(busy || testing || (mode === "create" && kind === "ytm" && $authBusy));
  const blockedByKeyring = $derived(
    (kind === "navidrome" || (mode === "create" && kind === "ytm")) && $keyringError !== "",
  );
  const displayError = $derived(
    mode === "create" && kind === "ytm" ? $authError || error : error,
  );

  const title = $derived(mode === "edit" ? "Edit library" : "Create a library");
  const subtitle = $derived(
    mode === "edit"
      ? kind === "navidrome"
        ? "Update this Navidrome library's metadata or server connection."
        : "Update this library's name or description."
      : kind === "ytm"
        ? "Connect a YouTube Music account to mirror Liked Music and playlists."
        : kind === "navidrome"
          ? "Connect a remote Navidrome / Subsonic server to stream and sync."
          : "Store and organize local albums and watched folders.",
  );
  const submitLabel = $derived(
    mode === "edit"
      ? busy
        ? "Saving..."
        : "Save"
      : kind === "ytm"
        ? $authBusy
          ? "Waiting for sign-in..."
          : "Sign in and create"
        : kind === "navidrome"
          ? busy
            ? "Connecting..."
            : "Connect & sync"
          : busy
            ? "Creating..."
            : "Create library",
  );

  const trimmedName = $derived(name.trim());
  const trimmedDesc = $derived(description.trim());
  const trimmedUrl = $derived(serverUrl.trim());
  const trimmedUser = $derived(username.trim());
  const unchanged = $derived(
    mode === "edit" &&
      trimmedName === initialName.trim() &&
      trimmedDesc === initialDescription.trim() &&
      (kind !== "navidrome" ||
        (trimmedUrl === initialServerUrl.trim() &&
          trimmedUser === initialUsername.trim() &&
          password === "")),
  );
  const canSubmit = $derived(
    mode === "create" && kind === "ytm"
      ? !effectiveBusy && !blockedByKeyring
      : kind === "navidrome"
        ? !effectiveBusy &&
          !blockedByKeyring &&
          trimmedName !== "" &&
          trimmedUrl !== "" &&
          trimmedUser !== "" &&
          (mode === "edit" || password !== "") &&
          !unchanged
        : !effectiveBusy && trimmedName !== "" && !unchanged,
  );

  function submitLocal() {
    if ((mode === "edit" || kind === "local") && canSubmit) {
      onsubmit(trimmedName, trimmedDesc);
    }
  }

  function submitNavidrome() {
    if (kind === "navidrome" && canSubmit && onsubmitnavidrome) {
      onsubmitnavidrome({
        name: trimmedName,
        description: trimmedDesc,
        serverUrl: trimmedUrl,
        username: trimmedUser,
        password,
      });
    }
  }

  async function runConnectionTest() {
    if (!ontest || effectiveBusy || trimmedUrl === "" || trimmedUser === "" || password === "") {
      return;
    }
    testing = true;
    testMessage = "";
    testFailed = false;
    try {
      const err = await ontest(trimmedUrl, trimmedUser, password);
      if (err === "") {
        testMessage = "Connected successfully.";
        testFailed = false;
      } else {
        testMessage = err;
        testFailed = true;
      }
    } finally {
      testing = false;
    }
  }

  async function submitYTMBrowser() {
    if (effectiveBusy || blockedByKeyring) {
      return;
    }
    const created = await createYTMLibraryWithBrowser(trimmedName, trimmedDesc);
    if (created) {
      if (oncreated) {
        oncreated(created);
      } else {
        oncancel();
      }
    }
  }

  async function submitYTMCookie() {
    if (effectiveBusy || blockedByKeyring || cookieInput.trim() === "") {
      return;
    }
    const created = await createYTMLibraryWithCookie(trimmedName, trimmedDesc, cookieInput);
    if (created) {
      if (oncreated) {
        oncreated(created);
      } else {
        oncancel();
      }
    }
  }

  function submitPrimary() {
    if (!canSubmit) {
      return;
    }
    if (mode === "create" && kind === "ytm") {
      void submitYTMBrowser();
      return;
    }
    if (kind === "navidrome") {
      submitNavidrome();
      return;
    }
    submitLocal();
  }

  function handleKeydown(e: KeyboardEvent) {
    e.stopPropagation();
    if (e.key === "Escape" && !effectiveBusy) {
      e.preventDefault();
      oncancel();
    } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      submitPrimary();
    }
  }

  function handleNameKeydown(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      submitPrimary();
    }
  }

  function handleBackdropPointerDown(e: PointerEvent) {
    if (e.target === e.currentTarget && !effectiveBusy) {
      oncancel();
    }
  }
</script>

<div
  class="motion-scrim fixed inset-0 z-50 grid place-items-center bg-black/50 p-4"
  role="presentation"
  onpointerdown={handleBackdropPointerDown}
>
  <div
    class="motion-dialog w-[min(440px,calc(100vw-32px))] rounded-lg border border-line bg-surface p-5 shadow-menu"
    role="dialog"
    aria-modal="true"
    aria-labelledby="library-dialog-title"
    aria-describedby="library-dialog-subtitle"
    tabindex="-1"
    onkeydown={handleKeydown}
  >
    <h2 id="library-dialog-title" class="font-display text-[20px] font-bold leading-tight text-fg">
      {title}
    </h2>
    <p id="library-dialog-subtitle" class="mt-2 text-[12px] leading-relaxed text-muted">
      {subtitle}
    </p>

    <form
      class="mt-4 flex flex-col gap-3.5"
      onsubmit={(e) => {
        e.preventDefault();
        submitPrimary();
      }}
    >
      <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
        <span>
          Name
          {#if mode === "create" && kind === "ytm"}
            <span class="font-normal text-muted">(optional)</span>
          {/if}
        </span>
        <input
          bind:this={nameField}
          bind:value={name}
          onkeydown={handleNameKeydown}
          class="h-8 rounded-[6px] border border-line bg-bg px-2.5 text-[12px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus:border-muted focus:outline-none"
          type="text"
          placeholder={mode === "create" && kind === "ytm"
            ? "Uses YouTube account name if blank"
            : "Library name"}
          autocomplete="off"
          spellcheck="false"
          maxlength="80"
          required={mode === "edit" || kind === "local"}
          disabled={effectiveBusy}
        />
      </label>

      <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
        <span>
          Description <span class="font-normal text-muted">(optional)</span>
        </span>
        <textarea
          bind:value={description}
          rows="2"
          class="resize-none rounded-[6px] border border-line bg-bg px-2.5 py-2 text-[12px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus:border-muted focus:outline-none"
          placeholder="What you keep in this library"
          maxlength="240"
          disabled={effectiveBusy}
        ></textarea>
      </label>

      {#if mode === "create"}
        <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
          <span>Type</span>
          <span class="relative">
            <select
              value={kind}
              disabled={effectiveBusy}
              class="h-8 w-full appearance-none rounded-[6px] border border-line bg-bg pl-2.5 pr-8 text-[12px] font-normal text-fg transition-colors hover:border-muted focus:border-muted focus:outline-none disabled:opacity-40"
              onchange={(e) => selectKind(e.currentTarget.value as LibraryKindOption)}
            >
              {#each LIBRARY_TYPES as option (option.value)}
                <option value={option.value} class="bg-surface text-fg">
                  {option.label}
                </option>
              {/each}
            </select>
            <CaretDown
              size="12"
              class="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 text-muted"
              aria-hidden="true"
            />
          </span>
        </label>
      {/if}

      {#if kind === "navidrome"}
        <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
          <span>Server URL</span>
          <input
            bind:value={serverUrl}
            class="h-8 rounded-[6px] border border-line bg-bg px-2.5 text-[12px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus:border-muted focus:outline-none"
            type="url"
            placeholder="https://music.example.com"
            autocomplete="off"
            spellcheck="false"
            required
            disabled={effectiveBusy}
          />
        </label>

        <div class="grid grid-cols-2 gap-2.5">
          <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
            <span>Username</span>
            <input
              bind:value={username}
              class="h-8 rounded-[6px] border border-line bg-bg px-2.5 text-[12px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus:border-muted focus:outline-none"
              type="text"
              placeholder="username"
              autocomplete="off"
              spellcheck="false"
              required
              disabled={effectiveBusy}
            />
          </label>
          <label class="flex flex-col gap-1.5 text-[12px] font-medium text-fg">
            <span>
              Password
              {#if mode === "edit"}
                <span class="font-normal text-muted">(optional)</span>
              {/if}
            </span>
            <input
              bind:value={password}
              class="h-8 rounded-[6px] border border-line bg-bg px-2.5 text-[12px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus:border-muted focus:outline-none"
              type="password"
              placeholder={mode === "edit" ? "Keep current" : "••••••••"}
              autocomplete="off"
              required={mode === "create"}
              disabled={effectiveBusy}
            />
          </label>
        </div>

        {#if ontest}
          <div class="flex items-center justify-between gap-2">
            <button
              type="button"
              class="h-7 rounded-[6px] border border-line bg-bg px-2.5 text-[11px] text-fg transition-colors hover:bg-hover disabled:opacity-40"
              disabled={effectiveBusy || trimmedUrl === "" || trimmedUser === "" || password === ""}
              onclick={() => void runConnectionTest()}
            >
              {testing ? "Testing..." : "Test connection"}
            </button>
            {#if testMessage !== ""}
              <span class="truncate text-[11px] {testFailed ? 'text-danger' : 'text-muted'}">
                {testMessage}
              </span>
            {/if}
          </div>
        {/if}
      {/if}

      {#if blockedByKeyring}
        <div
          class="flex items-start gap-2 rounded-[6px] border border-danger bg-bg px-3 py-2.5 text-[12px] text-fg"
          role="alert"
        >
          <WarningCircle size="16" class="mt-0.5 shrink-0 text-danger" aria-hidden="true" />
          <div class="min-w-0">
            <p class="font-medium">System keyring unavailable</p>
            <p class="mt-0.5 leading-relaxed text-muted">{$keyringError}</p>
          </div>
        </div>
      {:else if displayError !== ""}
        <div
          class="flex items-start gap-2 rounded-[6px] border border-danger bg-bg px-2.5 py-2 text-[12px] text-fg"
          role="alert"
        >
          <WarningCircle size="16" class="mt-0.5 shrink-0 text-danger" aria-hidden="true" />
          <span>{displayError}</span>
        </div>
      {/if}

      {#if mode === "create" && kind === "ytm"}
        <details class="rounded-[6px] border border-line/70 bg-bg/60">
          <summary
            class="cursor-pointer select-none px-3 py-2 text-[12px] text-muted transition-colors hover:text-fg focus:text-fg focus:outline-none"
          >
            Having trouble? Paste cookie header
          </summary>
          <div class="flex flex-col gap-2.5 border-t border-line/70 p-3">
            <textarea
              bind:value={cookieInput}
              rows="3"
              class="resize-none rounded-[6px] border border-line bg-surface px-2.5 py-2 text-[11px] font-normal text-fg placeholder:text-muted/70 transition-colors hover:border-muted focus:border-muted focus:outline-none"
              placeholder="SAPISID=...; SID=...; __Secure-3PSID=..."
              spellcheck="false"
              disabled={effectiveBusy || blockedByKeyring}
            ></textarea>
            <div class="flex justify-end">
              <button
                type="button"
                class="h-8 rounded-[6px] border border-line bg-surface px-3 text-[12px] text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
                disabled={effectiveBusy || blockedByKeyring || cookieInput.trim() === ""}
                onclick={() => void submitYTMCookie()}
              >
                {$authBusy ? "Verifying..." : "Create with cookie"}
              </button>
            </div>
          </div>
        </details>
      {/if}

      <div class="mt-1 flex justify-end gap-2">
        <button
          type="button"
          class="h-8 rounded-[6px] px-3 text-[12px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={effectiveBusy}
          onclick={oncancel}
        >
          Cancel
        </button>
        <button
          type="submit"
          class="h-8 rounded-[6px] bg-fg px-3 text-[12px] font-medium text-bg-dark transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
          disabled={!canSubmit}
        >
          {submitLabel}
        </button>
      </div>
    </form>
  </div>
</div>
