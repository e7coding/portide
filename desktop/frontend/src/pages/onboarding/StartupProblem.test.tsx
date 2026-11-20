import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../../App';
import { renderWithProviders, runningState } from '../../test/render';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

const failed = { ...runningState, phase: 'error', error: '端口已被占用：…', errorKind: 'port-in-use', detail: 'node: listen port is already in use: listen udp :7443: bind: address already in use' };

beforeEach(() => {
  resetPortideMock();
  Service.Devices.mockResolvedValue([]);
  Service.Conversations.mockResolvedValue([]);
  Service.Transfers.mockResolvedValue({ outgoing: [], incoming: [], active: 0 });
});

test('port in use: causes, details on demand, and "start on another port"', async () => {
  const user = userEvent.setup();
  Service.State.mockResolvedValue(failed);
  Service.UseAutomaticPort.mockResolvedValue(runningState);
  renderWithProviders(<App />);
  expect(await screen.findByRole('heading', { name: '端口被占用' })).toBeInTheDocument();
  expect(screen.getByText('另一个 Portide 正在运行（包括命令行的 portide daemon）')).toBeInTheDocument();
  expect(screen.queryByText(/bind: address already in use/)).not.toBeVisible();
  await user.click(screen.getByRole('button', { name: '详细信息' }));
  expect(screen.getByText(/bind: address already in use/)).toBeVisible();
  await user.click(screen.getByRole('button', { name: '换一个端口启动' }));
  expect(Service.UseAutomaticPort).toHaveBeenCalled();
  await waitFor(() => expect(screen.getByRole('navigation', { name: '主导航' })).toBeInTheDocument());
});

test('a newer data folder shows where it is', async () => {
  Service.State.mockResolvedValue({ ...failed, errorKind: 'newer-version', error: '数据目录来自更新版本的 Portide，请升级本程序' });
  renderWithProviders(<App />);
  expect(await screen.findByRole('heading', { name: '数据来自更新版本的 Portide' })).toBeInTheDocument();
  expect(screen.getByText(runningState.dataDir)).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '换一个端口启动' })).not.toBeInTheDocument();
});

test('another daemon is running: explain and retry', async () => {
  const user = userEvent.setup();
  Service.State.mockResolvedValue({ ...runningState, phase: 'busy', error: '这个数据目录已经有守护进程在运行' });
  Service.Retry.mockResolvedValue(runningState);
  renderWithProviders(<App />);
  expect(await screen.findByRole('heading', { name: 'Portide 已经在运行' })).toBeInTheDocument();
  expect(screen.getByText('这个数据目录已经有守护进程在运行')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '重试' }));
  await waitFor(() => expect(screen.getByRole('navigation', { name: '主导航' })).toBeInTheDocument());
});
