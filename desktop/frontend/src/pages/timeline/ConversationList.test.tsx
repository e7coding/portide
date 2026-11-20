import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ConversationList } from './ConversationList';
import { renderWithNav } from '../../test/render';
import { conversation } from '../../test/fixtures';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

beforeEach(() => {
  resetPortideMock();
  localStorage.clear();
  Service.Conversations.mockResolvedValue([
    conversation({ key: '', name: '全部设备', icon: '', online: false, path: '', lastPreview: '回家路上帮我买点水果', lastAt: Date.now(), queued: 1 }),
    conversation({ lastPreview: '收到，我准时到。', lastDirection: 'out', lastAt: Date.now() }),
    conversation({ key: 'PHON', name: '手机', icon: 'smartphone', online: false, path: '', lastPreview: '照片.jpg', lastAt: 1, queued: 1 }),
    conversation({ key: 'WORK', name: '工作本', icon: 'laptop' }),
  ]);
});

test('rows show the last message, queued sends of offline devices and devices without history', async () => {
  renderWithNav(<ConversationList />, { conversation: 'DESK' });
  const all = await screen.findByRole('button', { name: '全部设备' });
  expect(within(all).getByText('1 条等待对方上线')).toBeInTheDocument();
  const desk = screen.getByRole('button', { name: '台式机' });
  expect(desk).toHaveAttribute('aria-current', 'true');
  expect(within(desk).getByText('你：收到，我准时到。')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '手机' })).getByText('1 条等待对方上线')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '工作本' })).getByText('还没有收发记录')).toBeInTheDocument();
});

test('every row is a drop target that names the device; offline ones promise delivery later', async () => {
  renderWithNav(<ConversationList />);
  const phone = await screen.findByRole('button', { name: '手机' });
  expect(phone).toHaveAttribute('data-file-drop-target');
  expect(phone).toHaveAttribute('data-peer', 'PHON');
  expect(phone).toHaveAttribute('data-drop-label', '松开发给「手机」· 它上线后自动送达');
  expect(screen.getByRole('button', { name: '台式机' })).toHaveAttribute('data-drop-label', '松开发给「台式机」');
  expect(screen.getByRole('button', { name: '全部设备' })).toHaveAttribute('data-peer', '*');
});

test('clicking a row opens it and remembers it; the search box opens the search dialog', async () => {
  const user = userEvent.setup();
  renderWithNav(<ConversationList />, { conversation: 'DESK' });
  await user.click(await screen.findByRole('button', { name: '手机' }));
  expect(screen.getByRole('button', { name: '手机' })).toHaveAttribute('aria-current', 'true');
  expect(localStorage.getItem('portide.conversation')).toBe('PHON');
  await user.click(screen.getByRole('button', { name: '搜索消息和文件' }));
  expect(await screen.findByRole('dialog')).toBeInTheDocument();
});
