<script lang="ts">
  import type { Snippet } from "svelte";

  // SettingGroup labels a block of related controls: the label and note sit
  // above the group, and the controls keep their own layout inside. Use it when
  // several knobs read as one setting, such as the visualizer's shape. The note
  // is optional for a group whose label already says what it holds.
  let {
    label,
    description = null,
    children,
  }: {
    label: string;
    description?: string | null;
    children: Snippet;
  } = $props();
</script>

<div class="py-2.5">
  <p class="text-sm text-fg">{label}</p>
  {#if description}
    <p class="mt-0.5 text-[11px] text-muted">{description}</p>
  {/if}
  <div class="mt-3">
    {@render children()}
  </div>
</div>
