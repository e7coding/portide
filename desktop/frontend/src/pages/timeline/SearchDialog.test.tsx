import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { markMatch, SearchDialog } from './SearchDialog';
import { NavProvider, useNav } from '../../store/nav';
import { renderWithProviders } from '../../test/render';
import { Service, resetPortideMock } from '../../test/portideMock';

vi.mock('../../api/portide', () => import('../../test/portideMock'));

const result = (over: object) => ({ historyId: 1, peerKey: 'DESK', peerName: '台式机', direction: 'in', at: Date.now(), snippet: '', fileIndex: -1, ...over });

function Probe() {
  const nav = useNav();
  return (
    <div>
      <button onClick={() => nav.setSearchOpen(true)}>打开搜索</button>
      <span>当前会话：{nav.conversation}</span>
      <span>高亮：{String(nav.highlight)}</span>
    </div>
  );
}

beforeEach(() => {
  resetPortideMock();
  localStorage.clear();
  Service.Search.mockResolvedValue([
    result({ historyId: 3, snippet: '会议纪要在 Wiki 上' }),
    result({ historyId: 8, peerKey: 'PHON', peerName: '手机', direction: 'out', snippet: 'WIKI-封面.png', fileIndex: 1 }),
  ]);
});

test('marks matches case-insensitively', () => {
  expect(markMatch('Wiki 与 wiki', 'WIKI')).toEqual([
    { text: 'Wiki', hit: true }, { text: ' 与 ', hit: false }, { text: 'wiki', hit: true },
  ]);
  expect(markMatch('abc', ' ')).toEqual([{ text: 'abc', hit: false }]);
});

test('searching and choosing a result opens its conversation and highlights it', async () => {
  const user = userEvent.setup();
  renderWithProviders(
    <NavProvider initial={{ conversation: 'DESK' }}>
      <Probe />
      <SearchDialog />
    </NavProvider>,
  );
  await user.click(screen.getByRole('button', { name: '打开搜索' }));
  await user.type(screen.getByRole('textbox', { name: '搜索消息和文件名' }), 'wiki');
  await waitFor(() => expect(Service.Search).toHaveBeenCalledWith('wiki'));
  const options = await screen.findAllByRole('option');
  expect(options).toHaveLength(2);
  expect(options[1]).toHaveTextContent('发给「手机」');
  await user.keyboard('{ArrowDown}{Enter}');
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  expect(screen.getByText('当前会话：PHON')).toBeInTheDocument();
  expect(screen.getByText('高亮：8')).toBeInTheDocument();
});

test('no results says so', async () => {
  const user = userEvent.setup();
  Service.Search.mockResolvedValue([]);
  renderWithProviders(
    <NavProvider>
      <Probe />
      <SearchDialog />
    </NavProvider>,
  );
  await user.click(screen.getByRole('button', { name: '打开搜索' }));
  await user.type(screen.getByRole('textbox'), '不存在');
  expect(await screen.findByText('没有找到「不存在」')).toBeInTheDocument();
});
