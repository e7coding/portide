import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DeviceList } from './DeviceList';
import { NavProvider, useNav } from '../../store/nav';
import { renderWithNav, renderWithProviders } from '../../test/render';
import { device, self } from '../../test/fixtures';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

beforeEach(() => {
  resetPortideMock();
  Service.Devices.mockResolvedValue([
    self,
    device(),
    device({ keyId: 'PHON', name: '手机', icon: 'smartphone', online: false, lastSeen: Date.now() - 2 * 3600_000 }),
    device({ keyId: 'TAB', name: '平板', icon: 'tablet', online: true, path: 'relay', addr: '' }),
  ]);
});

test('this device and the circle are grouped, with status lines; this device is selected by default', async () => {
  renderWithNav(<DeviceList />);
  const me = await screen.findByRole('button', { name: '笔记本' });
  expect(me).toHaveAttribute('aria-current', 'true');
  expect(within(me).getByText('本机')).toBeInTheDocument();
  expect(screen.getByText('设备圈 · 3 台')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '台式机' })).getByText('在线 · 局域网')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '手机' })).getByText('离线 · 2 小时前')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '平板' })).getByText('在线 · 经中继')).toBeInTheDocument();
});

test('selecting a device and opening "add device"', async () => {
  const user = userEvent.setup();
  let pairing = false;
  function Probe() {
    pairing = useNav().pairingOpen;
    return null;
  }
  renderWithProviders(
    <NavProvider>
      <Probe />
      <DeviceList />
    </NavProvider>,
  );
  await user.click(await screen.findByRole('button', { name: '手机' }));
  expect(screen.getByRole('button', { name: '手机' })).toHaveAttribute('aria-current', 'true');
  await user.click(screen.getByRole('button', { name: '添加设备' }));
  expect(pairing).toBe(true);
});

test('devices in a move state show the move status instead of offline', async () => {
  Service.Devices.mockResolvedValue([
    self,
    device({ keyId: 'W', name: '甲', online: false, move: 'waiting', moveUntil: 1 }),
    device({ keyId: 'O', name: '乙', online: false, move: 'outdated' }),
    device({ keyId: 'L', name: '丙', online: false, move: 'left' }),
    device({ keyId: 'E', name: '丁', online: false, move: 'elsewhere' }),
  ]);
  renderWithNav(<DeviceList />);
  expect(await within(await screen.findByRole('button', { name: '甲' })).findByText('等待合并')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '乙' })).getByText('需要升级')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '丙' })).getByText('没有跟过来')).toBeInTheDocument();
  expect(within(screen.getByRole('button', { name: '丁' })).getByText('去了别的设备圈')).toBeInTheDocument();
  expect(screen.queryByText(/离线/)).not.toBeInTheDocument();
});
