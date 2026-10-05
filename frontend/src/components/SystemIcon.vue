<script setup lang="ts">
import { computed } from 'vue'
import { Apple } from '@lucide/vue'

const props = defineProps<{ osId: string }>()
type SystemMark = { name: string; kind: 'ubuntu' | 'letter' | 'mountain' | 'windows' | 'macos' | 'linux'; color?: string; label?: string }
// Match the Rust client's system_badge / paint_* geometry and colours.
const systems: Record<string, SystemMark> = {
  ubuntu: { name: 'Ubuntu', kind: 'ubuntu' },
  debian: { name: 'Debian', kind: 'letter', color: '#d3205c', label: 'd' },
  centos: { name: 'CentOS', kind: 'letter', color: '#7c4399', label: '✦' },
  rhel: { name: 'Red Hat Enterprise Linux', kind: 'letter', color: '#cc0000', label: 'RH' },
  rocky: { name: 'Rocky Linux', kind: 'mountain', color: '#107b58' },
  almalinux: { name: 'AlmaLinux', kind: 'letter', color: '#108d99', label: 'A' },
  fedora: { name: 'Fedora', kind: 'letter', color: '#295b93', label: 'f' },
  alinux: { name: 'Alibaba Cloud Linux', kind: 'letter', color: '#ff6a00', label: 'A' },
  anolis: { name: 'Anolis OS', kind: 'letter', color: '#ff6a00', label: 'A' },
  opencloudos: { name: 'OpenCloudOS', kind: 'letter', color: '#007ee5', label: 'OC' },
  tencentos: { name: 'TencentOS Server', kind: 'letter', color: '#007ee5', label: 'OC' },
  arch: { name: 'Arch Linux', kind: 'mountain', color: '#1793d1' },
  alpine: { name: 'Alpine Linux', kind: 'mountain', color: '#0e5b90' },
  windows: { name: 'Windows', kind: 'windows' },
  darwin: { name: 'macOS', kind: 'macos' },
}
const aliases: Record<string, string> = { redhat: 'rhel', aliyun: 'alinux', macos: 'darwin' }
const system = computed<SystemMark>(() => {
  const id = (props.osId || '').trim().toLowerCase()
  return systems[aliases[id] || id] || { name: id && id !== 'unknown' ? `Linux${id === 'linux' ? '' : `（${id}）`}` : 'Linux（连接后自动识别）', kind: 'linux' }
})
</script>

<template>
  <svg class="system-icon" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 38 38" width="38" height="38" role="img" :aria-label="system.name">
    <title>{{ system.name }}</title>
    <rect x="0.4" y="0.4" width="37.2" height="37.2" rx="2.5" fill="#dee0e0" stroke="#bcbfc0" stroke-width="0.8" />
    <g v-if="system.kind === 'ubuntu'">
      <g transform="translate(19 15)">
        <circle r="10" fill="#cd3724" />
        <circle r="5.6" fill="none" stroke="#eeefef" stroke-width="1.8" />
        <g v-for="angle in [0, 120, 240]" :key="angle" :transform="`rotate(${angle})`">
          <circle cx="7.2" r="2.3" fill="#eeefef" />
          <circle cx="7.2" r="0.8" fill="#cd3724" />
        </g>
      </g>
      <text x="19" y="34.5" text-anchor="middle" font-size="7.5" fill="#1d1f20">ubuntu</text>
    </g>
    <g v-else-if="system.kind === 'letter'">
      <circle cx="19" cy="19" r="11" :fill="system.color" />
      <text x="19" y="19" text-anchor="middle" dominant-baseline="central" :font-size="(system.label?.length || 0) > 1 ? 8.5 : 16" fill="white">{{ system.label }}</text>
    </g>
    <g v-else-if="system.kind === 'mountain'">
      <path d="M7 28 17 10 21 17 25 12 31 28Z" :fill="system.color" />
      <path d="m16 20 4 3" stroke="white" stroke-width="2" />
    </g>
    <path v-else-if="system.kind === 'windows'" d="M8 9h10v10H8zm12 0h10v10H20zM8 21h10v10H8zm12 0h10v10H20z" fill="#0078d4" transform="translate(0 -1)" />
    <Apple v-else-if="system.kind === 'macos'" x="8" y="8" width="22" height="22" color="#2a2e31" />
    <g v-else transform="translate(19 19)">
      <circle cy="-5" r="6.5" fill="#2a2e31" />
      <circle cy="2.5" r="8" fill="#2a2e31" />
      <path d="m-5.5-1-5 8 3.5 2 4-6zm11 0 5 8-3.5 2-4-6z" fill="#484d50" />
      <circle cy="2.5" r="5.3" fill="#eeefeb" />
      <circle cx="-2.4" cy="-5" r="2.2" fill="#eeefeb" />
      <circle cx="2.4" cy="-5" r="2.2" fill="#eeefeb" />
      <circle cx="-1.7" cy="-5.2" r="0.7" fill="#2a2e31" />
      <circle cx="1.7" cy="-5.2" r="0.7" fill="#2a2e31" />
      <path d="M-2.4-2.8H2.4L0 .2ZM-7.5 8H-1L-2.2 11-8.5 10.5ZM7.5 8H1L2.2 11 8.5 10.5Z" fill="#d59718" />
    </g>
  </svg>
</template>

<style scoped>
.system-icon{display:block;flex-shrink:0;font-family:Arial,sans-serif}
</style>
