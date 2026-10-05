<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { Eye, EyeOff } from '@lucide/vue'
import type { FormInstance, FormRules, InputInstance } from 'element-plus'
import { api, errorText, type Profile, type HostKey } from '../api'
import AppDialog from './AppDialog.vue'
import { useCredentialReveal } from '../credential-reveal'

const props = defineProps<{
  modelValue: boolean
  profile: Profile | null
  groups: string[]
  saving: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  submit: [payload: { profile: Profile; secret: string; keepSecret: boolean }]
  error: [message: string]
}>()

function emptyProfile(): Profile {
  return {
    id: 0, name: '', host: '', port: 22, username: 'root', authKind: 'password',
    keyPath: '', groupName: '', remark: '', lastConnectedAt: '', osId: '',
    cpuCores: 0, memoryBytes: 0, diskBytes: 0, hasSecret: false,
  }
}

const draft = reactive<Profile>(emptyProfile())
const form = ref<FormInstance>()
const nameInput = ref<InputInstance>()
const {
  secret, visible: secretVisible, loading: revealingSecret, displayValue: displaySecret,
  toggle: toggleSecret, update: updateSecret, hide: hideSecret, reset: resetSecret,
} = useCredentialReveal(id => api.RevealProfileSecret(id))
const saveCredential = ref(true)
const pickingKey = ref(false)
const validating = ref(false)
const originalAuth = ref({ kind: 'password', keyPath: '' })
const testing = ref(false)
const testResult = ref<{ success: boolean; message: string } | null>(null)
const unknownHost = ref<HostKey | null>(null)
const section = ref('basic')
const sections = [{id:'basic', label:'基本信息'}, {id:'connection',label:'连接设置'}, {id:'initialization',label:'初始化'}, {id:'jump',label:'跳板机'}, {id:'proxy',label:'代理设置'}, {id:'advanced',label:'高级设置'}]
const busy = computed(() => props.saving || pickingKey.value || validating.value || testing.value)
const sameAuthentication = computed(() => draft.authKind === originalAuth.value.kind && draft.keyPath.trim() === originalAuth.value.keyPath)
const hasSavedCredential = computed(() => !!draft.id && draft.hasSecret && sameAuthentication.value)
const secretLabel = computed(() => draft.authKind === 'private_key' ? '私钥口令' : '密码')
const secretVisibilityLabel = computed(() => `${secretVisible.value ? '隐藏' : '查看'}${secretLabel.value}`)
const secretPlaceholder = computed(() => {
  if (hasSavedCredential.value) return '已保存，留空保持不变'
  if (draft.hasSecret && !sameAuthentication.value) return '认证方式已更改，请重新填写'
  return draft.authKind === 'private_key' ? '仅加密的私钥需要填写' : '可留空，连接时再输入'
})
const credentialHint = computed(() => {
  if (!saveCredential.value) return draft.hasSecret ? '保存后将移除已有凭据，下次连接时再输入。' : '连接时输入凭据，不在此电脑保存。'
  if (hasSavedCredential.value && !secret.value) return secretVisible.value ? '当前显示已保存凭据，未编辑时保持不变。' : '留空会保留此连接已保存的凭据。'
  return '凭据保存在此电脑，方便下次连接。'
})

watch(() => [props.modelValue, props.profile] as const, async ([open, profile]) => {
  resetSecret()
  testResult.value = null
  unknownHost.value = null
  section.value = 'basic'
  if (!open) return
  Object.assign(draft, profile || emptyProfile())
  originalAuth.value = { kind: draft.authKind, keyPath: draft.keyPath.trim() }
  saveCredential.value = true
  await nextTick()
  form.value?.clearValidate()
}, { immediate: true, flush: 'sync' })

watch(() => [draft.authKind, draft.keyPath, saveCredential.value], resetSecret, { flush: 'sync' })
watch(section, hideSecret, { flush: 'sync' })
onBeforeUnmount(resetSecret)

async function toggleSecretVisibility() {
  if (busy.value || !saveCredential.value) return
  try { await toggleSecret(draft.id, hasSavedCredential.value) }
  catch (error) { emit('error', errorText(error)) }
}

