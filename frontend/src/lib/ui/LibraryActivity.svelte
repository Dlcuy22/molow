<script lang="ts">
  import { cubicIn, cubicOut } from "svelte/easing";
  import CheckCircle from "phosphor-svelte/lib/CheckCircle";
  import WarningCircle from "phosphor-svelte/lib/WarningCircle";
  import X from "phosphor-svelte/lib/X";
  import {
    ACTIVITY_COMPACT_LIMIT,
    ACTIVITY_MAX_ITEMS,
    activityStore,
    clearFinishedActivities,
    dismissActivity,
    formatCompletionMessage,
    spinnerStepMs,
  } from "../activity";
  import LoadingSpinner from "./LoadingSpinner.svelte";

  let { libraryId }: { libraryId: string } = $props();

  let hovered = $state(false);
  let pinned = $state(false);
  let expanded = $state(false);
  let lingerVisible = $state(false);

  let closeTimer: ReturnType<typeof setTimeout> | undefined;
  let lingerTimer: ReturnType<typeof setTimeout> | undefined;
  let prevRunning = 0;
  let lastLibraryId = "";

  const open = $derived(hovered || pinned);
  const libState = $derived($activityStore[libraryId] ?? { items: [], batchPeak: 0 });
  const items = $derived(libState.items);
  const runningItems = $derived(items.filter((item) => item.state === "running"));
  const runningCount = $derived(runningItems.length);
  const errorCount = $derived(items.filter((item) => item.state === "error").length);
  const finishedCount = $derived(items.length - runningCount);
  const hasProblems = $derived(runningCount === 0 && errorCount > 0);
  const stepMs = $derived(spinnerStepMs(runningCount, libState.batchPeak));

  const visible = $derived(runningCount > 0 || errorCount > 0 || lingerVisible || open);
  const prefersReducedMotion =
    typeof window !== "undefined" &&
    Boolean(window.matchMedia?.("(prefers-reduced-motion: reduce)").matches);

  function activitySlide(
    node: Element,
    { duration = 200, easing = cubicOut }: { duration?: number; easing?: (t: number) => number } = {},
  ) {
    if (prefersReducedMotion || duration === 0) {
      return { duration: 0 };
    }
    const el = node as HTMLElement;
    const style = getComputedStyle(el);
    const width = parseFloat(style.width) || el.offsetWidth;
    const paddingRight = parseFloat(style.paddingRight) || 0;
    return {
      duration,
      easing,
      css: (t: number) =>
        `width: ${(t * width).toFixed(2)}px; padding-right: ${(t * paddingRight).toFixed(2)}px; opacity: ${t}; overflow: hidden;`,
    };
  }
  const visibleItems = $derived(
    items.slice(0, expanded ? ACTIVITY_MAX_ITEMS : ACTIVITY_COMPACT_LIMIT),
  );
  const hiddenCount = $derived(Math.max(0, items.length - ACTIVITY_COMPACT_LIMIT));

  const singleRunning = $derived(runningCount === 1 ? runningItems[0] : null);
  const latestDone = $derived(items.find((item) => item.state === "done") ?? null);
  const triggerLabel = $derived.by(() => {
    if (runningCount === 1 && singleRunning) {
      return singleRunning.title;
    }
    if (runningCount > 1) {
      return runningItems.every((i) => i.kind === "sync" || i.kind === "hydrate")
        ? `Syncing (${runningCount})`
        : `Processing (${runningCount})`;
    }
    if (errorCount > 0) {
      return errorCount === 1 ? "You got a problem" : `You have ${errorCount} problems`;
    }
    return latestDone ? formatCompletionMessage(latestDone) : "Done";
  });

  function splitRetryText(
    detail: string,
    retryLabel: string,
  ): { before: string; match: string; after: string } | null {
    const label = retryLabel.trim() || "again";
    let idx = detail.indexOf(label);
    if (idx === -1) {
      idx = detail.toLowerCase().indexOf(label.toLowerCase());
    }
    if (idx === -1) {
      return null;
    }
    return {
      before: detail.slice(0, idx),
      match: detail.slice(idx, idx + label.length),
      after: detail.slice(idx + label.length),
    };
  }

  $effect(() => {
    if (libraryId !== lastLibraryId) {
      lastLibraryId = libraryId;
      clearTimeout(closeTimer);
      clearTimeout(lingerTimer);
      hovered = false;
      pinned = false;
      expanded = false;
      lingerVisible = false;
      prevRunning = runningCount;
    }
  });

  $effect(() => {
    if (runningCount > 0) {
      clearTimeout(lingerTimer);
      lingerVisible = true;
      prevRunning = runningCount;
      return;
    }
    if (prevRunning > 0) {
      prevRunning = 0;
      clearTimeout(lingerTimer);
      if (errorCount > 0) {
        lingerVisible = false;
        return;
      }
      lingerVisible = true;
      lingerTimer = setTimeout(() => {
        lingerVisible = false;
      }, 2000);
    }
  });

  $effect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as HTMLElement | null;
      if (!target?.closest("[data-library-activity]")) {
        pinned = false;
        hovered = false;
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        pinned = false;
        hovered = false;
      }
    };
    window.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown);
    };
  });

  function handleMouseEnter() {
    clearTimeout(closeTimer);
    hovered = true;
  }

  function handleMouseLeave() {
    clearTimeout(closeTimer);
    if (pinned) {
      return;
    }
    closeTimer = setTimeout(() => {
      hovered = false;
    }, 150);
  }

  function togglePin() {
    clearTimeout(closeTimer);
    if (pinned) {
      pinned = false;
      hovered = false;
    } else {
      pinned = true;
    }
  }
