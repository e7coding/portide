// 英文界面：格式化函数输出英文、切换语言时调用 Go 端并立即换成新语言的文字。
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { formatAgo, formatDay, formatEta, quoteNames, taskStatusText } from '../format';
import { Appearance } from '../pages/settings/Appearance';
import { renderWithProviders } from '../test/render';
import { Platform, resetPortideMock } from '../test/portideMock';
import { LanguageProvider, setLangForTest, t } from './index';

vi.mock('../api/portide', () => import('../test/portideMock'));

beforeEach(() => resetPortideMock());
afterEach(() => setLangForTest('zh'));

test('格式化函数按英文输出', () => {
  setLangForTest('en');
  const now = new Date(2026, 10, 9, 12, 0).getTime();
  expect(formatDay(now, now)).toBe('Today');
  expect(formatDay(new Date(2026, 9, 24).getTime(), now)).toBe('Oct 24');
  expect(formatDay(new Date(2025, 9, 24).getTime(), now)).toBe('Oct 24, 2025');
  expect(formatEta(45)).toBe('about 45 s');
  expect(formatAgo(now - 2 * 3600 * 1000, now)).toBe('2 hours ago');
  expect(formatAgo(now - 3600 * 1000, now)).toBe('1 hour ago');
  expect(taskStatusText('pending', false)).toBe('Queued until the other device comes online');
  expect(quoteNames(['Mac', 'Phone'])).toBe('Mac, Phone');
  expect(t('{n} 个文件', { n: 1 })).toBe('1 file');
  expect(t('{n} 个文件', { n: 3 })).toBe('3 files');
});

test('中文仍是原文', () => {
  const now = new Date(2026, 10, 9, 12, 0).getTime();
  expect(formatDay(new Date(2026, 9, 24).getTime(), now)).toBe('10月24日');
  expect(quoteNames(['电脑', '手机'])).toBe('「电脑」「手机」');
});

test('设置 › 外观里切换到英文：通知 Go 端，界面立即换成英文', async () => {
  renderWithProviders(
    <LanguageProvider initial="zh">
      <Appearance />
    </LanguageProvider>,
  );
  const english = await screen.findByRole('button', { name: 'English' });
  expect(screen.getByText('语言')).toBeInTheDocument();
  await userEvent.click(english);
  await waitFor(() => expect(Platform.SetLanguage).toHaveBeenCalledWith('en'));
  expect(await screen.findByText('Language')).toBeInTheDocument();
  expect(document.documentElement.lang).toBe('en');
});
