<script setup lang="ts">
defineProps<{ variant?: 'primary' | 'ghost' | 'danger' | 'subtle'; size?: 'sm' | 'md'; loading?: boolean; disabled?: boolean; type?: 'button' | 'submit' }>()
</script>
<template>
  <button :type="type ?? 'button'" :disabled="disabled || loading"
    class="focus-ring inline-flex items-center justify-center gap-1.5 font-medium transition-all active:scale-[.97] disabled:opacity-50 disabled:cursor-not-allowed whitespace-nowrap"
    :class="[
      size === 'sm' ? 'px-3 py-1.5 text-xs rounded-lg' : 'px-4 py-2.5 text-sm rounded-xl',
      variant === 'ghost' ? 'border border-[var(--line-strong)] hover:bg-[var(--panel-2)] hover:border-[var(--muted)]' :
      variant === 'danger' ? 'bg-[var(--err)]/12 text-[var(--err)] border border-[var(--err)]/35 hover:bg-[var(--err)]/22' :
      variant === 'subtle' ? 'hover:bg-[var(--panel-2)] muted hover:text-[var(--text)]' :
      'bg-accent text-white hover:brightness-110',
    ]">
    <span v-if="loading" class="inline-block size-3 rounded-full border-2 border-current border-t-transparent animate-spin" aria-hidden="true" />
    <slot />
  </button>
</template>
