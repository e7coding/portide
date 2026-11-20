import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Clipboard } from '@wailsio/runtime';
import { SettingsNav } from './SettingsNav';
import { SettingsPage } from './SettingsPage';
import { friendlyPath } from './parts';
import { renderWithNav, runningState } from '../../test/render';
import { Platform, Service, resetPortideMock } from '../../test/portideMock';
import type { SettingsSection } from '../../store/nav';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

const MB = 1024 * 1024;
const settings = { saveDir: '/home/me/Downloads/Portide', receiveMode: 'auto', askAbove: 0, historyDays: 0, queueDays: 7, notify: true, listenPort: 7443, actualPort: 7443 };

const open = (section: SettingsSection) =>
  renderWithNav(
    <>
      <SettingsNav />
      <SettingsPage state={runningState} />
    </>,
    { page: 'settings', section },
  );

beforeEach(() => {
  resetPortideMock();
  localStorage.clear();
  Service.Settings.mockResolvedValue(settings);
  Service.CanRename.mockResolvedValue({ allowed: true, missing: [] });
  Service.Relay.mockResolvedValue({ relays: [], changedBy: '', changedAt: 0, outdated: [] });
  Service.LocalAddresses.mockResolvedValue(['192.168.1.5:7443', '[fd00::5]:7443']);
});

test('friendly save paths', () => {
  expect(friendlyPath('/home/me/Downloads/Portide')).toBe('下载 › Portide');
  expect(friendlyPath('C:\\Users\\me\\Documents\\收到')).toBe('文档 › 收到');
});

test('the category list switches sections', async () => {
  const user = userEvent.setup();
  open('general');
  expect(screen.getByRole('heading', { name: '通用', level: 1 })).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '存储与历史' }));
  expect(screen.getByRole('heading', { name: '存储与历史', level: 1 })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: '存储与历史' })).toHaveAttribute('aria-current', 'true');
});

test('general: rename this device; close-to-tray is saved with a toast', async () => {
  const user = userEvent.setup();
  open('general');
  const box = await screen.findByRole('textbox', { name: '本机名字' });
  await waitFor(() => expect(box).toBeEnabled());
  await user.clear(box);
  await user.type(box, '新笔记本');
  await user.click(screen.getByRole('button', { name: '改名' }));
  expect(Service.RenameDevice).toHaveBeenCalledWith(runningState.keyId, '新笔记本');
  await user.click(await screen.findByRole('switch', { name: '关闭窗口时缩到托盘' }));
  expect(Platform.SetCloseToTray).toHaveBeenCalledWith(false);
  expect(await screen.findByRole('status')).toHaveTextContent('已保存');
});

test('general: without a tray the close-to-tray switch is off limits and says why', async () => {
  Platform.Prefs.mockResolvedValue({ closeToTray: true, hideHint: false, hasTray: false });
  open('general');
  expect(await screen.findByText('这台电脑的桌面没有托盘，关闭窗口即退出')).toBeInTheDocument();
  expect(screen.getByRole('switch', { name: '关闭窗口时缩到托盘' })).toBeDisabled();
});

test('receive: friendly folder, open and change it; modes; the threshold only applies to "large files"', async () => {
  const user = userEvent.setup();
  open('receive');
  expect(await screen.findByText('下载 › Portide')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '打开' }));
  expect(Service.OpenSaveDir).toHaveBeenCalled();
  Platform.PickFolder.mockResolvedValue('/tmp/x');
  await user.click(screen.getByRole('button', { name: '更改' }));
  await waitFor(() => expect(Service.SetSaveDir).toHaveBeenCalledWith('/tmp/x'));

  const threshold = screen.getByRole('group', { name: '大文件阈值' });
  expect(within(threshold).getByRole('button', { name: '100 MB' })).toBeDisabled();
  await user.click(screen.getByRole('button', { name: '大文件确认' }));
  expect(Service.SetReceivePolicy).toHaveBeenCalledWith('ask-above', 100 * MB);
});

test('receive: a custom threshold in MB', async () => {
  const user = userEvent.setup();
  Service.Settings.mockResolvedValue({ ...settings, receiveMode: 'ask-above', askAbove: 100 * MB });
  open('receive');
  await user.click(await screen.findByRole('button', { name: '自定义' }));
  const box = screen.getByRole('spinbutton', { name: '大文件阈值（MB）' });
  await user.clear(box);
  await user.type(box, '250{Enter}');
  expect(Service.SetReceivePolicy).toHaveBeenCalledWith('ask-above', 250 * MB);
  await user.clear(box);
  await user.type(box, '0{Enter}');
  expect(await screen.findByRole('alert')).toHaveTextContent('请输入 1 到 1048576 之间的整数');
});

