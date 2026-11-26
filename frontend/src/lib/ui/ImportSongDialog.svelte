<script lang="ts">
  import { onMount } from "svelte";
  import Heart from "phosphor-svelte/lib/Heart";
  import MusicNotesPlus from "phosphor-svelte/lib/MusicNotesPlus";
  import Plus from "phosphor-svelte/lib/Plus";
  import type { YTMAddTarget } from "../../../bindings/github.com/dlcuy22/molow/backend/models";
  import { closeImportSongModal, importSongModal, importYTMSong, ytmAddTargets } from "../library";

  const modal = $derived($importSongModal);

  let targets = $state<YTMAddTarget[]>([]);
  let loading = $state(true);
  let busy = $state(false);
  let error = $state("");
  let newName = $state("");
  let duplicatePrompt = $state<{ playlistId: string; name: string } | null>(null);
  let likeBtn = $state<HTMLButtonElement | null>(null);

  onMount(() => {
    likeBtn?.focus();
    const cur = $importSongModal;
    if (!cur) return;
    void loadTargets(cur.libraryId, cur.hit.videoId);
  });

  async function loadTargets(libId: string, videoId: string) {
    loading = true;
    error = "";
    try {
      targets = await ytmAddTargets(libId, videoId);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      targets = [];
    } finally {
      loading = false;
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    e.stopPropagation();
    if (e.key === "Escape" && !busy) {
      e.preventDefault();
      if (duplicatePrompt) {
        duplicatePrompt = null;
      } else {
        closeImportSongModal();
      }
    }
  }

  function handleBackdropPointerDown(e: PointerEvent) {
    if (e.target === e.currentTarget && !busy) {
      closeImportSongModal();
    }
  }

  async function pickLiked() {
    if (!modal || busy) return;
    busy = true;
    error = "";
    try {
      await importYTMSong(modal.libraryId, modal.hit, { liked: true });
      closeImportSongModal();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function pickPlaylist(playlistId: string, name: string, allowDuplicate = false) {
    if (!modal || busy) return;
    busy = true;
    error = "";
    try {
      const res = await importYTMSong(modal.libraryId, modal.hit, {
        playlistId,
        allowDuplicate,
      });
      if (res.duplicate) {
        duplicatePrompt = { playlistId, name: res.name || name };
        return;
      }
      closeImportSongModal();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function createAndAdd(e: SubmitEvent) {
    e.preventDefault();
    const title = newName.trim();
    if (!modal || busy || title === "") return;
    busy = true;
    error = "";
    try {
      await importYTMSong(modal.libraryId, modal.hit, { newPlaylist: title });
      closeImportSongModal();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

{#if modal}
  <div
    class="motion-scrim fixed inset-0 z-[60] grid place-items-center bg-black/50 p-4"
    role="presentation"
    onpointerdown={handleBackdropPointerDown}
  >
    <div
      class="motion-dialog w-[min(420px,calc(100vw-32px))] rounded-lg border border-line bg-surface p-5 shadow-menu"
      role="dialog"
      aria-modal="true"
      aria-labelledby="import-song-title"
      tabindex="-1"
      onkeydown={handleKeydown}
    >
      <h2 id="import-song-title" class="text-sm font-medium text-fg">
        Save song to YouTube Music
      </h2>
      <p class="mt-1 truncate text-[12px] text-muted" title={modal.hit.title}>
        {modal.hit.title}{#if modal.hit.artist} — {modal.hit.artist}{/if}
      </p>

      {#if error !== ""}
        <p class="mt-3 rounded-[6px] border border-danger/40 bg-danger/10 px-3 py-2 text-[12px] text-fg">
          {error}
        </p>
      {/if}

      {#if duplicatePrompt}
        <div class="mt-4 rounded-[6px] border border-line bg-bg/60 p-3">
          <p class="text-[12px] leading-relaxed text-fg">
            Already in <span class="font-medium">{duplicatePrompt.name}</span>. Add again?
          </p>
          <div class="mt-3 flex justify-end gap-2">
            <button
              type="button"
              class="h-8 rounded-[6px] px-3 text-[12px] text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
              disabled={busy}
              onclick={() => (duplicatePrompt = null)}
            >
              Back
            </button>
            <button
              type="button"
              class="h-8 rounded-[6px] bg-fg px-3 text-[12px] font-medium text-bg-dark transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
              disabled={busy}
              onclick={() =>
                duplicatePrompt &&
                void pickPlaylist(duplicatePrompt.playlistId, duplicatePrompt.name, true)}
            >
              {busy ? "Adding..." : "Add again"}
            </button>
          </div>
        </div>
      {:else}
        <div class="mt-4 space-y-3">
          <button
            bind:this={likeBtn}
            type="button"
            class="flex w-full items-center gap-3 rounded-[6px] border border-line bg-bg/60 px-3 py-2.5 text-left transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
            disabled={busy}
            onclick={() => void pickLiked()}
          >
            <Heart size="18" class="shrink-0 text-fg" />
            <div class="min-w-0 flex-1">
              <div class="text-[13px] font-medium text-fg">Like it</div>
              <div class="text-[11px] text-muted">Add to Liked Music</div>
            </div>
          </button>

          <div>
            <div class="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium text-muted">
              <MusicNotesPlus size="14" />
              <span>Save to a playlist</span>
            </div>
            <div class="max-h-44 space-y-1 overflow-y-auto rounded-[6px] border border-line bg-bg/40 p-1.5">
              {#if loading}
                <p class="px-2 py-3 text-center text-[12px] text-muted">Loading playlists…</p>
              {:else if targets.length === 0}
                <p class="px-2 py-3 text-center text-[12px] text-muted">
                  No editable playlists yet — create one below.
                </p>
              {:else}
                {#each targets as tgt (tgt.playlistId || tgt.remoteId)}
                  <button
                    type="button"
                    class="flex w-full items-center justify-between gap-2 rounded-[5px] px-2.5 py-2 text-left transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
                    disabled={busy}
                    onclick={() => void pickPlaylist(tgt.playlistId, tgt.title)}
                  >
                    <div class="min-w-0">
                      <div class="truncate text-[12px] font-medium text-fg">{tgt.title}</div>
                      {#if tgt.subtitle}
                        <div class="truncate text-[11px] text-muted">{tgt.subtitle}</div>
                      {/if}
                    </div>
                    {#if tgt.hasVideo}
                      <span class="shrink-0 rounded bg-surface px-1.5 py-0.5 text-[10px] text-muted">
                        In playlist
                      </span>
                    {/if}
                  </button>
                {/each}
              {/if}
            </div>
          </div>

          <form class="flex gap-2" onsubmit={(e) => void createAndAdd(e)}>
            <input
              type="text"
              placeholder="New playlist name"
              bind:value={newName}
              disabled={busy}
              class="h-8 min-w-0 flex-1 rounded-[6px] border border-line bg-bg px-2.5 text-[12px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            />
            <button
              type="submit"
              class="inline-flex h-8 shrink-0 items-center gap-1 rounded-[6px] bg-fg px-3 text-[12px] font-medium text-bg-dark transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
              disabled={busy || newName.trim() === ""}
            >
              <Plus size="14" />
              <span>{busy ? "Creating..." : "New"}</span>
            </button>
          </form>
        </div>

        <div class="mt-4 flex justify-end">
          <button
            type="button"
            class="h-8 rounded-[6px] px-3 text-[12px] text-fg transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-40"
            disabled={busy}
            onclick={closeImportSongModal}
          >
            Cancel
          </button>
        </div>
      {/if}
    </div>
  </div>
{/if}
