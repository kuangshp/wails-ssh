import { ref } from 'vue'
import type { api, Profile } from '../../api'

type ProfileQueries = Pick<typeof api, 'ListProfiles' | 'ListGroups'>

// Own the library's read state independently of dialogs and terminal sessions.
// Only the latest refresh may publish either data, errors, or loading state.
export function useProfiles(backend: ProfileQueries, onError: (error: unknown) => void, enabled = () => true) {
  const profiles = ref<Profile[]>([])
  const groups = ref<string[]>([])
  const pageLoading = ref(false)
  let generation = 0
  let disposed = false

  async function refresh() {
    if (disposed || !enabled()) return
    const request = ++generation
    pageLoading.value = true
    try {
      const [nextProfiles, nextGroups] = await Promise.all([backend.ListProfiles(), backend.ListGroups()])
      if (disposed || request !== generation) return
      profiles.value = nextProfiles || []
      groups.value = nextGroups || []
    } catch (error) {
      if (!disposed && request === generation) onError(error)
    } finally {
      if (!disposed && request === generation) pageLoading.value = false
    }
  }

  function dispose() {
    disposed = true
    generation++
    pageLoading.value = false
    profiles.value = []
    groups.value = []
  }

  return { profiles, groups, pageLoading, refresh, dispose }
}
