import '@testing-library/jest-dom/vitest';
import { vi } from 'vitest';

// Wails 运行时只在桌面窗口里可用；测试里把事件订阅换成记录回调的替身（见 api/changes.test.ts），剪贴板换成 vi.fn()。
vi.mock('@wailsio/runtime', () => ({ Events: { On: vi.fn(() => () => {}) }, Clipboard: { SetText: vi.fn(() => Promise.resolve()) } }));

// jsdom 没有 matchMedia（主题"跟随系统"要用）。
window.matchMedia ??= ((query: string) => ({
  matches: false, media: query, onchange: null,
  addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
})) as unknown as typeof window.matchMedia;
