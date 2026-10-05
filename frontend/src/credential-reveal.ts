import { computed, ref } from 'vue'

// A viewed saved credential must never become an edited credential implicitly.
export function useCredentialReveal(load: (profileId: number) => Promise<string>) {
  const secret = ref('')
  const visible = ref(false)
  const loading = ref(false)
  const savedPreview = ref('')
  let requestVersion = 0
  const displayValue = computed(() => secret.value || (visible.value ? savedPreview.value : ''))

  function invalidate() {
    requestVersion++
    loading.value = false
    savedPreview.value = ''
  }
  function hide() { invalidate(); visible.value = false }
  function reset() { hide(); secret.value = '' }
  function update(value: string) { invalidate(); secret.value = value }

  async function toggle(profileId: number, hasSavedCredential: boolean) {
    if (visible.value) { hide(); return }
    if (loading.value) return
    if (secret.value || !hasSavedCredential) { visible.value = true; return }
    const request = ++requestVersion
    loading.value = true
    try {
      const saved = await load(profileId)
      if (request !== requestVersion) return
      savedPreview.value = saved
      visible.value = true
    } catch (error) {
      if (request === requestVersion) throw error
    } finally {
      if (request === requestVersion) loading.value = false
    }
  }
  return { secret, visible, loading, displayValue, toggle, update, hide, reset }
}
