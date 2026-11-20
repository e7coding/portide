import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { JoinCodeForm, formatCodeInput } from './JoinCodeForm';
import { renderWithProviders } from '../test/render';
import { Service, resetPortideMock } from '../test/portideMock';

vi.mock('../api/portide', () => import('../test/portideMock'));

beforeEach(() => resetPortideMock());

test('the pairing code input keeps digits and splits them into two groups', () => {
  expect(formatCodeInput('1234')).toBe('1234');
  expect(formatCodeInput('12345')).toBe('1234 5');
  expect(formatCodeInput('1234-5678-9')).toBe('1234 5678');
  expect(formatCodeInput('ab12')).toBe('12');
});

test('join with an 8-digit code; the address hides under "can\'t find it?"', async () => {
  const user = userEvent.setup();
  let finish: (r: unknown) => void = () => {};
  Service.Join.mockImplementation(() => new Promise((r) => (finish = r)));
  const onDone = vi.fn();
  renderWithProviders(<JoinCodeForm others={[]} onDone={onDone} />);
  const code = screen.getByRole('textbox', { name: '配对码' });
  await user.type(code, '1234567');
  expect(code).toHaveValue('1234 567');
  expect(screen.getByRole('button', { name: '加入' })).toBeDisabled();
  await user.type(code, '8');
  await user.click(screen.getByRole('button', { name: '找不到对方？' }));
  await user.type(screen.getByLabelText('对方地址（可不填）'), '192.168.1.5:7443');
  await user.click(screen.getByRole('button', { name: '加入' }));
  expect(Service.Join).toHaveBeenCalledWith('12345678', '192.168.1.5:7443');
  expect(screen.getByText('正在寻找并连接…')).toBeInTheDocument();
  const result = { mode: 'join', peerName: '电脑', devices: 2 };
  finish(result);
  await vi.waitFor(() => expect(onDone).toHaveBeenCalledWith(result));
});