const rules: FormRules<Profile> = {
  name: [
    { required: true, whitespace: true, message: '请输入连接名称', trigger: 'blur' },
    { max: 100, message: '连接名称不能超过 100 个字符', trigger: 'blur' },
  ],
  host: [
    { required: true, whitespace: true, message: '请输入主机地址', trigger: 'blur' },
    { max: 255, message: '主机地址不能超过 255 个字符', trigger: 'blur' },
  ],
  port: [{
    validator: (_rule, value, callback) => {
      if (!Number.isInteger(value) || value < 1 || value > 65535) callback(new Error('请输入 1–65535 之间的端口'))
      else callback()
    },
    trigger: ['blur', 'change'],
  }],
  username: [
    { required: true, whitespace: true, message: '请输入用户名', trigger: 'blur' },
    { max: 100, message: '用户名不能超过 100 个字符', trigger: 'blur' },
  ],
  keyPath: [{
    validator: (_rule, value, callback) => {
      if (draft.authKind === 'private_key' && !String(value || '').trim()) callback(new Error('请选择或填写私钥文件路径'))
      else callback()
    },
    trigger: ['blur', 'change'],
  }],
  groupName: [{ max: 100, message: '分组名称不能超过 100 个字符', trigger: ['blur', 'change'] }],
  remark: [{ max: 500, message: '备注不能超过 500 个字符', trigger: 'blur' }],
}

function close() {
  if (busy.value) return
  resetSecret()
  emit('update:modelValue', false)
}

function beforeClose(done: () => void) {
  if (busy.value) return
  resetSecret()
  done()
}

function onVisibilityChange(value: boolean) {
  if (!value) close()
}

async function pickKey() {
  if (busy.value) return
  pickingKey.value = true
  try {
    const path = await api.PickPrivateKey()
    if (path && props.modelValue) draft.keyPath = path
  } catch (error) {
    emit('error', errorText(error))
  } finally {
    pickingKey.value = false
  }
}

