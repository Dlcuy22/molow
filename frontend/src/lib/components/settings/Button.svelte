<script lang="ts">
  // Button is the settings panel's one-shot action: a row control that fires
  // once, unlike a Picker or Switch that holds a value. It is a neutral
  // secondary button, because a screen's primary action is not a setting.
  //
  // A press that returns a promise keeps the button disabled until it settles,
  // so a double click cannot clear a cache or pick a folder twice.
  let {
    label,
    busyLabel = label,
    disabled = false,
    onclick,
  }: {
    label: string;
    busyLabel?: string;
    disabled?: boolean;
    onclick: () => void | Promise<unknown>;
  } = $props();

  let busy = $state(false);

  async function press() {
    if (busy || disabled) {
      return;
    }
    const action = onclick();
    if (!(action instanceof Promise)) {
      return;
    }
    busy = true;
    try {
      await action;
    } finally {
      busy = false;
    }
  }
</script>

<button
  class="rounded-[6px] border border-line bg-surface px-2.5 py-1.5 text-xs text-fg transition-colors hover:bg-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:cursor-default disabled:text-disabled disabled:hover:bg-surface"
  type="button"
  disabled={disabled || busy}
  aria-busy={busy}
  onclick={() => void press()}
>
  {busy ? busyLabel : label}
</button>
