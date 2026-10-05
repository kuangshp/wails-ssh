<script setup lang="ts">
withDefaults(defineProps<{
  modelValue: boolean
  title: string
  width?: number | string
  busy?: boolean
  dismissible?: boolean
}>(), { width: 480, busy: false, dismissible: true })
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; closed: [] }>()
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="title"
    :width="width"
    class="app-dialog"
    align-center
    append-to-body
    destroy-on-close
    :close-on-click-modal="false"
    :close-on-press-escape="dismissible && !busy"
    :show-close="dismissible && !busy"
    @closed="emit('closed')"
    @update:model-value="(value: boolean) => { if (!busy && dismissible) emit('update:modelValue', value) }"
  >
    <slot />
    <template v-if="$slots.footer" #footer><slot name="footer" /></template>
  </el-dialog>
</template>
