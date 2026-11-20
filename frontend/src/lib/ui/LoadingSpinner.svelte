<script lang="ts">
  let {
    label = "",
    size = 24,
    class: cls = "",
  }: {
    label?: string;
    size?: number;
    class?: string;
  } = $props();

  // Prefix SMIL animation IDs with alphanumeric chars only (hyphens break SMIL begin= syntax).
  const rawId = $props.id();
  const uid = `sp${rawId.replace(/[^a-zA-Z0-9]/g, "")}`;
  const a1 = `${uid}_a1`;
  const a2 = `${uid}_a2`;
  const a3 = `${uid}_a3`;
  const a4 = `${uid}_a4`;
  const b1 = `${uid}_b1`;
  const b2 = `${uid}_b2`;
  const b3 = `${uid}_b3`;
  const b4 = `${uid}_b4`;
</script>

<div class="flex flex-col items-center gap-3 text-fg/75 {cls}" role="status" aria-live="polite">
  <svg
    xmlns="http://www.w3.org/2000/svg"
    width={size}
    height={size}
    viewBox="0 0 24 24"
    aria-hidden="true"
  >
    <rect width="10" height="10" x="1" y="1" fill="currentColor" rx="1">
      <animate id={a1} fill="freeze" attributeName="x" begin="0;{b4}.end" dur="0.2s" values="1;13" />
      <animate id={a2} fill="freeze" attributeName="y" begin="{b1}.end" dur="0.2s" values="1;13" />
      <animate id={a3} fill="freeze" attributeName="x" begin="{b2}.end" dur="0.2s" values="13;1" />
      <animate id={a4} fill="freeze" attributeName="y" begin="{b3}.end" dur="0.2s" values="13;1" />
    </rect>
    <rect width="10" height="10" x="1" y="13" fill="currentColor" rx="1">
      <animate id={b1} fill="freeze" attributeName="y" begin="{a1}.end" dur="0.2s" values="13;1" />
      <animate id={b2} fill="freeze" attributeName="x" begin="{a2}.end" dur="0.2s" values="1;13" />
      <animate id={b3} fill="freeze" attributeName="y" begin="{a3}.end" dur="0.2s" values="1;13" />
      <animate id={b4} fill="freeze" attributeName="x" begin="{a4}.end" dur="0.2s" values="13;1" />
    </rect>
  </svg>
  {#if label}
    <span class="text-[12px] text-fg/70">{label}</span>
  {/if}
</div>
