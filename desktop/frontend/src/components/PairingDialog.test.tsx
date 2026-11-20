import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Clipboard } from '@wailsio/runtime';
import { PairingDialog } from './PairingDialog';
import { NavProvider, useNav } from '../store/nav';
import { renderWithProviders } from '../test/render';
import { device, self } from '../test/fixtures';
import { Service, resetPortideMock } from '../test/portideMock';

vi.mock('../api/portide', () => import('../test/portideMock'));

const waiting = { id: 7, code: '1234-5678', expiresAt: Date.now() + 300_000, status: 'waiting', mode: '', joinerName: '', error: '' };
let nav: ReturnType<typeof useNav>;
function Probe() {
  nav = useNav();
  return <span>会话：{nav.conversation}</span>;
}
function renderOpen() {
  const r = renderWithProviders(
    <NavProvider initial={{ conversation: '' }}>
      <Probe />
      <PairingDialog />
    </NavProvider>,
  );
  act(() => nav.setPairingOpen(true));
  return r;
}

beforeEach(() => {
  resetPortideMock();
  Service.StartPairing.mockResolvedValue(waiting);
  Service.PairingStatus.mockResolvedValue(waiting);
  Service.Devices.mockResolvedValue([self, device({ keyId: 'WORK', name: '工作本' })]);
});

test('waiting: a big code in two groups that can be copied, steps and a countdown; closing cancels', async () => {
  const user = userEvent.setup();
  renderOpen();
  const code = await screen.findByLabelText('配对码 1234-5678');
  expect(within(code).getByText('1234')).toBeInTheDocument();
  expect(within(code).getByText('5678')).toBeInTheDocument();
  expect(screen.getByText(/内有效/)).toBeInTheDocument();
  expect(screen.getByText('正在等待新设备…')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '复制配对码' }));
  expect(Clipboard.SetText).toHaveBeenCalledWith('12345678');
  await user.keyboard('{Escape}');
  expect(Service.CancelPairing).toHaveBeenCalledWith(7);
});

test('success offers to message the new device', async () => {
  const user = userEvent.setup();
  Service.PairingStatus.mockResolvedValue({ ...waiting, status: 'done', joinerName: '工作本' });
  renderOpen();
  expect(await screen.findByText('「工作本」已加入设备圈')).toBeInTheDocument();
  await user.click(await screen.findByRole('button', { name: '给它发消息' }));
  expect(screen.getByText('会话：WORK')).toBeInTheDocument();
  expect(Service.CancelPairing).not.toHaveBeenCalled();
});

test('failed or expired codes can be regenerated', async () => {
  const user = userEvent.setup();
  Service.PairingStatus.mockResolvedValue({ ...waiting, status: 'expired' });
  renderOpen();
  expect(await screen.findByText('配对码已过期')).toBeInTheDocument();
  Service.StartPairing.mockResolvedValue({ ...waiting, id: 8 });
  Service.PairingStatus.mockResolvedValue({ ...waiting, id: 8 });
  await user.click(screen.getByRole('button', { name: '重新生成' }));
  await waitFor(() => expect(Service.StartPairing).toHaveBeenCalledTimes(2));
  expect(await screen.findByText('正在等待新设备…')).toBeInTheDocument();
});

test('a pairing that cannot start explains why', async () => {
  Service.StartPairing.mockRejectedValue(new Error('Portide 还没有启动'));
  renderOpen();
  expect(await screen.findByText('无法开始配对')).toBeInTheDocument();
  expect(screen.getByText('Portide 还没有启动')).toBeInTheDocument();
});

test('two tabs: show the code (default) and enter a code; switching to enter cancels my pairing', async () => {
  const user = userEvent.setup();
  renderOpen();
  expect(await screen.findByLabelText('配对码 1234-5678')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '输入配对码' }));
  expect(Service.CancelPairing).toHaveBeenCalledWith(7);
  expect(await screen.findByRole('textbox', { name: '配对码' })).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '显示配对码' }));
  await waitFor(() => expect(Service.StartPairing).toHaveBeenCalledTimes(2));
  expect(await screen.findByLabelText('配对码 1234-5678')).toBeInTheDocument();
});

test('entering a code with other devices explains the merge first', async () => {
  const user = userEvent.setup();
  renderOpen();
  await user.click(await screen.findByRole('button', { name: '输入配对码' }));
  expect(
    await screen.findByText('如果对方只有它自己，它会加入这个设备圈；如果对方也有其他设备，两个设备圈会合并：这台设备所在的设备圈（还有「工作本」）并入对方的。历史记录和设置都保留。'),
  ).toBeInTheDocument();
});

test('entering a code with several other devices names the first and counts them', async () => {
  const user = userEvent.setup();
  Service.Devices.mockResolvedValue([self, device({ keyId: 'WORK', name: '工作本' }), device({ keyId: 'W', name: 'Windows666' })]);
  renderOpen();
  await user.click(await screen.findByRole('button', { name: '输入配对码' }));
  expect(
    await screen.findByText('如果对方只有它自己，它会加入这个设备圈；如果对方也有其他设备，两个设备圈会合并：这台设备所在的设备圈（还有「工作本」等 2 台）并入对方的。历史记录和设置都保留。'),
  ).toBeInTheDocument();
});

