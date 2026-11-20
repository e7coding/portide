import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Transfers, transferStatus } from './Transfers';
import { renderWithNav } from '../test/render';
import { device, item, self, transfer } from '../test/fixtures';
import { Service, resetPortideMock } from '../test/portideMock';

vi.mock('../api/portide', () => import('../test/portideMock'));

const DAY = 24 * 3600 * 1000;
const sending = transfer({ key: 'o1', id: 1, summary: '视频.mp4', status: 'sending', bytesDone: 1024 * 1024, bytesTotal: 4 * 1024 * 1024, bytesPerSec: 1024 * 1024, etaSec: 3 });
const paused = transfer({ key: 'o2', id: 2, summary: '照片（3 个文件）', status: 'interrupted', paused: true, bytesDone: 10, bytesTotal: 100, error: '连接断开' });
const queued = transfer({ key: 'o3', id: 3, peerKey: 'PHON', peerName: '手机', summary: '文本「稍后」', status: 'pending', at: Date.now() - DAY });
const receiving = transfer({ key: 'iDESK:ab', direction: 'in', peerKey: 'DESK', transferId: 'ab', summary: '报告.pdf', status: 'receiving', bytesDone: 50, bytesTotal: 100, bytesPerSec: 25, etaSec: 2 });

beforeEach(() => {
  resetPortideMock();
  Service.Transfers.mockResolvedValue({ outgoing: [sending, paused, queued], incoming: [receiving], active: 2 });
  Service.Devices.mockResolvedValue([self, device(), device({ keyId: 'PHON', name: '手机', online: false })]);
  Service.Settings.mockResolvedValue({ queueDays: 7 });
  Service.Timeline.mockResolvedValue([]);
});

test('queued and interrupted sends explain themselves', () => {
  const now = Date.now();
  expect(transferStatus(queued, 7, now)).toBe('排队中，等「手机」上线 · 还会等 6 天');
  expect(transferStatus({ ...queued, at: now - 8 * DAY }, 7, now)).toBe('排队中，等「手机」上线');
  expect(transferStatus({ ...paused, paused: false }, 7, now)).toBe('中断，稍后自动重试（连接断开）');
  expect(transferStatus(paused, 7, now)).toBe('已暂停');
});

test('summary counts, groups, connection labels, progress, speed and remaining time', async () => {
  renderWithNav(<Transfers />);
  const send = await screen.findByTestId('transfer-o1');
  expect(within(send).getByText('发给「台式机」')).toBeInTheDocument();
  expect(within(send).getByText('局域网')).toBeInTheDocument();
  expect(within(send).getByText('正在发送 · 1.0 MB / 4.0 MB · 1.0 MB/s · 剩余约 3 秒')).toBeInTheDocument();
  expect(within(send).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '25');
  expect(within(screen.getByTestId('transfer-o2')).queryByRole('progressbar')).not.toBeInTheDocument();
  expect(within(screen.getByTestId('transfer-o3')).queryByText('局域网')).not.toBeInTheDocument(); // 离线：没有连接方式
  const recv = screen.getByTestId('transfer-iDESK:ab');
  expect(within(recv).getByText('来自「台式机」')).toBeInTheDocument();
  expect(within(recv).getByText('正在接收 · 50 B / 100 B · 25 B/s · 剩余约 2 秒')).toBeInTheDocument();
  expect(screen.getByRole('region', { name: '发送' })).toBeInTheDocument();
  expect(screen.getByRole('region', { name: '接收' })).toBeInTheDocument();
  expect(screen.getByText('个正在传输').previousSibling).toHaveTextContent('2');
  expect(screen.getByText('个等待中').previousSibling).toHaveTextContent('2');
});

test('pause, resume, cancel, and pause all / resume all', async () => {
  const user = userEvent.setup();
  renderWithNav(<Transfers />);
  await user.click(within(await screen.findByTestId('transfer-o1')).getByRole('button', { name: '暂停' }));
  expect(Service.PauseSend).toHaveBeenCalledWith(1);
  await user.click(within(screen.getByTestId('transfer-o2')).getByRole('button', { name: '继续' }));
  expect(Service.ResumeSend).toHaveBeenCalledWith(2);
  await user.click(within(screen.getByTestId('transfer-o3')).getByRole('button', { name: '取消' }));
  expect(Service.CancelSend).toHaveBeenCalledWith(3);
  await user.click(screen.getByRole('button', { name: '全部暂停' }));
  expect(Service.PauseAll).toHaveBeenCalled();
  expect(await screen.findByRole('status')).toHaveTextContent('已全部暂停');
});

