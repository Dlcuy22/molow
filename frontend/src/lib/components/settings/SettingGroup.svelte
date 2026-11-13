<script lang="ts">
  import type { Snippet } from "svelte";

  // SettingGroup labels a block of related controls: the label and note sit
  // above the group, and the controls keep their own layout inside. Use it when
  // several knobs read as one setting, such as the visualizer's shape. The note
  // is optional for a group whose label already says what it holds.
  //
  // The label is set as a panel section label (uppercase, tracked) rather than
  // a row title, so a group heading never reads as one more setting above the
  // controls it names.
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

<div class="pt-2.5">
  <p class="text-[10px] font-medium uppercase tracking-[0.06em] text-muted">{label}</p>
  {#if description}
    <p class="mt-1 text-[11px] text-muted">{description}</p>
  {/if}
  <!-- Top padding only: the last row carries its own py-2.5, and the panel rule
       follows this box, so padding here would double the gap above that rule. -->
  <div class="mt-2 border-t border-line/50"></div>
  <div class={description ? "mt-3" : "-mt-0.5"}>
    {@render children()}
  </div>
</div>
