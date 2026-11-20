import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NoDevices } from './EmptyStates';
import { renderWithProviders } from '../../test/render';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

beforeEach(() => {
  resetPortideMock();
  Service.NearbyInviters.mockResolvedValue(false);
});

test('no devices: two buttons open the pairing dialog on the right tab', async () => {
  const user = userEvent.setup();
  const onAdd = vi.fn();
  renderWithProviders(<NoDevices onAdd={onAdd} />);
  await user.click(screen.getByRole('button', { name: '显示配对码' }));
  expect(onAdd).toHaveBeenLastCalledWith('show');
  await user.click(screen.getByRole('button', { name: '输入配对码' }));
  expect(onAdd).toHaveBeenLastCalledWith('enter');
  expect(screen.queryByText('附近有设备正在等待配对')).not.toBeInTheDocument();
});

test('shows a hint when a device nearby is waiting to pair', async () => {
  Service.NearbyInviters.mockResolvedValue(true);
  renderWithProviders(<NoDevices onAdd={vi.fn()} />);
  expect(await screen.findByText('附近有设备正在等待配对')).toBeInTheDocument();
});
