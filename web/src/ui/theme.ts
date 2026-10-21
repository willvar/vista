import { theme, type ThemeConfig } from 'antdv-next'
import { computed, ref } from 'vue'
import { resolvedAppearance } from './appearance'

const motionPreference = window.matchMedia('(prefers-reduced-motion: reduce)')
const reducedMotion = ref(motionPreference.matches)
motionPreference.addEventListener('change', event => { reducedMotion.value = event.matches })

export const vistaTheme = computed<ThemeConfig>(() => {
  const dark = resolvedAppearance.value === 'dark'
  return {
    algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      motion: !reducedMotion.value,
      colorPrimary: dark ? '#5686fe' : '#2563eb',
      colorInfo: dark ? '#5686fe' : '#2563eb',
      colorBgBase: dark ? '#151517' : '#ffffff',
      colorBgContainer: dark ? '#2c2c2e' : '#ffffff',
      colorBgElevated: dark ? '#353638' : '#ffffff',
      colorBgMask: dark ? 'rgba(0, 0, 0, 0.36)' : 'rgba(15, 23, 42, 0.22)',
      colorText: dark ? '#f9fafb' : '#172b4d',
      colorTextSecondary: dark ? '#adb2b8' : '#5e7391',
      colorBorder: dark ? '#ffffff1f' : '#d5e2f4',
      colorBorderSecondary: dark ? '#ffffff0f' : '#e5edfa',
      colorLink: dark ? '#679efe' : '#2563eb',
      borderRadius: 8,
      controlHeight: 36,
      fontSize: 16,
      fontFamily: 'Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
    },
    components: {
      Button: { primaryShadow: 'none', defaultShadow: 'none' },
      Modal: { borderRadiusLG: 16, boxShadow: dark ? '0 12px 40px #00000038, 0 2px 6px #0000001f' : '0 12px 40px #143e7814, 0 2px 6px #143e7808' },
      Tooltip: { colorBgSpotlight: dark ? '#43454a' : '#172b4d', colorTextLightSolid: dark ? '#f9fafb' : '#ffffff' },
    },
  }
})
