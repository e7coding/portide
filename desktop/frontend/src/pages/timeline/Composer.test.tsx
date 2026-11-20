import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Composer, pastedName } from './Composer';
import { renderWithProviders } from '../../test/render';
import { device, self } from '../../test/fixtures';
import { Platform, Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

const desk = device();
const phone = device({ keyId: 'PHON', name: '手机', online: false });

beforeEach(() => {
  resetPortideMock();
  localStorage.clear();
  Service.SendText.mockResolvedValue({ tasks: 1, skipped: [] });
  Service.SendFiles.mockResolvedValue({ tasks: 1, skipped: [] });
  Service.SendPasted.mockResolvedValue({ tasks: 1, skipped: [] });
});

test('in a device conversation the target is fixed; Enter sends, Shift+Enter and IME composition do not', async () => {
  const user = userEvent.setup();
  renderWithProviders(<Composer conversation="DESK" devices={[self, desk, phone]} />);
  const box = screen.getByRole('textbox', { name: '输入要发送的文字' });
  expect(box).toHaveAttribute('placeholder', '发给「台式机」… 也可以拖入文件或粘贴图片');
  await user.type(box, '第一行{Shift>}{Enter}{/Shift}第二行');
  fireEvent.keyDown(box, { key: 'Enter', isComposing: true });
  expect(Service.SendText).not.toHaveBeenCalled();
  // WebKit 上确认输入法候选词的 Enter 有时 isComposing 为 false，但 keyCode 仍是 229：也不能发送。
  fireEvent.keyDown(box, { key: 'Enter', keyCode: 229 });
  expect(Service.SendText).not.toHaveBeenCalled();
  await user.keyboard('{Enter}');
  await waitFor(() => expect(Service.SendText).toHaveBeenCalledWith(['DESK'], '第一行\n第二行'));
  await waitFor(() => expect(box).toHaveValue(''));
});

test('in "all devices" targets are chosen in a menu and remembered', async () => {
  const user = userEvent.setup();
  const { unmount } = renderWithProviders(<Composer conversation="" devices={[self, desk, phone]} />);
  expect(screen.getByRole('textbox')).toHaveAttribute('placeholder', '先选择要发给哪台设备');
  expect(screen.getByRole('button', { name: '发送文件' })).toBeDisabled();
  await user.click(screen.getByRole('button', { name: '选择设备' }));
  await user.click(screen.getByRole('checkbox', { name: '台式机' }));
  await user.click(screen.getByRole('checkbox', { name: '手机' }));
  await user.keyboard('{Escape}');
  expect(screen.getByRole('button', { name: '发给：台式机、手机' })).toBeInTheDocument();
  unmount();
  renderWithProviders(<Composer conversation="" devices={[self, desk, phone]} />);
  expect(screen.getByRole('button', { name: '发给：台式机、手机' })).toBeInTheDocument();
});

test('files and folders come from the system dialogs; skipped items and dialog failures are reported', async () => {
  const user = userEvent.setup();
  renderWithProviders(<Composer conversation="DESK" devices={[self, desk]} />);
  Platform.PickFiles.mockResolvedValue(['/a', '/b']);
  Service.SendFiles.mockResolvedValue({ tasks: 1, skipped: ['/b'] });
  await user.click(screen.getByRole('button', { name: '发送文件' }));
  await waitFor(() => expect(Service.SendFiles).toHaveBeenCalledWith(['DESK'], ['/a', '/b']));
  expect(await screen.findByRole('alert')).toHaveTextContent('跳过了 1 项');

  Platform.PickFolder.mockRejectedValue(new Error('对话框打不开'));
  await user.click(screen.getByRole('button', { name: '发送文件夹' }));
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('对话框打不开'));
});

test('pasting an image sends it with a readable name; pasting text just types', async () => {
  renderWithProviders(<Composer conversation="DESK" devices={[self, desk]} />);
  const box = screen.getByRole('textbox');
  const png = new File([new Uint8Array([137, 80, 78, 71])], 'image.png', { type: 'image/png' });
  fireEvent.paste(box, { clipboardData: { files: [png] } });
  await waitFor(() => expect(Service.SendPasted).toHaveBeenCalled());
  const [targets, name, data] = Service.SendPasted.mock.calls[0];
  expect(targets).toEqual(['DESK']);
  expect(name).toMatch(/^截图 \d{4}-\d\d-\d\d \d\d\.\d\d\.\d\d\.png$/);
  expect(data).toMatch(/^data:image\/png;base64,/);

  fireEvent.paste(box, { clipboardData: { files: [] } });
  expect(Service.SendPasted).toHaveBeenCalledTimes(1);
});

test('pasted names keep real file names', () => {
  expect(pastedName(new File([''], '设计稿.png', { type: 'image/png' }))).toBe('设计稿.png');
  expect(pastedName(new File([''], '', { type: 'image/jpeg' }), new Date(2026, 9, 26, 9, 5, 7))).toBe('截图 2026-10-26 09.05.07.jpg');
});

test('without other devices the composer explains why it is empty', () => {
  renderWithProviders(<Composer conversation="" devices={[self]} />);
  expect(screen.getByText('设备圈里还没有其他设备，添加之后就能在这里发送')).toBeInTheDocument();
});
