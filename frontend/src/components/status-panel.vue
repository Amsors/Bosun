<script setup lang="ts">
defineProps<{ kind: 'loading' | 'empty' | 'error'; message: string }>()
</script>

<template>
  <section
    class="status-panel"
    :class="kind"
    :aria-live="kind === 'loading' ? 'polite' : 'assertive'"
  >
    <span v-if="kind === 'loading'" class="spinner" aria-hidden="true" />
    <p>{{ message }}</p>
    <slot />
  </section>
</template>
