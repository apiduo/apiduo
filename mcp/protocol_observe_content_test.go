package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 判据：失败的 MCP 调用，日志 content 必须带上真正的错误原因。
//
// 线上实测：content 只写 "mcp image.generate"，43 条失败全归成一簇，
// 服务质量的「错误聚类」根因列显示的就是这个工具名，毫无诊断价值。
// 真正的错误在 other.admin_info.upstream_error 里，但聚类是按 content 分组的。
func TestWriteObservationPutsErrorInContent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	prev := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = prev })

	writeObservation(callObservation{
		Tool:     "image.generate",
		ModelID:  "qwen-image-3.0",
		OK:       false,
		ErrorMsg: "upstream 502 (POST /images/generations): upstream error, please retry later",
	})

	// EnqueueLog 是异步的，轮询等它落库
	var row model.Log
	for range 200 {
		if err := db.Where("type = ?", 5).First(&row).Error; err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotZero(t, row.Id, "错误日志应写入")

	require.Contains(t, row.Content, "image.generate", "工具名要保留")
	require.Contains(t, row.Content, "upstream 502", "错误原因必须在 content 里")

	// 成功：只写工具名，且用独立类型 8 —— 不能混进 type=2，
	// 否则日志页会把一次 MCP 调用显示成两行（另一行是 relay 的计费记录）。
	writeObservation(callObservation{Tool: "image.generate", ModelID: "qwen-image-3.0", OK: true})
	var ok model.Log
	for range 200 {
		if err := db.Where("type = ?", logTypeMCPObservation).First(&ok).Error; err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, "mcp image.generate", ok.Content)
	require.Equal(t, -1, ok.ChannelId, "观察日志没有渠道，用 -1 与真实请求区分")
	require.Zero(t, ok.Quota, "MCP 层不计费")

	// other 里的结构化字段不能丢
	var other map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.Other), &other))
	require.Contains(t, other["admin_info"], "upstream_error")
}
