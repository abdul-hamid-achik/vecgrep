import DefaultTheme from 'vitepress/theme'
import './custom.css'
import { inject } from '@vercel/analytics'

export default {
  extends: DefaultTheme,
  enhanceApp() {
    if (typeof window !== 'undefined') {
      inject()
    }
  },
}
