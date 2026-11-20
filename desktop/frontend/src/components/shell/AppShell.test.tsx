import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Events } from '@wailsio/runtime';
import { AppShell, FILES_DROPPED_EVENT, SHOW_TIMELINE_EVENT } from './AppShell';
import { renderWithNav, runningState } from '../../test/render';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));
// 页面本身在各自的测试里测；这里换成只显示名字的替身。
vi.mock('../../pages', () => {
  const page = (name: string) => () => <div>页面：{name}</div>;
  return {
    PAGES: {
      timeline: { Content: page('时间线'), List: () => <aside>会话列表</aside> },
      transfers: { Content: page('传输') },
      devices: { Content: page('设备') },
      settings: { Content: page('设置') },
    },
  };
});

const dev = (keyId: string, self = false) => ({ keyId, name: keyId === 'DESK' ? '台式机' : keyId, icon: 'monitor', self, online: true, path: 'lan', addr: '', lastSeen: 0, joinedAt: 0 });
const handlers: Record<string, (ev: { data: unknown }) => void> = {};

beforeEach(() => {
  resetPortideMock();
  localStorage.clear();
  Service.Devices.mockResolvedValue([dev('SELF', true), dev('DESK')]);
  Service.Transfers.mockResolvedValue({ outgoing: [], incoming: [], active: 2 });
  Service.SendFiles.mockResolvedValue({ tasks: 1, skipped: [] });
  vi.mocked(Events.On).mockImplementation(((name: string, cb: (ev: { data: unknown }) => void) => {
    handlers[name] = cb;
    return () => {};
  }) as unknown as typeof Events.On);
});

test('the rail switches pages; transfers has no list pane; the badge counts active transfers only', async () => {
  const user = userEvent.setup();
  renderWithNav(<AppShell state={runningState} />, { conversation: 'DESK' });
  expect(screen.getByText('页面：时间线')).toBeInTheDocument();
  expect(screen.getByText('会话列表')).toBeInTheDocument();
  expect(await screen.findByLabelText('2 个正在传输')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '传输' }));
  expect(screen.getByText('页面：传输')).toBeInTheDocument();
  expect(screen.queryByText('会话列表')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: '传输' })).toHaveAttribute('aria-current', 'page');
});

test('Ctrl+1–4 switch pages, Ctrl+K goes to the timeline', () => {
  renderWithNav(<AppShell state={runningState} />);
  fireEvent.keyDown(window, { key: '4', ctrlKey: true });
  expect(screen.getByText('页面：设置')).toBeInTheDocument();
  fireEvent.keyDown(window, { key: '3', ctrlKey: true });
  expect(screen.getByText('页面：设备')).toBeInTheDocument();
  fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
  expect(screen.getByText('页面：时间线')).toBeInTheDocument();
  fireEvent.keyDown(window, { key: '2' }); // 没按 Ctrl：不切换
  expect(screen.getByText('页面：时间线')).toBeInTheDocument();
});

test('a notification click shows the timeline', () => {
  renderWithNav(<AppShell state={runningState} />, { page: 'devices' });
  act(() => handlers[SHOW_TIMELINE_EVENT]({ data: null }));
  expect(screen.getByText('页面：时间线')).toBeInTheDocument();
});

test('dropped files go to the open conversation; with no target a warning explains what to do', async () => {
  renderWithNav(<AppShell state={runningState} />, { conversation: 'DESK' });
  await screen.findByLabelText('2 个正在传输'); // 设备列表已加载
  await waitFor(() => expect(Service.Devices).toHaveBeenCalled());
  await act(async () => handlers[FILES_DROPPED_EVENT]({ data: { paths: ['/tmp/a.txt'], peer: '' } }));
  await waitFor(() => expect(Service.SendFiles).toHaveBeenCalledWith(['DESK'], ['/tmp/a.txt']));

  Service.SendFiles.mockRejectedValueOnce(new Error('目标设备不在设备圈里'));
  await act(async () => handlers[FILES_DROPPED_EVENT]({ data: { paths: ['/tmp/b.txt'], peer: 'DESK' } }));
  expect(await screen.findByRole('alert')).toHaveTextContent('目标设备不在设备圈里');
  expect(screen.getByRole('alert')).toHaveAttribute('data-kind', 'error');
});

test('a drop in "all devices" with nothing chosen warns instead of sending', async () => {
  Service.Devices.mockResolvedValue([dev('SELF', true), dev('DESK'), dev('PHON')]);
  renderWithNav(<AppShell state={runningState} />, { conversation: '' });
  await waitFor(() => expect(Service.Devices).toHaveBeenCalled());
  await act(async () => new Promise((r) => setTimeout(r, 0)));
  await act(async () => handlers[FILES_DROPPED_EVENT]({ data: { paths: ['/tmp/a.txt'], peer: '' } }));
  expect(await screen.findByRole('alert')).toHaveAttribute('data-kind', 'warning');
  expect(Service.SendFiles).not.toHaveBeenCalled();
});