test('an interrupted send can be retried right away; paused, queued and sending ones cannot', async () => {
  const user = userEvent.setup();
  const failed = transfer({ key: 'o4', id: 4, summary: '报告.pdf', status: 'interrupted', error: '连接中断' });
  Service.Transfers.mockResolvedValue({ outgoing: [sending, paused, queued, failed], incoming: [], active: 1 });
  renderWithNav(<Transfers />);
  await user.click(within(await screen.findByTestId('transfer-o4')).getByRole('button', { name: '立即重试' }));
  expect(Service.RetrySend).toHaveBeenCalledWith(4);
  expect(await screen.findByRole('status')).toHaveTextContent('正在重试');
  for (const key of ['o1', 'o2', 'o3']) {
    expect(within(screen.getByTestId(`transfer-${key}`)).queryByRole('button', { name: '立即重试' })).not.toBeInTheDocument();
  }
});

test('when every send is paused the button resumes them all', async () => {
  const user = userEvent.setup();
  Service.Transfers.mockResolvedValue({ outgoing: [paused], incoming: [], active: 0 });
  renderWithNav(<Transfers />);
  await user.click(await screen.findByRole('button', { name: '全部继续' }));
  expect(Service.ResumeAll).toHaveBeenCalled();
});

test('stopping a receive asks first', async () => {
  const user = userEvent.setup();
  renderWithNav(<Transfers />);
  await user.click(within(await screen.findByTestId('transfer-iDESK:ab')).getByRole('button', { name: '停止接收' }));
  const dialog = screen.getByRole('dialog');
  expect(dialog).toHaveTextContent('已收到的部分会删除');
  await user.click(within(dialog).getByRole('button', { name: '继续接收' }));
  expect(Service.StopReceive).not.toHaveBeenCalled();
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  await user.click(within(screen.getByTestId('transfer-iDESK:ab')).getByRole('button', { name: '停止接收' }));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: '停止接收' }));
  expect(Service.StopReceive).toHaveBeenCalledWith('DESK', 'ab');
});

test('a failing action is shown in red', async () => {
  const user = userEvent.setup();
  Service.PauseSend.mockRejectedValue({ message: '这个任务已经结束' });
  renderWithNav(<Transfers />);
  await user.click(within(await screen.findByTestId('transfer-o1')).getByRole('button', { name: '暂停' }));
  const alert = await screen.findByRole('alert');
  expect(alert).toHaveTextContent('这个任务已经结束');
  expect(alert).toHaveAttribute('data-kind', 'error');
});

test('the five most recent finished transfers link to the timeline', async () => {
  const user = userEvent.setup();
  Service.Transfers.mockResolvedValue({ outgoing: [], incoming: [], active: 0 });
  Service.Timeline.mockResolvedValue([
    ...[1, 2, 3, 4, 5].map((i) => item({ key: `m${i}`, id: i, texts: [`第 ${i} 条`] })),
    item({ key: 'm6', id: 6, direction: 'out', status: 'failed', error: '对方拒绝了', files: [{ name: 'a.zip', size: 1, exists: true }], fileCount: 1 }),
    item({ key: 't7', kind: 'task', id: 7, summary: '还在发' }),
  ]);
  renderWithNav(<Transfers />, { conversation: 'PHON' });
  expect(await screen.findByText('没有进行中的传输')).toBeInTheDocument();
  const recent = await screen.findByRole('region', { name: '最近完成' });
  const rows = within(recent).getAllByRole('button');
  expect(rows).toHaveLength(5);
  expect(rows[0]).toHaveTextContent('a.zip');
  expect(rows[0]).toHaveTextContent('没有送达：对方拒绝了');
  expect(within(recent).queryByText('第 1 条')).not.toBeInTheDocument();
  await user.click(rows[0]); // 跳到那台设备的会话（并高亮那一条）
  expect(localStorage.getItem('portide.conversation')).toBe('DESK');
});
