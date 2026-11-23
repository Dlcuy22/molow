<script lang="ts">
  let {
    onplay = () => {},
    class: cls = "",
  }: {
    onplay?: () => void;
    class?: string;
  } = $props();
</script>

<span
  class="absolute bottom-2 right-2 grid size-11 place-items-center rounded-full bg-bg-dark/50 text-fg opacity-0 shadow-lift transition-[opacity,scale,background-color] duration-200 ease-out group-hover:opacity-100 hover:scale-110 hover:bg-bg-dark {cls}"
  role="presentation"
  onclick={(e) => {
    e.stopPropagation();
    onplay();
  }}
>
  <span class="ml-[3px] size-0 border-y-[7px] border-l-[12px] border-y-transparent border-l-fg"></span>
</span>
