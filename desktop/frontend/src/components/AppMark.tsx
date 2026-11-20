// Portide 的标志（A2「单条流线」，和应用图标、site/public/assets/mark.svg 是同一张矢量稿）：
// 深色圆角方块上两个浅色端口（设备），中间一条薄荷绿流线（传输）。
export function AppMark({ size }: { size: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" aria-hidden focusable="false" style={{ display: 'block', flexShrink: 0 }}>
      <rect width="64" height="64" rx="15" fill="#102A33" />
      <rect x="9" y="23" width="15" height="18" rx="4" fill="#F3F8F4" />
      <rect x="40" y="23" width="15" height="18" rx="4" fill="#F3F8F4" />
      <path d="M23 30c7-6 11 10 18 4" fill="none" stroke="#58E0BC" strokeWidth="6.5" strokeLinecap="round" />
    </svg>
  );
}
