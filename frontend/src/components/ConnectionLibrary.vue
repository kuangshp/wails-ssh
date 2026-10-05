<script setup lang="ts">
import { computed } from 'vue'
import { Copy, KeyRound, Plus, RefreshCw, Search } from '@lucide/vue'
import { formatBytes, type Profile } from '../api'
import SystemIcon from './SystemIcon.vue'
const props = defineProps<{ profiles: Profile[]; title: string; search: string; loading: boolean; connectingId: number | null; total: number }>()
const emit = defineEmits<{
  'update:search': [value: string]; create: []; edit: [profile: Profile]; delete: [profile: Profile]; connect: [profile: Profile];
  import: [legacy: boolean]; refresh: []; copy: [text: string]; 'copy-credentials': [profile: Profile]; reset: []
}>()
const searchValue = computed({ get: () => props.search, set: (value: string) => emit('update:search', value) })
const host = (row: Profile) => row.host.includes(':') && !row.host.startsWith('[') ? `[${row.host}]` : row.host
</script>
<template>
  <section class="connection-library">
    <header class="library-toolbar">
      <strong class="library-title">本地主机库</strong>
      <el-input v-model="searchValue" clearable placeholder="搜索名称、地址或备注" aria-label="搜索主机"><template #prefix><Search :size="16" /></template></el-input>
      <span class="library-filter-label">{{ title }} · {{ profiles.length }}</span>
      <el-dropdown trigger="click" @command="emit('import', $event === 'legacy')"><el-button text class="library-import">导入连接</el-button><template #dropdown><el-dropdown-menu><el-dropdown-item command="ssh">OpenSSH 配置</el-dropdown-item><el-dropdown-item command="legacy">旧版数据库</el-dropdown-item></el-dropdown-menu></template></el-dropdown>
      <el-button text class="library-refresh" aria-label="刷新主机" title="刷新主机" :disabled="loading" @click="emit('refresh')"><RefreshCw :size="16" /></el-button>
      <el-button type="primary" aria-label="新建 SSH 连接" @click="emit('create')"><Plus :size="16" /><span>新建连接</span></el-button>
    </header>
    <div v-loading="loading" class="library-table-wrap">
      <el-table :data="profiles" row-key="id" height="100%" class="host-table" @row-dblclick="(row: Profile) => emit('connect', row)">
        <el-table-column label="系统" width="76"><template #default="{ row }: {row: Profile}"><div class="system-badge"><SystemIcon :os-id="row.osId" /></div></template></el-table-column>
        <el-table-column label="名称" min-width="150"><template #default="{row}: {row:Profile}"><strong class="host-name" :title="row.name">{{row.name}}</strong></template></el-table-column>
        <el-table-column label="地址" min-width="215"><template #default="{row}: {row:Profile}"><div class="host-address" @dblclick.stop><code :title="`${host(row)}:${row.port}`">{{host(row)}}:{{row.port}}</code><div class="host-copy-actions"><el-button text size="small" aria-label="复制 IP" title="复制 IP" @click.stop="emit('copy',row.host)"><Copy :size="13" />IP</el-button><el-button text size="small" aria-label="复制用户名" title="复制用户名" @click.stop="emit('copy',row.username)">用户</el-button><el-button text size="small" :disabled="!row.hasSecret" :aria-label="row.authKind === 'private_key' ? '复制全部（IP、账号、私钥口令）' : '复制全部（IP、账号、密码）'" :title="row.authKind === 'private_key' ? '复制全部：IP、端口、账号、私钥口令' : '复制全部：IP、端口、账号、密码'" @click.stop="emit('copy-credentials',row)"><KeyRound :size="13" /></el-button></div></div></template></el-table-column>
        <el-table-column label="信息" min-width="190"><template #default="{row}: {row:Profile}"><div class="host-facts"><span title="CPU 核心数">{{row.cpuCores ? `${row.cpuCores} 核` : 'CPU —'}}</span><span title="总内存">{{row.memoryBytes ? formatBytes(row.memoryBytes) : '内存 —'}}</span><span title="磁盘容量">{{row.diskBytes ? formatBytes(row.diskBytes) : '磁盘 —'}}</span></div></template></el-table-column>
        <el-table-column label="备注" min-width="85" show-overflow-tooltip><template #default="{row}: {row:Profile}">{{row.remark || '—'}}</template></el-table-column>
        <el-table-column label="操作" width="178" fixed="right" align="right"><template #default="{row}: {row:Profile}"><div class="host-actions" @click.stop @dblclick.stop><el-button type="primary" size="small" :loading="connectingId===row.id" :disabled="connectingId!==null" @click="emit('connect',row)">连接</el-button><el-button text size="small" @click="emit('edit',row)">编辑</el-button><el-button text type="danger" size="small" @click="emit('delete',row)">删除</el-button></div></template></el-table-column>
        <template #empty><el-empty :description="search ? '没有匹配的主机' : total ? '此分组暂无主机' : '还没有保存的主机'" :image-size="64"><el-button v-if="search" @click="emit('update:search','')">清除搜索</el-button><el-button v-else type="primary" @click="emit('create')">新建 SSH 连接</el-button><el-button v-if="total" text @click="emit('reset')">全部主机</el-button></el-empty></template>
      </el-table>
    </div>
  </section>
