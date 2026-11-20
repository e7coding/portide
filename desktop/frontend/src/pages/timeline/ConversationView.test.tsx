import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Clipboard } from '@wailsio/runtime';
import { ConversationView } from './ConversationView';
import { renderWithNav } from '../../test/render';
import { device, item, self } from '../../test/fixtures';
import { Platform, Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

const desk = device();
const phone = device({ keyId: 'PHON', name: '手机', icon: 'smartphone', online: false, path: '', addr: '', lastSeen: Date.now() - 2 * 3600_000 });

beforeEach(() => {
  resetPortideMock();
  localStorage.clear();
  Service.Devices.mockResolvedValue([self, desk, phone]);
  Service.Timeline.mockResolvedValue([]);
  Service.Inbox.mockResolvedValue([]);
  Service.Thumbnail.mockResolvedValue('');
  Service.SendText.mockResolvedValue({ tasks: 1, skipped: [] });
  Service.SendFiles.mockResolvedValue({ tasks: 1, skipped: [] });
});

test('the header shows status, connection and address; the folder button opens received files', async () => {
  const user = userEvent.setup();
  renderWithNav(<ConversationView />, { conversation: 'DESK' });
  expect(await screen.findByRole('heading', { name: '台式机' })).toBeInTheDocument();
  expect(screen.getByText('在线 · 局域网 · 192.168.1.5:7443')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '收到的文件' }));
  expect(Service.OpenSaveDir).toHaveBeenCalled();
  expect(Service.Timeline).toHaveBeenCalledWith('DESK', 200);
});

test('an empty conversation is a big drop zone; with no other devices it guides to adding one', async () => {
  const { unmount } = renderWithNav(<ConversationView />, { conversation: 'PHON' });
  expect(await screen.findByText('发给「手机」')).toBeInTheDocument();
  expect(screen.getByText(/手机现在离线，发出的内容会在它上线后送达/)).toBeInTheDocument();
  expect(screen.getByText('离线 · 2 小时前在线')).toBeInTheDocument();
  expect(screen.getByTestId('feed')).toHaveAttribute('data-peer', 'PHON');
  unmount();

  Service.Devices.mockResolvedValue([self]);
  renderWithNav(<ConversationView />);
  expect(await screen.findByText('设备圈里还没有其他设备')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: '显示配对码' })).toBeInTheDocument();
});

test('texts link out through the system browser; files and folders are cards', async () => {
  const user = userEvent.setup();
  Service.Timeline.mockResolvedValue([
    item({ key: 'm1', id: 1, texts: ['会议纪要 https://wiki.example.com/x'] }),
    item({ key: 'm2', id: 2, files: [{ name: '季度报告.pdf', size: 2_516_582, exists: true }], fileCount: 1, bytesTotal: 2_516_582 }),
    item({
      key: 'm3', id: 3, fileCount: 9, bytesTotal: 1024 * 1024,
      files: ['a.jpg', 'b.png', 'c.txt', 'd.gif', 'e.jpg'].map((n) => ({ name: `项目资料/${n}`, size: 1, exists: true })),
    }),
  ]);
  Service.Thumbnail.mockImplementation(async (_id: number, i: number) => (i === 0 ? 'data:image/png;base64,AAAA' : ''));
  renderWithNav(<ConversationView />, { conversation: 'DESK' });

  await user.click(await screen.findByRole('button', { name: 'https://wiki.example.com/x' }));
  expect(Platform.OpenURL).toHaveBeenCalledWith('https://wiki.example.com/x');

  const file = screen.getByTestId('file-card');
  expect(within(file).getByText('季度报告.pdf')).toBeInTheDocument();
  expect(within(file).getByText('2.4 MB')).toBeInTheDocument();
  await user.click(within(file).getByRole('button', { name: '打开 季度报告.pdf' }));
  expect(Service.OpenFile).toHaveBeenCalledWith(2, 0);

  const folder = screen.getByTestId('folder-card');
  expect(within(folder).getByText('项目资料')).toBeInTheDocument();
  expect(within(folder).getByText('9 个文件 · 1.0 MB')).toBeInTheDocument();
  expect(within(folder).getByText('+6')).toBeInTheDocument(); // 3 个缩略图 + 其余 6 个
  await user.click(within(folder).getByRole('button', { name: '展开文件列表' }));
  expect(within(folder).getByText('还有 4 个文件，请在文件夹中查看')).toBeInTheDocument();
});

