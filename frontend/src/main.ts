import { createApp, h } from 'vue'
import { ElConfigProvider } from 'element-plus/es/components/config-provider/index'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import App from './App.vue'
import { installUI } from './plugins/element-plus'
import './style.css'
import './element-theme.css'

const app = createApp({ render: () => h(ElConfigProvider, { locale: zhCn }, () => h(App)) })
installUI(app)
app.mount('#app')
