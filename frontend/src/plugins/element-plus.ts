import type { App } from 'vue'
import { ElAlert } from 'element-plus/es/components/alert/index'
import { ElButton } from 'element-plus/es/components/button/index'
import { ElCheckbox } from 'element-plus/es/components/checkbox/index'
import { ElDescriptions, ElDescriptionsItem } from 'element-plus/es/components/descriptions/index'
import { ElDialog } from 'element-plus/es/components/dialog/index'
import { ElDropdown, ElDropdownItem, ElDropdownMenu } from 'element-plus/es/components/dropdown/index'
import { ElEmpty } from 'element-plus/es/components/empty/index'
import { ElForm, ElFormItem } from 'element-plus/es/components/form/index'
import { ElInput } from 'element-plus/es/components/input/index'
import { ElInputNumber } from 'element-plus/es/components/input-number/index'
import { ElLoading } from 'element-plus/es/components/loading/index'
import { ElMessage } from 'element-plus/es/components/message/index'
import { ElOption, ElSelect } from 'element-plus/es/components/select/index'
import { ElProgress } from 'element-plus/es/components/progress/index'
import { ElRadioButton, ElRadioGroup } from 'element-plus/es/components/radio/index'
import { ElTable, ElTableColumn } from 'element-plus/es/components/table/index'
import { ElTag } from 'element-plus/es/components/tag/index'
import { ElTooltip } from 'element-plus/es/components/tooltip/index'

// Register only the components used by this application, keeping unused UI out
// of the JavaScript bundle while sharing one consistent theme and locale.
export function installUI(app: App) {
  const components = {
    ElAlert, ElButton, ElCheckbox, ElDescriptions, ElDescriptionsItem,
    ElDialog, ElDropdown, ElDropdownItem, ElDropdownMenu, ElEmpty,
    ElForm, ElFormItem, ElInput, ElInputNumber, ElOption, ElProgress,
    ElRadioButton, ElRadioGroup, ElSelect, ElTable, ElTableColumn, ElTag, ElTooltip,
  }
  Object.entries(components).forEach(([name, component]) => app.component(name, component))
  app.use(ElLoading).use(ElMessage)
}
