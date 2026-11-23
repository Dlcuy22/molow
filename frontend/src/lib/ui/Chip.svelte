<script lang="ts">
  let {
    label,
    selected = false,
    onclick = () => {},
  }: { label: string; selected?: boolean; onclick?: () => void } = $props();
</script>

<button
  class="h-7 cursor-pointer rounded-md px-3 text-[12px] font-medium leading-none transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
  class:bg-selected={selected}
  class:text-fg={selected}
  class:bg-surface={!selected}
  class:text-muted={!selected}
  class:hover:text-fg={!selected}
  aria-pressed={selected}
  {onclick}
>
  {label}
</button>
