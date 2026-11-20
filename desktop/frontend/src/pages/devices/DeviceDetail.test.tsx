import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Clipboard } from '@wailsio/runtime';
import { DeviceDetail, groupFingerprint } from './DeviceDetail';
import { renderWithNav } from '../../test/render';
import { device, self } from '../../test/fixtures';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

const KEY = 'A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E8F90';
const desk = device({ keyId: KEY, joinedAt: new Date(2026, 9, 20, 14, 3).getTime() });

beforeEach(() => {
  resetPortideMock();
  Service.Devices.mockResolvedValue([self, desk]);
  Service.CanRename.mockResolvedValue({ allowed: true, missing: [] });
  Service.RemoveDevice.mockResolvedValue({ needsForce: false, relayWarning: false });
});

test('fingerprints are shown in groups of four', () => {
  expect(groupFingerprint('ABCDEFGH12')).toBe('ABCD EFGH 12');
});

test('details: status, connection, address, join time and a copyable fingerprint', async () => {
  const user = userEvent.setup();
  renderWithNav(<DeviceDetail />, { device: KEY });
  expect(await screen.findByRole('heading', { name: '台式机' })).toBeInTheDocument();
  const info = screen.getByRole('region', { name: '信息' });
  expect(within(info).getByText('192.168.1.5:7443')).toBeInTheDocument();
  expect(within(info).getByText('10-20 14:03')).toBeInTheDocument();
  expect(within(info).getByText(groupFingerprint(KEY))).toBeInTheDocument();
  await user.click(within(info).getByRole('button', { name: '复制设备指纹' }));
  expect(Clipboard.SetText).toHaveBeenCalledWith(KEY);
});

test('rename with the pencil; Enter saves', async () => {
  const user = userEvent.setup();
  renderWithNav(<DeviceDetail />, { device: KEY });
  await user.click(await screen.findByRole('button', { name: '改名' }));
  const box = screen.getByRole('textbox', { name: '设备名' });
  await user.clear(box);
  await user.type(box, '办公室电脑{Enter}');
  expect(Service.RenameDevice).toHaveBeenCalledWith(KEY, '办公室电脑');
  expect(await screen.findByRole('status')).toHaveTextContent('已改名');
});

test('Enter cannot save an empty device name', async () => {
  const user = userEvent.setup();
  renderWithNav(<DeviceDetail />, { device: KEY });
  await user.click(await screen.findByRole('button', { name: '改名' }));
  const box = screen.getByRole('textbox', { name: '设备名' });
  await user.clear(box);
  await user.keyboard('{Enter}');
  expect(Service.RenameDevice).not.toHaveBeenCalled();
});

test('renaming is blocked until every device supports it', async () => {
  Service.CanRename.mockResolvedValue({ allowed: false, missing: ['手机'] });
  renderWithNav(<DeviceDetail />, { device: KEY });
  expect(await screen.findByText('「手机」需要先升级到新版本，才能改名')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: '改名' })).toBeDisabled();
});

test('the icon picker saves locally', async () => {
  const user = userEvent.setup();
  renderWithNav(<DeviceDetail />, { device: KEY });
  await user.click(await screen.findByRole('button', { name: '平板' }));
  expect(Service.SetDeviceIcon).toHaveBeenCalledWith(KEY, 'tablet');
});

test('removing asks first, and asks again when the circle may be stale', async () => {
  const user = userEvent.setup();
  Service.RemoveDevice.mockResolvedValueOnce({ needsForce: true, relayWarning: false });
  renderWithNav(<DeviceDetail />, { device: KEY });
  await user.click(await screen.findByRole('button', { name: '移出设备圈' }));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: '移除' }));
  expect(Service.RemoveDevice).toHaveBeenLastCalledWith(KEY, false);
  expect(await screen.findByText(/暂时确认不了设备圈是否已同步/)).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '仍要移除' }));
  expect(Service.RemoveDevice).toHaveBeenLastCalledWith(KEY, true);
  expect(await screen.findByRole('status')).toHaveTextContent('已把「台式机」移出设备圈');
});

test('this device: no removal and no message button, but a version', async () => {
  renderWithNav(<DeviceDetail />);
  expect(await screen.findByRole('heading', { name: '笔记本' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '移出设备圈' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '发消息' })).not.toBeInTheDocument();
  expect(screen.getByText(/^Portide /)).toBeInTheDocument();
});

test.each([
  ['waiting', '它和这里任意一台合并过来的设备连上后会自动加入；30 天后不再等待（11 月 27 日）'],
  ['outdated', '它的 Portide 版本太旧，升级之后和这里任意一台合并过来的设备连上时会自动加入'],
  ['left', '它没有跟过来（已经等了 30 天）。可以把它移出设备圈，或者在它上面再输入一次这个设备圈的配对码'],
  ['elsewhere', '原来设备圈里的部分设备合并到了别的设备圈，把那个设备圈和这里再合并一次即可'],
])('move state %s explains itself and does not say offline', async (move, text) => {
  Service.Devices.mockResolvedValue([self, device({ keyId: KEY, online: false, move, moveUntil: new Date(2026, 10, 27, 12).getTime() })]);
  renderWithNav(<DeviceDetail />, { device: KEY });
  expect(await screen.findByText(text)).toBeInTheDocument();
  expect(screen.queryByText('离线')).not.toBeInTheDocument();
});

test('a relayed device shows the relay address, or just 经中继 when unknown', async () => {
  Service.Devices.mockResolvedValue([self, device({ keyId: KEY, path: 'relay', addr: '1.2.3.4:7443' })]);
  const { unmount } = renderWithNav(<DeviceDetail />, { device: KEY });
  await screen.findByRole('heading', { name: '台式机' });
  expect(within(screen.getByRole('region', { name: '信息' })).getByText('经中继（1.2.3.4:7443）')).toBeInTheDocument();
  unmount();
  Service.Devices.mockResolvedValue([self, device({ keyId: KEY, path: 'relay', addr: '' })]);
  renderWithNav(<DeviceDetail />, { device: KEY });
  await screen.findByRole('heading', { name: '台式机' });
  expect(within(screen.getByRole('region', { name: '信息' })).getAllByText('经中继')).toHaveLength(2); // 连接方式 + 地址
});

test('removing a device with a relay tells how to change the token', async () => {
  const user = userEvent.setup();
  Service.RemoveDevice.mockResolvedValue({ needsForce: false, relayWarning: true });
  renderWithNav(<DeviceDetail />, { device: KEY });
  await user.click(await screen.findByRole('button', { name: /移出设备圈/ }));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: '移除' }));
  expect(await screen.findByRole('status')).toHaveTextContent('在任意一台设备上更新中继，会自动同步到其余设备');
});
