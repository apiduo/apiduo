package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 一次查询要同时给出多个参数，并且失败只记到该记的那个参数上。
//
// 这条测试盯的是批量化的两个风险：
//  1. 合并成一条查询后，某个参数的计数被别的参数串了；
//  2. blame 过滤在应用层漏掉，导致「一次失败污染全部参数」的老毛病复发。
func TestQueryParamsSQLiteCountsEachParamIndependently(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Log{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	insert := func(typ int, snapshot map[string]any, upstreamErr, blamed string) {
		info := map[string]any{"param_snapshot": snapshot, "mcp": true}
		if upstreamErr != "" {
			info["upstream_error"] = upstreamErr
		}
		if blamed != "" {
			info["failed_param"] = blamed
		}
		raw, _ := json.Marshal(map[string]any{"admin_info": info})
		row := model.Log{
			CreatedAt: time.Now().Unix(),
			Type:      typ,
			ModelName: "batch-image",
			Other:     string(raw),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("插入日志失败: %v", err)
		}
	}

	// 一次成功：两个参数都记 ok
	insert(logTypeConsume, map[string]any{"n": "9", "aspect_ratio": "32:9", "size": "1024x1024"}, "", "")
	// 本地校验点名 content：跟 n / aspect_ratio 无关，写入侧不给归因
	insert(logTypeError, map[string]any{"n": "9", "aspect_ratio": "32:9"}, `模型 x 未声明参数 "content"，可用：n`, "")
	// 上游信封错误：content 是请求体结构，同样不算到快照参数上
	insert(logTypeError, map[string]any{"n": "9"}, `'content' 必须是非空数组`, "")
	// 真正的取值失败：单参数快照，写入侧归因给 n
	insert(logTypeError, map[string]any{"n": "9"}, "upstream 400 invalid value", "n")

	stats, err := queryParamsSQLite(db, "batch-image", []string{"n", "aspect_ratio"}, time.Now().AddDate(0, 0, -7).Unix())
	if err != nil {
		t.Fatalf("批量查询失败: %v", err)
	}

	byKey := map[string]ParamStat{}
	for _, s := range stats {
		byKey[s.Param+"="+s.Value] = s
	}

	if s := byKey["n=9"]; s.OK != 1 || s.Fail != 1 {
		t.Errorf("n=9 期望 1 成功 1 失败（另外两次是 content 相关），实际 %+v", s)
	}
	if s := byKey["aspect_ratio=32:9"]; s.OK != 1 || s.Fail != 0 {
		t.Errorf("aspect_ratio=32:9 期望 1 成功 0 失败，实际 %+v", s)
	}
	// 没在 names 里的参数不该出现：调用方只声明了 n 和 aspect_ratio
	for key := range byKey {
		if key == "size=1024x1024" {
			t.Errorf("未声明的参数不该出现在结果里：%s", key)
		}
	}
	if len(stats) != 2 {
		t.Errorf("期望 2 行（每个参数一个取值），实际 %d 行：%+v", len(stats), stats)
	}
}
