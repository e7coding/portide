package node

import (
	"path"
	"strings"
)

// SearchScanLimit 是一次搜索最多解密检查的历史条数（M3c 设计 5.2）：历史是字段加密的，只能逐条解密匹配。
const SearchScanLimit = 20000

// SearchHistory 从新到旧找文字内容或文件名包含 query 的历史（不区分大小写），找到 limit 条或扫描完 SearchScanLimit 条为止。
func (n *Node) SearchHistory(query string, limit int) ([]HistoryEntry, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || limit <= 0 {
		return nil, nil
	}
	all, err := n.History("", SearchScanLimit)
	if err != nil {
		return nil, err
	}
	var out []HistoryEntry
	for _, e := range all {
		if historyMatches(e.Body, q) {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// historyMatches 报告一条历史的文字或文件名是否包含 q（q 已转成小写）。文件只比较文件名，不比较所在目录。
func historyMatches(b HistoryBody, q string) bool {
	for _, t := range b.Texts {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	for _, f := range b.Files {
		if strings.Contains(strings.ToLower(path.Base(f.Path)), q) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(b.Summary), q)
}
