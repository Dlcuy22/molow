<script lang="ts">
  import type { Snippet } from "svelte";

  // SettingRow is the shared shape of one setting: a label and its description
  // on the left, the control on the right. Every setting in the panel uses it,
  // so the label column lines up and a new setting is one row, not a new
  // layout. The control arrives as a snippet, so a row can hold a switch, a
  // slider or a picker without this file knowing which. Description is
  // optional: a label that explains itself leaves it out and the row keeps its
  // single line.
  let {
    label,
    description = null,
    control,
  }: {
    label: string;
    description?: string | null;
    control: Snippet;
  } = $props();
</script>

<div class="flex items-center justify-between gap-4 py-2.5">
  <div class="min-w-0">
    <p class="text-sm text-fg">{label}</p>
    {#if description}
      <p class="mt-0.5 text-[11px] text-muted">{description}</p>
    {/if}
  </div>
  <!-- Both columns center, so a row without a note is one line of label and
       control on a shared middle line instead of a label pinned to the top
       above the space the note would have filled. -->
  <div class="flex shrink-0 items-center">
    {@render control()}
  </div>
</div>