test('notifications, autostart and the hide hint can be switched; a failure is shown in red', async () => {
  const user = userEvent.setup();
  open('notify');
  await user.click(await screen.findByRole('switch', { name: '系统通知' }));
  expect(Service.SetNotify).toHaveBeenCalledWith(false);
  await user.click(screen.getByRole('switch', { name: '每次缩到托盘时提示' }));
  expect(Platform.SetHideHint).toHaveBeenCalledWith(true);
  Platform.SetAutostart.mockRejectedValue(new Error('这个平台不支持开机时启动'));
  await user.click(screen.getByRole('switch', { name: '开机时启动' }));
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('这个平台不支持开机时启动'));
});

test('network: add a relay, choose an automatic port, copy a local address', async () => {
  const user = userEvent.setup();
  open('network');
  await user.type(await screen.findByRole('textbox', { name: '中继配置串' }), 'portide-relay://x');
  await user.click(screen.getByRole('button', { name: '添加中继' }));
  expect(Service.AddRelay).toHaveBeenCalledWith('portide-relay://x');
  expect(screen.getByText('现在监听 7443；改了之后重启 Portide 生效')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '自动选择' }));
  expect(Service.SetListenPort).toHaveBeenCalledWith(0);
  await user.click(screen.getByRole('button', { name: '复制 192.168.1.5:7443' }));
  expect(Clipboard.SetText).toHaveBeenCalledWith('192.168.1.5:7443');
});

const two = {
  relays: [
    { id: 'aa11', addr: 'relay.example.com:7443', fingerprint: 'ab12cd34ef56ab12', status: '已连接' },
    { id: 'bb22', addr: '1.2.3.4:7443', fingerprint: '99887766aabbccdd', status: '中继拒绝了登记' },
  ],
  changedBy: '笔记本',
  changedAt: new Date(2026, 0, 5, 9, 7).getTime(),
  outdated: ['旧手机'],
};

test('network: lists every relay with its status, the sync note and outdated warnings', async () => {
  Service.Relay.mockResolvedValue(two);
  open('network');
  expect(await screen.findByText('relay.example.com:7443')).toBeInTheDocument();
  expect(screen.getByText('1.2.3.4:7443')).toBeInTheDocument();
  expect(screen.getByText('已连接')).toBeInTheDocument();
  expect(screen.getByText('中继拒绝了登记')).toBeInTheDocument();
  expect(screen.getByText('中继设置会自动同步到设备圈里的所有设备 · 最后由「笔记本」在 01-05 09:07 修改')).toBeInTheDocument();
  expect(screen.getByText('「旧手机」的 Portide 版本较旧，收不到中继设置的更改，需要在它上面手动设置')).toBeInTheDocument();
});

test('network: without settings only the first half of the note shows', async () => {
  open('network');
  expect(await screen.findByText('中继设置会自动同步到设备圈里的所有设备')).toBeInTheDocument();
});

test('network: adding says added for a new relay and updated for a known one', async () => {
  const user = userEvent.setup();
  Service.Relay.mockResolvedValueOnce(two).mockResolvedValueOnce(two).mockResolvedValue({ ...two, relays: [...two.relays, { id: 'cc33', addr: 'n:1', fingerprint: 'x', status: '已连接' }] });
  open('network');
  await screen.findByText('1.2.3.4:7443');
  await user.type(screen.getByRole('textbox', { name: '中继配置串' }), 'portide-relay://new');
  await user.click(screen.getByRole('button', { name: '添加中继' }));
  expect(Service.AddRelay).toHaveBeenCalledWith('portide-relay://new');
  expect(await screen.findByText('已添加中继，几秒内就会连接')).toBeInTheDocument();
});

test('network: adding a relay that is already listed reports an update', async () => {
  const user = userEvent.setup();
  Service.Relay.mockResolvedValue(two);
  open('network');
  await screen.findByText('1.2.3.4:7443');
  await user.type(screen.getByRole('textbox', { name: '中继配置串' }), 'portide-relay://same');
  await user.click(screen.getByRole('button', { name: '添加中继' }));
  expect(await screen.findByText('已更新中继')).toBeInTheDocument();
});

