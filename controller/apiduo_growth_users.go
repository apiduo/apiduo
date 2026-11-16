package controller

/* APIDUO:growth-users:BEGIN */
// 把看板的派生字段附加到用户列表的返回项上。
// 独立文件、独立 marker，避免改动上游 GetAllUsers 的函数体。

import (
	"github.com/QuantumNous/new-api/model"
)

// UserWithGrowth 是用户列表里带派生列的一行。
// 匿名字段让原来的 User 字段在 JSON 里保持平铺，前端无需改结构。
type UserWithGrowth struct {
	*model.User
	GrowthQuota30d int    `json:"growth_quota_30d"`
	GrowthTrend    string `json:"growth_trend"`
	GrowthTier     string `json:"growth_tier"`
}

// AttachGrowthStats 批量补上三列派生数据。查不到看板行的用户
// （刚注册、尚未聚合）保持零值，不影响原有展示。
func AttachGrowthStats(users []*model.User) []UserWithGrowth {
	ids := make([]int, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.Id)
	}
	boards, err := model.GetGrowthBoardsByUserIds(ids)
	if err != nil {
		boards = map[int]model.GrowthUserBoard{}
	}
	out := make([]UserWithGrowth, 0, len(users))
	for _, u := range users {
		row := UserWithGrowth{User: u}
		if board, ok := boards[u.Id]; ok {
			row.GrowthQuota30d = board.Quota30d
			row.GrowthTrend = board.Trend
			row.GrowthTier = board.EffectiveTier()
		}
		out = append(out, row)
	}
	return out
}

/* APIDUO:growth-users:END */