test('opening on the enter tab never starts a pairing', async () => {
  renderWithProviders(
    <NavProvider initial={{ conversation: '' }}>
      <Probe />
      <PairingDialog />
    </NavProvider>,
  );
  act(() => nav.setPairingOpen(true, 'enter'));
  expect(await screen.findByRole('textbox', { name: '配对码' })).toBeInTheDocument();
  expect(Service.StartPairing).not.toHaveBeenCalled();
});

async function submitCode(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: '输入配对码' }));
  await user.type(await screen.findByRole('textbox', { name: '配对码' }), '12345678');
  await user.click(screen.getByRole('button', { name: '加入' }));
}

test('join result: joined the other circle', async () => {
  const user = userEvent.setup();
  Service.Join.mockResolvedValue({ mode: 'join', peerName: '电脑', devices: 3 });
  renderOpen();
  await submitCode(user);
  expect(await screen.findByText('已加入「电脑」的设备圈')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: '完成' })).toBeInTheDocument();
});

test('join result: the other device joined mine', async () => {
  const user = userEvent.setup();
  Service.Join.mockResolvedValue({ mode: 'adopt', peerName: '手机', devices: 3 });
  renderOpen();
  await submitCode(user);
  expect(await screen.findByText('「手机」已加入设备圈')).toBeInTheDocument();
});

test('join errors stay on the form', async () => {
  const user = userEvent.setup();
  Service.Join.mockRejectedValue(new Error('配对码不正确'));
  renderOpen();
  await submitCode(user);
  expect(await screen.findByText('配对码不正确')).toBeInTheDocument();
  expect(screen.getByRole('textbox', { name: '配对码' })).toBeInTheDocument();
});

test('inviter side: adopted shows that this device joined the other circle', async () => {
  Service.PairingStatus.mockResolvedValue({ ...waiting, status: 'done', mode: 'adopt', joinerName: '电脑' });
  Service.Devices.mockResolvedValue([self, device({ keyId: 'PC', name: '电脑' }), device({ keyId: 'W', name: 'Windows666' })]);
  renderOpen();
  expect(await screen.findByText('已加入「电脑」的设备圈，现在可以和「电脑」「Windows666」互传了')).toBeInTheDocument();
});

test('merge result: title, waiting and outdated lines, and the duplicate-name hint', async () => {
  const user = userEvent.setup();
  Service.Devices.mockResolvedValue([self, device({ keyId: 'A', name: '手机' }), device({ keyId: 'B', name: '手机' })]);
  Service.Join.mockResolvedValue({ mode: 'merge', peerName: '电脑', devices: 5, waiting: ['平板', '旧机', '平板'], outdated: ['旧机'] });
  renderOpen();
  await submitCode(user);
  expect(await screen.findByText('已合并，设备圈里现在有 5 台设备')).toBeInTheDocument();
  expect(screen.getAllByText('「平板」还没跟过来，它和这里任意一台合并过来的设备连上时会自动跟过来（30 天内）')).toHaveLength(2);
  expect(screen.getAllByText('「旧机」的 Portide 版本太旧，需要先升级才能跟过来')).toHaveLength(1);
  expect(await screen.findByText('有设备重名，可以在设备详情里改名')).toBeInTheDocument();
});

test('merge result: no duplicate-name hint without duplicates', async () => {
  const user = userEvent.setup();
  Service.Join.mockResolvedValue({ mode: 'merge', peerName: '电脑', devices: 3, waiting: [], outdated: [] });
  renderOpen();
  await submitCode(user);
  expect(await screen.findByText('已合并，设备圈里现在有 3 台设备')).toBeInTheDocument();
  expect(screen.queryByText('有设备重名，可以在设备详情里改名')).not.toBeInTheDocument();
});

test('inviter result for a merge', async () => {
  Service.PairingStatus.mockResolvedValue({ ...waiting, status: 'done', mode: 'merge', joinerName: '工作本', brought: 2 });
  renderOpen();
  expect(await screen.findByText('已合并：「工作本」和它设备圈里的 2 台设备加入了设备圈')).toBeInTheDocument();
});

test('merge result: every same-name outdated device gets the upgrade line', async () => {
  const user = userEvent.setup();
  Service.Join.mockResolvedValue({ mode: 'merge', peerName: '电脑', devices: 4, waiting: ['旧机', '旧机'], outdated: ['旧机', '旧机'] });
  renderOpen();
  await submitCode(user);
  expect(await screen.findAllByText('「旧机」的 Portide 版本太旧，需要先升级才能跟过来')).toHaveLength(2);
  expect(screen.queryByText(/还没跟过来/)).not.toBeInTheDocument();
});
