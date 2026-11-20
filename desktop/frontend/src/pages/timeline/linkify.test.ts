import { linkify } from './linkify';

test('finds http and https links and leaves trailing punctuation outside', () => {
  expect(linkify('看这个 https://example.com/a?b=1，还有 http://192.168.1.5:8080/x.')).toEqual([
    { text: '看这个 ' },
    { text: 'https://example.com/a?b=1', url: 'https://example.com/a?b=1' },
    { text: '，还有 ' },
    { text: 'http://192.168.1.5:8080/x', url: 'http://192.168.1.5:8080/x' },
    { text: '.' },
  ]);
  expect(linkify('没有链接 ftp://x')).toEqual([{ text: '没有链接 ftp://x' }]);
  expect(linkify('(https://a.b/c)')).toEqual([{ text: '(' }, { text: 'https://a.b/c', url: 'https://a.b/c' }, { text: ')' }]);
});
