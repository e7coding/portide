package app

import (
	"testing"

	"portide/internal/fixtures"
)

// iOS 的 Swift 模型按 ios/PortideTests/Fixtures.json 解码（PortideTests/ModelsTests.swift）。这里检查那份样例的字段
// 和 Go 结构体的 JSON 字段一一对应：改了这里的结构体却没同步 Swift 时，这个测试会失败（M4）。
func TestIOSFixturesMatchGoTypes(t *testing.T) {
	fixtures.Check(t, "../ios/PortideTests/Fixtures.json", map[string]any{
		"State": State{}, "Device": Device{}, "Pairing": Pairing{}, "RemoveResult": RemoveResult{}, "LeaveResult": LeaveResult{},
		"TimelineItem": TimelineItem{}, "SendResult": SendResult{}, "InboxItem": InboxItem{}, "Settings": Settings{},
		"RelayInfo": RelayInfo{}, "TransferList": TransferList{}, "JoinResult": JoinResult{},
		// iOS 界面改造：会话列表、搜索结果、能否改名。
		"Conversation": Conversation{}, "SearchResult": SearchResult{}, "RenameCheck": RenameCheck{},
	})
}
