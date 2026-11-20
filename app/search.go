package app

import (
	"path"
	"strings"
	"unicode/utf8"
)

// 搜索（M3c 设计 5.2）。
const (
	searchLimit   = 50 // 最多返回几条结果
	snippetBefore = 12 // 匹配片段在命中位置之前保留的字符数
	snippetRunes  = 60 // 匹配片段的最大长度（字符数）
)

// SearchResult 是一条搜索结果：点击后跳到这条历史所在的会话并高亮它。
type SearchResult struct {
	HistoryID int64  `json:"historyId"`
	PeerKey   string `json:"peerKey"`
	PeerName  string `json:"peerName"`
	Direction string `json:"direction"`
	At        int64  `json:"at"`
	Snippet   string `json:"snippet"`   // 匹配到的文字片段或文件名
	FileIndex int    `json:"fileIndex"` // 匹配的是文件名时是文件的序号，否则为 -1
}

// Search 从新到旧搜索文字内容和文件名（不区分大小写），最多返回 50 条。
func (s *Service) Search(query string) ([]SearchResult, error) {
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	entries, err := n.SearchHistory(query, searchLimit)
	if err != nil {
		return nil, uiError(err)
	}
	q := strings.ToLower(strings.TrimSpace(query))
	names := deviceNames(n)
	out := make([]SearchResult, 0, len(entries))
	for _, e := range entries {
		r := SearchResult{HistoryID: e.ID, PeerKey: e.PeerKey, PeerName: currentName(names, e.PeerKey, e.Body.PeerName), Direction: e.Direction, At: e.CreatedAt.UnixMilli(), FileIndex: -1}
		r.Snippet = e.Body.Summary
		for i := len(e.Body.Files) - 1; i >= 0; i-- { // 倒序：最后留下的是第一个匹配的文件
			if name := path.Base(e.Body.Files[i].Path); strings.Contains(strings.ToLower(name), q) {
				r.Snippet, r.FileIndex = name, i
			}
		}
		for _, t := range e.Body.Texts {
			if sn, ok := snippet(t, q); ok {
				r.Snippet, r.FileIndex = sn, -1
				break
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// snippet 返回文本 t 里包含 q（小写）的一段：命中位置之前留几个字，整段不超过 snippetRunes 个字，换行换成空格。
func snippet(t, q string) (string, bool) {
	lower := []rune(strings.ToLower(t))
	runes := []rune(t)
	if len(lower) != len(runes) { // 极少数字符转小写后长度会变：退回到从头截取
		if !strings.Contains(strings.ToLower(t), q) {
			return "", false
		}
		return flatten(truncateRunes(t, snippetRunes)), true
	}
	i := strings.Index(string(lower), q)
	if i < 0 {
		return "", false
	}
	start := utf8.RuneCountInString(string(lower)[:i]) - snippetBefore
	prefix := "…"
	if start <= 0 {
		start, prefix = 0, ""
	}
	return prefix + flatten(truncateRunes(string(runes[start:]), snippetRunes)), true
}

func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
