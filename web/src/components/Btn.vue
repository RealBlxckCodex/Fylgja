<script setup lang="ts">
defineProps<{ variant?: 'primary' | 'ghost' | 'danger' | 'subtle'; size?: 'sm' | 'md'; loading?: boolean; disabled?: boolean; type?: 'button' | 'submit' }>()
</script>
<template>
  <button
    :type="type ?? 'button'"
    :disabled="disabled || loading"
    class="focus-ring inline-flex items-center justify-center gap-1.5 rounded-lg font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed whitespace-nowrap"
    :class="[
      size === 'sm' ? 'px-2.5 py-1 text-xs' : 'px-3.5 py-2 text-sm',
      variant === 'ghost' ? 'border line hover:bg-[var(--panel-2)]' :
      variant === 'danger' ? 'bg-[var(--err)]/15 text-[var(--err)] border border-[var(--err)]/40 hover:bg-[var(--err)]/25' :
      variant === 'subtle' ? 'hover:bg-[var(--panel-2)] muted hover:text-[var(--text)]' :
      'bg-accent text-white hover:bg-[var(--accent-2)]',
    ]"
  >
    <span v-if="loading" class="inline-block size-3 rounded-full border-2 border-current border-t-transparent animate-spin" aria-hidden="true" />
    <slot />
  </button>
</template>
