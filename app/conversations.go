package app

import (
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"portide/core/i18n"
	"portide/core/node"
)

// AllDevices 是会话列表里"全部设备"那一项的 Key（看全部历史，发送时再选设备）。
const AllDevices = ""

// previewRunes 是会话列表里最后一条消息摘要的最大长度（字符数）。
const previewRunes = 60

// Conversation 是会话列表里的一项：一台设备，或者"全部设备"（M3c 设计 5.4）。
type Conversation struct {
	Key           string `json:"key"`  // 设备 KeyID；"全部设备"为空串
	Name          string `json:"name"` // 设备名；"全部设备"为"全部设备"
	Icon          string `json:"icon"` // 设备图标（Device.Icon）；"全部设备"为空
	Online        bool   `json:"online"`
	Path          string `json:"path"`
	LastPreview   string `json:"lastPreview"`   // 最后一条历史的摘要，没有历史时为空
	LastDirection string `json:"lastDirection"` // "in" | "out"
	LastAt        int64  `json:"lastAt"`        // Unix 毫秒；没有历史时为 0
	Queued        int    `json:"queued"`        // 发给这台设备、还没结束的任务数
}

// Conversations 返回"全部设备"和每台其他设备的会话，设备按最后一条历史从新到旧排列，从没收发过的按名字排在后面。
func (s *Service) Conversations() ([]Conversation, error) {
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	devs, err := s.Devices()
	if err != nil {
		return nil, err
	}
	tasks, err := n.Store().ListOutgoing()
	if err != nil {
		return nil, uiError(err)
	}
	queued := map[string]int{}
	for _, t := range tasks {
		if unfinished(t.Status) {
			queued[t.PeerKey]++
		}
	}
	all := Conversation{Key: AllDevices, Name: i18n.T("全部设备")}
	if err := fillLast(n, &all); err != nil {
		return nil, err
	}
	var convs []Conversation
	for _, d := range devs {
		if d.Self {
			continue
		}
		all.Queued += queued[d.KeyID]
		c := Conversation{Key: d.KeyID, Name: d.Name, Icon: d.Icon, Online: d.Online, Path: d.Path, Queued: queued[d.KeyID]}
		if err := fillLast(n, &c); err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}
	sort.SliceStable(convs, func(i, j int) bool {
		if convs[i].LastAt != convs[j].LastAt {
			return convs[i].LastAt > convs[j].LastAt
		}
		return convs[i].Name < convs[j].Name
	})
	return append([]Conversation{all}, convs...), nil
}

func fillLast(n *node.Node, c *Conversation) error {
	last, err := n.History(c.Key, 1)
	if err != nil {
		return uiError(err)
	}
	if len(last) == 1 {
		c.LastPreview, c.LastDirection, c.LastAt = preview(last[0].Body), last[0].Direction, last[0].CreatedAt.UnixMilli()
	}
	return nil
}

// preview 返回一条历史的一行摘要：文本取第一行，文件取文件名或"N 个文件"，没收到文件的接收用报价摘要。
func preview(b node.HistoryBody) string {
	switch {
	case len(b.Texts) > 0:
		line, _, _ := strings.Cut(strings.TrimSpace(b.Texts[0]), "\n")
		return truncateRunes(line, previewRunes)
	case len(b.Files) == 1:
		return path.Base(b.Files[0].Path)
	case len(b.Files) > 1:
		top, _, _ := strings.Cut(b.Files[0].Path, "/")
		for _, f := range b.Files {
			if t, _, _ := strings.Cut(f.Path, "/"); t != top {
				return i18n.Tf("%d 个文件", len(b.Files))
			}
		}
		return i18n.Tf("%s（%d 个文件）", top, len(b.Files)) // 一个文件夹
	}
	return b.Summary
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}
