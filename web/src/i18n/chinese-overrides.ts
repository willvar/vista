import type zhCN from './locales/zh-CN.ts'

// Only reviewed differences from OpenCC output belong here.
const taiwan = {
  "mcpSettingsHint": "連線本地或遠端 MCP 服務，為智能體擴充工具。",
  "mcpTokenHint": "以 Authorization: Bearer 請求標頭傳送。選擇“無需認證”可移除已儲存的令牌。",
  "delegationDepthIntro": "智能體可以把部分工作交給其他智能體，在子會話中協作完成。這裡限制任務最多可以向下分派幾層。",
  "searchSettingsIntro": "為智能體配置網路搜尋與網頁讀取服務。",
  "customAuthHeader": "當前認證方式新增以下請求標頭：",
  "customAuthNone": "不自動新增認證請求標頭。",
  "customAuthOverrideHint": "如果服務要求不同的請求標頭，請在「進階設定」中配置；同名時覆蓋預設值。若只使用自定義認證頭，可選擇「無需認證」。",
  "customHeadersHint": "自定義請求標頭用於模型發現和呼叫，同名時覆蓋預設請求標頭。",
  "headerName": "請求標頭名稱",
  "headerValue": "請求標頭值",
  "addHeader": "新增請求標頭",
  "removeHeader": "移除請求標頭",
  "reset": "重設",
  "fileServiceUnavailable": "未設定檔案內容服務"
} satisfies Partial<Record<keyof typeof zhCN, string>>

// Macau starts with the Hong Kong profile; add region-specific wording when needed.
const hongKong = {
  mcpTokenHint: '以 Authorization: Bearer 請求標頭發送。選擇「無需認證」可移除已保存的令牌。',
} satisfies Partial<Record<keyof typeof zhCN, string>>

export const chineseOverrides = {
  'zh-TW': taiwan,
  'zh-HK': hongKong,
  'zh-MO': hongKong,
  'zh-SG': {},
  'zh-MY': {},
}
