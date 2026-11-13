<script lang="ts">
  // TextField: a labelled single-line text input. It is a controlled input:
  // the caller owns the value and the change handler, so the panel's store
  // remains the one place a setting lives. It exists in the panel's vocabulary
  // now, so a tab that needs a path or a name later does not invent a fourth
  // input style. The label is optional like the picker's: a field inside a
  // SettingRow already has the row's label, so it passes null and names the
  // input through ariaLabel instead.
  let {
    value,
    label = null,
    ariaLabel,
    placeholder = "",
    disabled = false,
    onchange,
  }: {
    value: string;
    label?: string | null;
    ariaLabel?: string;
    placeholder?: string;
    disabled?: boolean;
    onchange: (value: string) => void;
  } = $props();
</script>

<label class="flex flex-col gap-1 text-xs text-muted">
  {#if label}
    <span class="truncate">{label}</span>
  {/if}
  <input
    class="rounded-[4px] border border-line bg-surface px-2.5 py-1.5 text-xs text-fg transition-colors hover:bg-hover disabled:text-disabled"
    type="text"
    {value}
    {placeholder}
    {disabled}
    autocomplete="off"
    spellcheck="false"
    aria-label={label ?? ariaLabel}
    oninput={(e) => onchange(e.currentTarget.value)}
  />
</label>