async function submit() {
  if (busy.value || !form.value) return
  validating.value = true
  const valid = await form.value.validate().catch(() => false)
  validating.value = false
  if (!valid) { section.value = 'basic'; return }
  if (props.saving || !props.modelValue) return
  const profile: Profile = {
    ...draft,
    name: draft.name.trim(), host: draft.host.trim(), username: draft.username.trim(),
    keyPath: draft.keyPath.trim(), groupName: (draft.groupName || '').trim(), remark: draft.remark.trim(),
  }
  emit('submit', {
    profile,
    secret: saveCredential.value ? secret.value : '',
    keepSecret: saveCredential.value && !secret.value && hasSavedCredential.value,
  })
}
async function testConnection(trust = false) {
  if (busy.value || !form.value) return
  validating.value = true
  section.value = 'basic'
  await nextTick()
  const valid = await form.value.validate().catch(() => false)
  validating.value = false
  if (!valid || !props.modelValue) return
  testing.value = true
  testResult.value = null
  try {
    if (trust && unknownHost.value) {
      const key = unknownHost.value
      await api.TrustHost(key.host, key.port, key.key)
      unknownHost.value = null
    }
    await api.TestConnection({ ...draft, groupName: draft.groupName || '' }, secret.value, !secret.value && hasSavedCredential.value)
    testResult.value = { success: true, message: '连接成功，认证与远程命令正常' }
  } catch (error) {
    const message = errorText(error)
    const marker = message.indexOf('HOST_KEY_UNKNOWN:')
    if (marker >= 0) {
      try { unknownHost.value = JSON.parse(message.slice(marker + 'HOST_KEY_UNKNOWN:'.length)); return }
      catch { /* Show the original error below. */ }
    }
    testResult.value = { success: false, message }
  } finally { testing.value = false }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="draft.id ? '编辑 SSH 连接' : '新建 SSH 连接'"
    width="760px"
    class="profile-editor-dialog"
    align-center append-to-body destroy-on-close
    :show-close="!busy"
    :close-on-click-modal="false"
    :close-on-press-escape="!busy && !unknownHost"
    :before-close="beforeClose"
    @update:model-value="onVisibilityChange"
    @opened="nameInput?.focus()"
    @closed="!modelValue && resetSecret()"
  >
    <template #header="{ titleId, titleClass }">
      <div class="profile-editor-heading">
        <h2 :id="titleId" :class="titleClass">{{ draft.id ? '编辑 SSH 连接' : '新建 SSH 连接' }}</h2>
        <p>管理主机地址、登录方式与连接设置</p>
      </div>
    </template>
    <div class="profile-editor-layout">
      <nav class="profile-sections" aria-label="连接设置分类">
        <button
          v-for="item in sections" :key="item.id"
          type="button" :class="{ active: section === item.id }"
          :aria-current="section === item.id ? 'page' : undefined"
          @click="section=item.id; testResult=null"
        >{{ item.label }}</button>
      </nav>
      <div class="profile-fields">
        <el-form
          id="profile-editor-form" ref="form" :model="draft" :rules="rules"
          :disabled="busy" label-position="top" require-asterisk-position="right"
          scroll-to-error :scroll-into-view-options="{ block: 'nearest' }"
          @submit.prevent="submit"
        >
          <section v-show="section === 'basic'">
            <div class="editor-section-title"><h3>主机信息</h3><p>填写连接名称与服务器地址</p></div>
            <div class="profile-name-group">
              <el-form-item label="连接名称" prop="name"><el-input ref="nameInput" v-model="draft.name" placeholder="例如：生产服务器" :maxlength="100" autocomplete="off" /></el-form-item>
              <el-form-item label="所属分组" prop="groupName"><el-select v-model="draft.groupName" :value-on-clear="''" filterable allow-create clearable default-first-option placeholder="未分组"><el-option v-for="group in groups" :key="group" :label="group" :value="group" /></el-select></el-form-item>
            </div>
            <div class="profile-host-port">
              <el-form-item label="主机地址" prop="host"><el-input v-model="draft.host" placeholder="IP 地址或域名" :maxlength="255" :spellcheck="false" /></el-form-item>
              <el-form-item label="端口" prop="port"><el-input-number v-model="draft.port" :min="1" :max="65535" :precision="0" controls-position="right" aria-label="端口" /></el-form-item>
            </div>
            <el-form-item label="备注" prop="remark"><el-input v-model="draft.remark" placeholder="用途或环境说明" :maxlength="500" /></el-form-item>
          </section>
          <section v-show="section === 'basic' || section === 'connection'" class="profile-auth-section" :class="{ 'with-divider': section === 'basic' }">
            <div class="editor-section-title"><h3>{{ section === 'basic' ? '身份认证' : '连接设置' }}</h3><p>配置登录用户和身份认证方式</p></div>
            <el-form-item label="认证方式">
              <el-radio-group v-model="draft.authKind" class="profile-auth-choice" aria-label="认证方式"><el-radio-button value="password">密码登录</el-radio-button><el-radio-button value="private_key">PEM 私钥</el-radio-button></el-radio-group>
            </el-form-item>
            <el-form-item label="登录用户" prop="username"><el-input v-model="draft.username" placeholder="root / ec2-user / ubuntu" :maxlength="100" autocomplete="off" /></el-form-item>
            <el-form-item v-if="draft.authKind === 'private_key'" label="私钥文件" prop="keyPath" required>
              <div class="key-picker"><el-input v-model="draft.keyPath" placeholder="选择 PEM 或 OpenSSH 私钥" aria-label="私钥路径" /><el-button :loading="pickingKey" @click="pickKey">浏览…</el-button></div>
            </el-form-item>
            <el-form-item :label="draft.authKind === 'private_key' ? '私钥口令' : '登录密码'">
              <el-input :model-value="displaySecret" :type="secretVisible ? 'text' : 'password'" autocomplete="off" :disabled="!saveCredential" :placeholder="secretPlaceholder" :aria-label="secretLabel" @update:model-value="updateSecret">
                <template #suffix>
                  <el-button text native-type="button" class="credential-visibility" :aria-label="secretVisibilityLabel" :title="secretVisibilityLabel" :aria-pressed="secretVisible" :loading="revealingSecret" :disabled="busy || !saveCredential" @mousedown.prevent @click="toggleSecretVisibility"><EyeOff v-if="secretVisible" :size="16" /><Eye v-else :size="16" /></el-button>
                </template>
              </el-input>
            </el-form-item>
            <div class="credential-preference"><el-checkbox v-model="saveCredential">在本机保存凭据</el-checkbox><p class="credential-hint">{{ credentialHint }}</p></div>
          </section>
        </el-form>
        <section v-if="section === 'initialization'" class="editor-settings">
          <h3>初始化</h3><p>连接成功后的终端启动行为</p>
          <dl><dt>服务器概览</dt><dd>自动检测系统与硬件基础信息</dd><dt>交互式终端</dt><dd>xterm-256color 兼容终端</dd><dt>初始目录</dt><dd>远程账户的默认主目录</dd></dl>
        </section>
        <section v-if="section === 'jump'" class="editor-settings">
          <h3>跳板机</h3><p>通过中间服务器访问目标主机</p>
          <dl><dt>当前连接方式</dt><dd>直接连接目标服务器，未启用跳板机</dd></dl>
          <el-alert title="跳板机链路尚未开放，当前页面不会保存无效配置。" type="info" :closable="false" show-icon />
        </section>
        <section v-if="section === 'proxy'" class="editor-settings">
          <h3>代理设置</h3><p>控制 SSH 网络连接的代理方式</p>
          <dl><dt>网络模式</dt><dd>直接 TCP 连接，不使用 HTTP 或 SOCKS 代理</dd></dl>
          <el-alert title="代理连接尚未启用。" type="info" :closable="false" show-icon />
        </section>
        <section v-if="section === 'advanced'" class="editor-settings">
          <h3>高级设置</h3><p>当前连接使用的底层参数</p>
          <dl><dt>连接超时</dt><dd>12 秒</dd><dt>连接保活</dt><dd>每 20 秒探测，响应超时 10 秒</dd><dt>终端类型</dt><dd>xterm-256color</dd><dt>文件传输</dt><dd>SFTP 原子替换与冲突处理</dd></dl>
        </section>
      </div>
    </div>
    <template #footer>
      <div class="profile-editor-footer">
        <el-alert v-if="testResult" class="connection-test-result" :title="testResult.message" :type="testResult.success ? 'success' : 'error'" :closable="false" show-icon role="status" />
        <div class="profile-editor-actions">
          <el-button :loading="testing" :disabled="saving || pickingKey || validating" @click="testConnection()">测试连接</el-button>
          <span class="flex-spacer" />
          <el-button :disabled="busy" @click="close">取消</el-button>
          <el-button type="primary" native-type="submit" form="profile-editor-form" :loading="saving" :disabled="pickingKey || testing || validating">{{ draft.id ? '保存修改' : '创建连接' }}</el-button>
        </div>
      </div>
    </template>
  </el-dialog>
  <AppDialog :model-value="!!unknownHost" title="确认服务器身份" :busy="testing" @update:model-value="value=>{if(!value)unknownHost=null}">
    <p class="dialog-description">首次测试 {{ unknownHost?.host }}:{{ unknownHost?.port }}，请先核对主机密钥指纹。</p>
    <code class="path-value">{{ unknownHost?.fingerprint }}</code>
    <template #footer><el-button :disabled="testing" @click="unknownHost=null">取消</el-button><el-button type="primary" :loading="testing" @click="testConnection(true)">信任并测试</el-button></template>
  </AppDialog>
</template>
<style scoped>
.profile-editor-heading h2 { margin: 0; }
.profile-editor-heading p { margin: 6px 0 0; color: var(--ui-muted); font-size: 12px; line-height: 1.6; }
.profile-editor-layout { display: flex; flex: 1 1 auto; min-height: 0; gap: 24px; height: 440px; max-height: calc(100dvh - 280px); }
.profile-sections { display: flex; flex: 0 0 124px; flex-direction: column; gap: 4px; max-height: 100%; overflow-y: auto; padding: 6px; border: 1px solid var(--ui-border); border-radius: var(--ui-radius); background: var(--ui-bg); }
.profile-sections button { height: 38px; flex-shrink: 0; padding: 0 12px; border: 0; border-radius: 6px; background: transparent; color: var(--ui-muted); font: inherit; font-size: 13px; text-align: left; cursor: pointer; transition: background .15s, color .15s; }
.profile-sections button:hover { background: var(--ui-hover); color: var(--ui-text); }
.profile-sections button.active { background: rgba(66, 197, 138, .12); color: var(--ui-accent); font-weight: 600; }
.profile-sections button:focus-visible { outline: 2px solid var(--ui-accent); outline-offset: 1px; }
.profile-fields { min-width: 0; flex: 1; overflow-y: auto; padding: 1px 8px 4px 0; scrollbar-width: thin; scrollbar-color: var(--ui-border) transparent; }
.profile-name-group { display: grid; grid-template-columns: 1.6fr 1fr; gap: 14px; }
.profile-host-port { display: grid; grid-template-columns: 1fr 108px; gap: 14px; }
.profile-host-port .el-input-number { width: 100%; }
.profile-fields :deep(.el-form-item) { margin-bottom: 18px; }
.profile-fields :deep(.el-form-item__label) { margin-bottom: 7px; color: var(--ui-muted); font-size: 12px; line-height: 18px; }
.profile-fields :deep(.el-input__wrapper), .profile-fields :deep(.el-select__wrapper) { min-height: 36px; }
.profile-auth-section.with-divider { margin-top: 22px; padding-top: 22px; border-top: 1px solid var(--ui-border); }
.profile-auth-choice { display: flex; width: 100%; }
.profile-auth-choice :deep(.el-radio-button) { flex: 1; }
.profile-auth-choice :deep(.el-radio-button__inner) { width: 100%; padding: 10px 14px; font-size: 13px; }
.key-picker { display: flex; gap: 8px; width: 100%; }
.key-picker .el-button { flex-shrink: 0; min-height: 36px; }
.credential-visibility.el-button { width: 28px; height: 28px; min-height: 0; margin: 0; padding: 0; color: var(--ui-muted); }
.credential-visibility.el-button:not(.is-disabled):hover { color: var(--ui-accent); }
.credential-preference { padding: 10px 14px 12px; border: 1px solid var(--ui-border); border-radius: var(--ui-radius); background: var(--ui-raised); }
.credential-preference .el-checkbox { height: 26px; }
.credential-hint { margin: 4px 0 0 24px; color: var(--ui-muted); font-size: 12px; line-height: 1.7; }
.profile-editor-footer { display: flex; flex-direction: column; gap: 14px; }
.profile-editor-actions { display: flex; align-items: center; gap: 10px; }
.profile-editor-actions .el-button { margin-left: 0; }
.connection-test-result { max-height: 78px; overflow-y: auto; text-align: left; overflow-wrap: anywhere; }
.editor-settings h3, .editor-section-title h3 { margin: 0 0 6px; color: var(--ui-text); font-size: 14px; font-weight: 600; }
.editor-settings p, .editor-section-title p { margin: 0; color: var(--ui-muted); font-size: 12px; line-height: 1.7; }
.editor-settings dl { margin: 22px 0; }
.editor-settings dt { margin: 16px 0 7px; color: var(--ui-muted); font-size: 12px; }
.editor-settings dd { margin: 0; padding: 12px 14px; border: 1px solid var(--ui-border); border-radius: var(--ui-radius); background: var(--ui-raised); color: var(--ui-text); font-size: 13px; line-height: 1.6; }
.editor-section-title { margin-bottom: 20px; }
@media (max-width: 640px) {
  .profile-editor-layout { gap: 14px; }
  .profile-sections { flex-basis: 100px; padding: 4px; }
  .profile-sections button { padding: 0 8px; }
  .profile-name-group { grid-template-columns: 1fr; gap: 0; }
}
</style>
<style>
.profile-editor-dialog { display: flex; flex-direction: column; max-width: calc(100vw - 40px); max-height: calc(100dvh - 32px); padding: 24px; }
.profile-editor-dialog .el-dialog__header { margin: 0; padding: 0 34px 20px 0; border-bottom: 1px solid var(--ui-border); text-align: left; }
.profile-editor-dialog .el-dialog__title { color: var(--ui-text); font-size: 18px; font-weight: 600; line-height: 1.4; }
.profile-editor-dialog .el-dialog__body { display: flex; flex: 1 1 auto; flex-direction: column; min-height: 0; padding: 22px 0; overflow: hidden; }
.profile-editor-dialog .el-dialog__footer { flex-shrink: 0; padding: 18px 0 0; border-top: 1px solid var(--ui-border); }
.profile-editor-dialog .el-dialog__headerbtn { top: 14px; right: 14px; }
</style>