</template>
<style scoped>
.connection-library {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  padding: 18px 20px 20px;
  background: var(--ui-bg);
}
.library-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 38px;
  margin-bottom: 18px;
}
.library-title { flex-shrink: 0; color: var(--ui-text); font-size: 15px; font-weight: 600; }
.library-toolbar > .el-input { width: 280px; min-width: 180px; flex-shrink: 1; }
.library-toolbar > .el-button { margin-left: 0; }
.library-toolbar > .el-button--primary svg { margin-right: 6px; }
.library-filter-label { min-width: 64px; max-width: 220px; margin-right: auto; overflow: hidden; color: var(--ui-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.library-refresh { width: 34px; padding: 0; }
.library-table-wrap { min-height: 0; flex: 1; border: 1px solid var(--ui-border); border-radius: var(--ui-radius); overflow: hidden; }
.host-table {
  --el-table-header-bg-color: var(--ui-raised);
  --el-table-tr-bg-color: var(--ui-panel);
  --el-table-row-hover-bg-color: var(--ui-hover);
  --el-table-bg-color: var(--ui-panel);
  --el-table-border-color: var(--ui-border);
  --el-table-header-text-color: var(--ui-muted);
  --el-table-text-color: var(--ui-text);
  --el-table-fixed-left-column: inset 8px 0 8px -8px rgba(0, 0, 0, .3);
  --el-table-fixed-right-column: inset -8px 0 8px -8px rgba(0, 0, 0, .3);
}
.host-table :deep(.el-table__cell) { height: 64px; padding: 8px 0; }
.host-table :deep(th.el-table__cell) { height: 44px; padding: 8px 0; font-weight: 500; }
.host-table :deep(.cell) { padding: 0 12px; font-size: 13px; line-height: 1.5; }
.system-badge { display: flex; align-items: center; justify-content: center; }
.host-name { display: block; overflow: hidden; color: var(--ui-text); font-size: 13px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.host-address code { display: block; max-width: 100%; overflow: hidden; color: var(--ui-text); font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.host-copy-actions { display: flex; align-items: center; gap: 5px; margin-top: 3px; }
.host-copy-actions .el-button { height: 22px; margin-left: 0; padding: 0 4px; color: var(--ui-muted); font-size: 12px; }
.host-copy-actions .el-button:not(.is-disabled):hover { color: var(--ui-accent); }
.host-copy-actions .el-button svg { margin-right: 3px; }
.host-copy-actions .el-button:last-child svg { margin-right: 0; }
.host-facts { display: flex; flex-wrap: wrap; gap: 5px; }
.host-facts span { padding: 3px 5px; border: 1px solid var(--ui-border); border-radius: 5px; background: var(--ui-raised); color: var(--ui-muted); font-size: 12px; line-height: 18px; white-space: nowrap; }
.host-actions { display: flex; align-items: center; justify-content: flex-end; gap: 3px; }
.host-actions .el-button { height: 30px; margin-left: 0; padding: 5px 9px; font-size: 12px; }
.host-actions .el-button.is-text:not(.el-button--danger) { color: var(--ui-muted); }
@media (max-width: 1120px) {
  .connection-library { padding: 16px; }
  .library-toolbar { flex-wrap: wrap; gap: 10px; }
  .library-toolbar > .el-input { flex: 1; min-width: 190px; }
  .library-filter-label { flex: 1; }
}
</style>