test('sent messages show delivery; failures can be resent; a stopped receive keeps its summary', async () => {
  const user = userEvent.setup();
  Service.Timeline.mockResolvedValue([
    item({ key: 'm1', id: 1, direction: 'out', texts: ['好的'] }),
    item({ key: 'm2', id: 2, direction: 'out', status: 'failed', error: '对方拒绝了', files: [{ name: 'a.txt', size: 1, exists: true }], fileCount: 1 }),
    item({ key: 'm3', id: 3, status: 'cancelled', error: '已停止接收', summary: '季度报告.pdf（2.0 MB）' }),
  ]);
  Service.HistoryFiles.mockResolvedValue(['/home/me/a.txt']);
  renderWithNav(<ConversationView />, { conversation: 'DESK' });
  expect(await screen.findByText('已送达')).toBeInTheDocument();
  expect(screen.getByText('没有送达：对方拒绝了')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '重发' }));
  await waitFor(() => expect(Service.SendFiles).toHaveBeenCalledWith(['DESK'], ['/home/me/a.txt']));
  const stopped = screen.getByTestId('item-m3');
  expect(within(stopped).getByText('季度报告.pdf（2.0 MB）')).toBeInTheDocument();
  expect(within(stopped).getByText('已停止接收')).toBeInTheDocument();
});

test('the context menu copies and deletes a record', async () => {
  const user = userEvent.setup();
  Service.Timeline.mockResolvedValue([item({ key: 'm1', id: 7, texts: ['你好'] })]);
  renderWithNav(<ConversationView />, { conversation: 'DESK' });
  fireEvent.contextMenu(await screen.findByText('你好'));
  await user.click(screen.getByRole('menuitem', { name: '复制' }));
  expect(Clipboard.SetText).toHaveBeenCalledWith('你好');
  fireEvent.contextMenu(screen.getByText('你好'));
  await user.click(screen.getByRole('menuitem', { name: '删除这条记录' }));
  expect(Service.DeleteHistory).toHaveBeenCalledWith(7);
});

test('running tasks: pause, resume, and a queued one can be withdrawn', async () => {
  const user = userEvent.setup();
  Service.Timeline.mockResolvedValue([
    item({ key: 't1', kind: 'task', id: 1, direction: 'out', status: 'sending', summary: '视频.mp4', bytesDone: 25, bytesTotal: 100 }),
    item({ key: 't2', kind: 'task', id: 2, direction: 'out', status: 'pending', summary: '文本「稍后」', peerName: '台式机' }),
    item({ key: 't3', kind: 'task', id: 3, direction: 'out', status: 'sending', paused: true, summary: 'b.zip' }),
  ]);
  renderWithNav(<ConversationView />, { conversation: 'DESK' });
  const sending = await screen.findByTestId('item-t1');
  expect(within(sending).getByText(/正在发送 25%/)).toBeInTheDocument();
  expect(within(sending).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '25');
  await user.click(within(sending).getByRole('button', { name: '暂停' }));
  expect(Service.PauseSend).toHaveBeenCalledWith(1);
  const queued = screen.getByTestId('item-t2');
  expect(within(queued).getByText('等「台式机」上线后送达')).toBeInTheDocument();
  await user.click(within(queued).getByRole('button', { name: '撤回' }));
  expect(Service.CancelSend).toHaveBeenCalledWith(2);
  await user.click(within(screen.getByTestId('item-t3')).getByRole('button', { name: '继续' }));
  expect(Service.ResumeSend).toHaveBeenCalledWith(3);
});

