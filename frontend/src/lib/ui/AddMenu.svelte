<script lang="ts">
  import CloudArrowDown from "phosphor-svelte/lib/CloudArrowDown";
  import FolderSimplePlus from "phosphor-svelte/lib/FolderSimplePlus";

  let {
    top = 85,
    right = 36,
    onimport = () => {},
    onytm = () => {},
    onclose = () => {},
  }: {
    top?: number;
    right?: number;
    onimport?: () => void;
    onytm?: () => void;
    onclose?: () => void;
  } = $props();

  const rows = [
    { label: "Import from computer", icon: FolderSimplePlus, kind: "import" as const },
    { label: "Import from YTM", icon: CloudArrowDown, kind: "ytm" as const },
  ];
</script>

<div
  class="fixed z-40 w-[240px] rounded-lg bg-surface p-1.5 shadow-menu"
  style="top:{top}px; right:{right}px"
  role="menu"
  tabindex="-1"
  data-add
  onpointerdown={(e) => e.stopPropagation()}
>
  {#each rows as row (row.label)}
    <button
      class="flex h-[34px] w-full items-center gap-2.5 rounded-md px-2.5 text-left text-[12px] text-fg transition-colors hover:bg-hover"
      role="menuitem"
      onclick={() => {
        if (row.kind === "import") {
          onimport();
        } else {
          onytm();
        }
        onclose();
      }}
    >
      <row.icon size="16" class="text-muted" />
      {row.label}
    </button>
  {/each}
</div>