</script>

{#if visible}
  <div
    class="relative pr-2"
    role="group"
    data-library-activity
    in:activitySlide={{ duration: 200, easing: cubicOut }}
    out:activitySlide={{ duration: 180, easing: cubicIn }}
    onmouseenter={handleMouseEnter}
    onmouseleave={handleMouseLeave}
  >
    <button
      type="button"
      class="flex h-8 max-w-[300px] items-center gap-2 rounded-md border px-2.5 text-[12px] font-medium transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus {hasProblems
        ? 'border-danger/80 bg-surface text-[#f27468]'
        : 'border-line/60 bg-surface text-fg'}"
      aria-haspopup="dialog"
      aria-expanded={open}
      onclick={togglePin}
    >
      {#if runningCount > 0}
        <LoadingSpinner size={14} inline {stepMs} />
        <span class="truncate">{triggerLabel}</span>
        {#if singleRunning && singleRunning.total && singleRunning.total > 0}
          <span class="shrink-0 tabular-nums text-[11px] text-muted">
            {singleRunning.done ?? 0}/{singleRunning.total}
          </span>
        {/if}
      {:else if errorCount > 0}
        <WarningCircle size={14} weight="fill" class="shrink-0 text-[#f27468]" />
        <span class="truncate">{triggerLabel}</span>
      {:else}
        <CheckCircle size={14} weight="fill" class="shrink-0 text-muted" />
        <span class="truncate" title={triggerLabel}>{triggerLabel}</span>
      {/if}
    </button>

    {#if open}
      <div
        class="motion-popover absolute right-2 top-10 z-40 origin-top-right rounded-lg border border-line/50 bg-surface shadow-menu {expanded
          ? 'w-[380px]'
          : 'w-[320px]'}"
        role="dialog"
        aria-label="Library activity"
      >
        <div class="flex items-center justify-between border-b border-line/60 px-3 py-2">
          <span class="text-[12px] font-medium text-fg">Activity</span>
          <span class="tabular-nums text-[11px] {hasProblems ? 'text-[#f27468]' : 'text-muted'}">
            {#if runningCount > 0}
              {runningCount} running
            {:else if errorCount > 0}
              {errorCount === 1 ? "You got a problem" : `You have ${errorCount} problems`}
            {:else}
              {items.length} recent
            {/if}
          </span>
        </div>

        <ul class="{expanded ? 'max-h-[380px] overflow-y-auto scroll-thin' : ''} divide-y divide-line/30">
          {#each visibleItems as item (item.id)}
            {@const total = item.total ?? 0}
            {@const done = item.done ?? 0}
            {@const ratio = total > 0 ? Math.max(0, Math.min(1, done / total)) : 0}
            {@const doneMessage = item.state === "done" ? formatCompletionMessage(item) : ""}
            {@const retryParts =
              item.state === "error" && item.onRetry && item.detail
                ? splitRetryText(item.detail, item.retryLabel ?? "again")
                : null}
            <li class="flex items-start gap-2.5 px-3 py-2">
              <span class="mt-0.5 flex size-4 shrink-0 items-center justify-center">
                {#if item.state === "running"}
                  <LoadingSpinner size={12} inline {stepMs} class="text-fg/80" />
                {:else if item.state === "error"}
                  <WarningCircle size={14} weight="fill" class="text-[#f27468]" />
                {:else}
                  <CheckCircle size={14} weight="fill" class="text-muted" />
                {/if}
              </span>

              <div class="min-w-0 flex-1">
                <p class="truncate font-content text-[12px] font-medium leading-snug text-fg" title={item.title}>
                  {item.title}
                </p>
                {#if item.state === "done"}
                  <p class="mt-0.5 truncate text-[11px] leading-snug text-muted" title={doneMessage}>
                    {doneMessage}
                  </p>
                {:else if item.detail}
                  <p
                    class="mt-0.5 leading-snug {item.state === 'error'
                      ? 'whitespace-normal break-words text-[11px] text-[#f27468]'
                      : 'truncate text-[11px] text-muted'}"
                    title={item.rawError || item.detail}
                  >
                    {#if retryParts}
                      {retryParts.before}<button
                        type="button"
                        class="cursor-pointer rounded-xs font-semibold text-fg underline decoration-[#f27468] underline-offset-2 transition-colors hover:text-[#f27468] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                        onclick={() => item.onRetry?.()}
                      >{retryParts.match}</button>{retryParts.after}
                    {:else if item.state === "error" && item.onRetry}
                      {item.detail}{" "}
                      <button
                        type="button"
                        class="cursor-pointer rounded-xs font-semibold text-fg underline decoration-[#f27468] underline-offset-2 transition-colors hover:text-[#f27468] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                        onclick={() => item.onRetry?.()}
                      >
                        Try again
                      </button>
                    {:else}
                      {item.detail}
                    {/if}
                  </p>
                {/if}
                {#if item.state === "running" && total > 0}
                  <div class="mt-1.5 h-[2px] w-full overflow-hidden rounded-full bg-track">
                    <div
                      class="h-full w-full origin-left bg-fg transition-transform duration-150"
                      style="transform: scaleX({ratio})"
                    ></div>
                  </div>
                {/if}
              </div>

              <div class="flex shrink-0 items-center gap-1.5">
                {#if total > 0}
                  <span class="tabular-nums text-[11px] {item.state === 'error' ? 'text-[#f27468]' : 'text-muted'}">
                    {done}/{total}
                  </span>
                {/if}
                {#if item.state === "error"}
                  <button
                    type="button"
                    class="grid size-6 shrink-0 place-items-center rounded text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                    aria-label={`Dismiss ${item.title}`}
                    title="Dismiss"
                    onclick={() => {
                      const willBeEmpty = items.length <= 1 && runningCount === 0;
                      dismissActivity(libraryId, item.id);
                      if (willBeEmpty) {
                        pinned = false;
                        hovered = false;
                        lingerVisible = false;
                      }
                    }}
                  >
                    <X size={13} />
                  </button>
                {/if}
              </div>
            </li>
          {/each}
        </ul>

        {#if hiddenCount > 0 || finishedCount > 0}
          <div class="flex items-center justify-between border-t border-line/60 px-2 py-1.5">
            {#if hiddenCount > 0}
              <button
                type="button"
                class="rounded px-2 py-1 text-[11px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                onclick={() => (expanded = !expanded)}
              >
                {expanded ? "Show less" : `Show more (${hiddenCount})`}
              </button>
            {:else}
              <span></span>
            {/if}

            {#if finishedCount > 0}
              <button
                type="button"
                class="rounded px-2 py-1 text-[11px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                onclick={() => {
                  clearFinishedActivities(libraryId);
                  if (runningCount === 0) {
                    pinned = false;
                    hovered = false;
                    lingerVisible = false;
                  }
                }}
              >
                Clear
              </button>
            {/if}
          </div>
        {/if}
      </div>
    {/if}
  </div>
{/if}