test('an interrupted task can be retried right away instead of waiting', async () => {
  const user = userEvent.setup();
  Service.Timeline.mockResolvedValue([
    item({ key: 't4', kind: 'task', id: 4, direction: 'out', status: 'interrupted', summary: '报告.pdf', error: '连接中断' }),
    item({ key: 't5', kind: 'task', id: 5, direction: 'out', status: 'interrupted', paused: true, summary: 'c.zip' }),
  ]);
  renderWithNav(<ConversationView />, { conversation: 'DESK' });
  const failed = await screen.findByTestId('item-t4');
  expect(within(failed).getByText(/中断，稍后重试（连接中断）/)).toBeInTheDocument();
  await user.click(within(failed).getByRole('button', { name: '立即重试' }));
  expect(Service.RetrySend).toHaveBeenCalledWith(4);
  expect(within(screen.getByTestId('item-t5')).queryByRole('button', { name: '立即重试' })).not.toBeInTheDocument();
});

test('pending confirmations show in their own conversation and can be accepted', async () => {
  const user = userEvent.setup();
  Service.Inbox.mockResolvedValue([
    { id: 4, peerName: '台式机', summary: 'a.zip（3 MB）', total: 3, expiresAt: Date.now() + 60_000 },
    { id: 5, peerName: '手机', summary: 'b.jpg（1 MB）', total: 1, expiresAt: Date.now() + 60_000 },
  ]);
  renderWithNav(<ConversationView />, { conversation: 'DESK' });
  const card = await screen.findByRole('group', { name: '来自「台式机」的待确认接收' });
  expect(screen.queryByRole('group', { name: '来自「手机」的待确认接收' })).not.toBeInTheDocument();
  await user.click(within(card).getByRole('button', { name: '接受' }));
  expect(Service.DecideInbox).toHaveBeenCalledWith(4, true);
});

test('the filter keeps only files or only texts', async () => {
  const user = userEvent.setup();
  Service.Timeline.mockResolvedValue([
    item({ key: 'm1', id: 1, texts: ['一段文字'] }),
    item({ key: 'm2', id: 2, files: [{ name: 'a.pdf', size: 1, exists: true }], fileCount: 1 }),
  ]);
  renderWithNav(<ConversationView />, { conversation: 'DESK' });
  await screen.findByText('一段文字');
  await user.click(screen.getByRole('button', { name: '筛选消息' }));
  await user.click(screen.getByRole('menuitem', { name: '只看文件' }));
  expect(screen.queryByText('一段文字')).not.toBeInTheDocument();
  expect(screen.getByText('a.pdf')).toBeInTheDocument();
});

test('a search jump highlights the entry for a moment, and does not later scroll away from it', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const scrollIntoView = vi.fn();
  const original = Element.prototype.scrollIntoView;
  Element.prototype.scrollIntoView = scrollIntoView;
  Service.Timeline.mockResolvedValue([item({ key: 'm9', id: 9, texts: ['找到我'] })]);
  const { NavProvider } = await import('../../store/nav');
  const { useNav } = await import('../../store/nav');
  let open: (k: string, h?: number) => void = () => {};
  function Grab() {
    open = useNav().openConversation;
    return null;
  }
  const { renderWithProviders } = await import('../../test/render');
  renderWithProviders(
    <NavProvider initial={{ conversation: 'PHON' }}>
      <Grab />
      <ConversationView />
    </NavProvider>,
  );
  act(() => open('DESK', 9));
  expect(await screen.findByTestId('item-m9')).toHaveAttribute('data-highlighted', 'true');
  scrollIntoView.mockClear();
  await act(async () => vi.advanceTimersByTime(3000));
  expect(screen.getByTestId('item-m9')).not.toHaveAttribute('data-highlighted');
  // 高亮过期后（target 从编号变回 null）不该把用户拉回到消息流底部：items 没有变，不是真的新消息。
  expect(scrollIntoView).not.toHaveBeenCalledWith({ block: 'end' });
  Element.prototype.scrollIntoView = original;
  vi.useRealTimers();
});
