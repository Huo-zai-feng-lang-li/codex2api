export const chartInitialDimensions = {
  dashboard: { width: 640, height: 280 },
  account: { width: 520, height: 260 },
  accountCompact: { width: 320, height: 200 },
  accountTrend: { width: 640, height: 320 },
  usagePie: { width: 240, height: 150 },
  modalPie: { width: 200, height: 200 },
} satisfies Record<string, { width: number; height: number }>
