import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../../App';
import { renderWithProviders, runningState } from '../../test/render';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

const base = { ...runningState, name: '', keyId: '' };

beforeEach(() => resetPortideMock());

test('first start: welcome, set a passphrase (typed twice), pick a suggested name, and Init starts a solo circle', async () => {
  const user = userEvent.setup();
  Service.State.mockResolvedValue({ ...base, phase: 'locked', firstPassphrase: true });
  renderWithProviders(<App />);

  expect(await screen.findByText('Portide 摆渡')).toBeInTheDocument();
  expect(screen.getByText('端到端加密')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '开始使用' }));

  expect(screen.getByLabelText('第 1 步，共 2 步')).toBeInTheDocument();
  await user.type(screen.getByLabelText('口令'), 'pw');
  await user.type(screen.getByLabelText('再输入一次'), 'px');
  expect(screen.getByText('两次输入的口令不一样')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: '设置并继续' })).toBeDisabled();
  await user.clear(screen.getByLabelText('再输入一次'));
  await user.type(screen.getByLabelText('再输入一次'), 'pw');

  Service.State.mockResolvedValue({ ...base, phase: 'setup', needsName: true, defaultName: 'LAPTOP-8F2K1', nameSuggestions: ['joy 的笔记本', 'joy 的台式机', 'LAPTOP-8F2K1'] });
  await user.click(screen.getByRole('button', { name: '设置并继续' }));
  expect(Service.Unlock).toHaveBeenCalledWith('pw');

  const name = await screen.findByLabelText('设备名');
  expect(screen.getByLabelText('第 2 步，共 2 步')).toBeInTheDocument();
  expect(name).toHaveValue('joy 的笔记本');
  await user.click(screen.getByRole('button', { name: 'joy 的台式机' }));
  expect(name).toHaveValue('joy 的台式机');
  Service.State.mockResolvedValue({ ...base, phase: 'setup', name: 'joy 的台式机' });
  await user.click(screen.getByRole('button', { name: '下一步' }));
  expect(Service.Init).toHaveBeenCalledWith('joy 的台式机');

  expect(screen.queryByText('新建设备圈')).not.toBeInTheDocument();
});

test('a returning user only sees "enter passphrase"; a wrong one is shown', async () => {
  const user = userEvent.setup();
  Service.State.mockResolvedValue({ ...base, phase: 'locked' });
  Service.Unlock.mockRejectedValue({ message: '口令不对' });
  renderWithProviders(<App />);
  expect(await screen.findByRole('heading', { name: '输入口令' })).toBeInTheDocument();
  expect(screen.queryByText('开始使用')).not.toBeInTheDocument();
  await user.type(screen.getByLabelText('口令'), 'bad');
  await user.click(screen.getByRole('button', { name: '解锁' }));
  expect(await screen.findByText('口令不对')).toBeInTheDocument();
});
