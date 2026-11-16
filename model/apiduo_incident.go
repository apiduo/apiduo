package model

import (
	"errors"
	"time"
)

// 值班状态机。只允许单向流转，不允许从 resolved 回到 open——
// 那样会让「已处理」变成可撤销，交接记录就失去意义。
const (
	IncidentStatusOpen     = "open"
	IncidentStatusClaimed  = "claimed"
	IncidentStatusResolved = "resolved"
	IncidentStatusIgnored  = "ignored"
)

// OpsIncident 是一次人工开单的值班事项。
//
// 为什么不直接给「实时算出的错误队列」加状态：队列每秒都在变，
// 一个错误指纹可能今天出现、明天消失、后天又来。状态必须挂在
// 一个人工确认过的实体上，否则交接记录会随着队列刷新而漂移。
type OpsIncident struct {
	Id         int    `json:"id" gorm:"primaryKey"`
	Title      string `json:"title" gorm:"size:255;default:''"`
	Severity   int    `json:"severity" gorm:"default:2"`
	Status     string `json:"status" gorm:"size:16;index;default:'open'"`
	OwnerId    int    `json:"owner_id" gorm:"index;default:0"`
	OwnerName  string `json:"owner_name" gorm:"size:64;default:''"`
	Detail     string `json:"detail" gorm:"type:text"`
	Resolution string `json:"resolution" gorm:"type:text"`
	StartedAt  int64  `json:"started_at" gorm:"index;default:0"`
	ClaimedAt  int64  `json:"claimed_at" gorm:"default:0"`
	ClosedAt   int64  `json:"closed_at" gorm:"default:0"`
	CreatedBy  string `json:"created_by" gorm:"size:64;default:''"`
	CreatedAt  int64  `json:"created_at" gorm:"default:0"`
	UpdatedAt  int64  `json:"updated_at" gorm:"default:0"`
}

// ErrIncidentNotClaimable 表示这条事项已经不在可认领状态。
var ErrIncidentNotClaimable = errors.New("incident is not claimable")

// ClaimIncident 抢占一条事项。
//
// 必须用条件更新（WHERE status = 'open'）而不是先查后写：
// 值班交接最常见的场景就是两个人同时看到同一条，先查后写会让
// 两个人都收到「认领成功」，然后互相以为对方在处理。
func ClaimIncident(id, ownerId int, ownerName string) error {
	now := time.Now().Unix()
	res := DB.Model(&OpsIncident{}).
		Where("id = ? AND status = ?", id, IncidentStatusOpen).
		Updates(map[string]any{
			"status":     IncidentStatusClaimed,
			"owner_id":   ownerId,
			"owner_name": ownerName,
			"claimed_at": now,
			"updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrIncidentNotClaimable
	}
	return nil
}

// CloseIncident 结单（解决或忽略）。只有当前认领人才能结，
// 除非调用方是管理员——这一层由 controller 判断。
func CloseIncident(id int, status, resolution string) error {
	if status != IncidentStatusResolved && status != IncidentStatusIgnored {
		return errors.New("invalid close status")
	}
	now := time.Now().Unix()
	res := DB.Model(&OpsIncident{}).
		Where("id = ? AND status IN ?", id, []string{IncidentStatusOpen, IncidentStatusClaimed}).
		Updates(map[string]any{
			"status":     status,
			"resolution": resolution,
			"closed_at":  now,
			"updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrIncidentNotClaimable
	}
	return nil
}

// ListIncidents 返回事项列表。默认只看未结单的——
// 值班交接关心的是「还有什么没处理完」。
func ListIncidents(includeClosed bool, limit int) ([]OpsIncident, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []OpsIncident
	q := DB.Model(&OpsIncident{}).Order("severity ASC, started_at DESC").Limit(limit)
	if !includeClosed {
		q = q.Where("status IN ?", []string{IncidentStatusOpen, IncidentStatusClaimed})
	}
	err := q.Find(&out).Error
	return out, err
}

// CreateIncident 手工开单。
func CreateIncident(ev OpsIncident) (*OpsIncident, error) {
	now := time.Now().Unix()
	if ev.StartedAt == 0 {
		ev.StartedAt = now
	}
	if ev.Status == "" {
		ev.Status = IncidentStatusOpen
	}
	ev.CreatedAt = now
	ev.UpdatedAt = now
	if err := DB.Create(&ev).Error; err != nil {
		return nil, err
	}
	return &ev, nil
}

// ReassignIncident 把一条未结事项转交给另一个人。
//
// 值班交接的真实场景：上一班处理到一半下班了，下一班要能接手，
// 而不是只能看着「已被 alice 认领」干等到她上班。
// 只允许在未结状态下转交——已结单的历史保持原负责人，那是事实记录。
func ReassignIncident(id, ownerId int, ownerName string) error {
	now := time.Now().Unix()
	res := DB.Model(&OpsIncident{}).
		Where("id = ? AND status IN ?", id, []string{IncidentStatusOpen, IncidentStatusClaimed}).
		Updates(map[string]any{
			"status":     IncidentStatusClaimed,
			"owner_id":   ownerId,
			"owner_name": ownerName,
			"claimed_at": now,
			"updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrIncidentNotClaimable
	}
	return nil
}