test('network: a failing lookup after a successful add still reports success and clears the field', async () => {
  const user = userEvent.setup();
  Service.Relay.mockResolvedValue(two);
  open('network');
  await screen.findByText('1.2.3.4:7443');
  let added = false;
  Service.AddRelay.mockImplementation(async () => {
    added = true;
  });
  Service.Relay.mockImplementation(() => (added ? Promise.reject(new Error('boom')) : Promise.resolve(two)));
  const box = screen.getByRole('textbox', { name: '中继配置串' });
  await user.type(box, 'portide-relay://new');
  await user.click(screen.getByRole('button', { name: '添加中继' }));
  expect(await screen.findByText('已添加中继，几秒内就会连接')).toBeInTheDocument();
  expect(box).toHaveValue('');
});

test('network: removing a relay is confirmed first and passes its id', async () => {
  const user = userEvent.setup();
  Service.Relay.mockResolvedValue(two);
  open('network');
  await user.click(await screen.findByRole('button', { name: '删除 1.2.3.4:7443' }));
  expect(Service.RemoveRelay).not.toHaveBeenCalled();
  const dlg = screen.getByRole('dialog');
  expect(within(dlg).getByText('删除中继 1.2.3.4:7443？')).toBeInTheDocument();
  await user.click(within(dlg).getByRole('button', { name: '删除' }));
  expect(Service.RemoveRelay).toHaveBeenCalledWith('bb22');
});

test('storage: history keeping, queue days, clearing (confirmed) and the data folder', async () => {
  const user = userEvent.setup();
  Service.ClearHistory.mockResolvedValue(12);
  open('storage');
  await user.click(await screen.findByRole('button', { name: '90 天' }));
  expect(Service.SetHistoryDays).toHaveBeenCalledWith(90);
  await user.click(within(screen.getByRole('group', { name: '离线队列有效期' })).getByRole('button', { name: '30 天' }));
  expect(Service.SetQueueDays).toHaveBeenCalledWith(30);
  expect(screen.getByText(runningState.dataDir)).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '清空' }));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: '清空' }));
  expect(await screen.findByRole('status')).toHaveTextContent('已清空 12 条历史记录');
});

test('appearance is remembered on this computer; about shows the version and licenses', async () => {
  const user = userEvent.setup();
  open('appearance');
  await user.click(screen.getByRole('radio', { name: '深色' }));
  expect(localStorage.getItem('portide.theme')).toBe('dark');
  expect(screen.getByRole('radio', { name: '深色' })).toHaveAttribute('aria-checked', 'true');
  await user.click(screen.getByRole('button', { name: '关于' }));
  expect(screen.getByText(/私人文件传输 · 版本/)).toBeInTheDocument();
  expect(screen.getByText('quic-go（MIT）')).toBeInTheDocument();
});

test('about: a local build without a release repo shows no update rows', async () => {
  open('about');
  expect(await screen.findByText(/私人文件传输 · 版本/)).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '立即检查' })).not.toBeInTheDocument();
});

test('about: a newer release offers a download; check now and the automatic switch', async () => {
  const user = userEvent.setup();
  const prefs = { closeToTray: true, hideHint: false, hasTray: true, language: '', uiLanguage: 'zh', updateCheck: true, canUpdate: true };
  Platform.Prefs.mockResolvedValue(prefs);
  const newer = { current: '0.5.0', latest: '0.6.0', url: 'https://github.com/acme/portide/releases/tag/v0.6.0', newer: true, checkedAt: new Date().toISOString() };
  Platform.CheckUpdate.mockResolvedValue(newer);
  open('about');
  expect(await screen.findByText('有新版本 0.6.0')).toBeInTheDocument();
  expect(Platform.CheckUpdate).toHaveBeenCalledWith(false);
  await user.click(screen.getByRole('button', { name: '下载' }));
  expect(Platform.OpenURL).toHaveBeenCalledWith(newer.url);

  Platform.CheckUpdate.mockResolvedValue({ ...newer, latest: '0.5.0', newer: false });
  await user.click(screen.getByRole('button', { name: '立即检查' }));
  expect(Platform.CheckUpdate).toHaveBeenCalledWith(true);
  expect(await screen.findByText('已是最新版本 · 刚刚检查')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '下载' })).not.toBeInTheDocument();

  await user.click(screen.getByRole('switch', { name: '自动检查更新' }));
  expect(Platform.SetUpdateCheck).toHaveBeenCalledWith(false);
});
