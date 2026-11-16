package model

import (
	"time"

	"gorm.io/gorm"
)

// OpsShift 是一次值班班次。
//
// 为什么需要它：OpsIncident 只记得「谁认领了」，不记得「什么时候换人」。
// 值班交接时真正要回答的是「上一班是谁、他从几点到几点、走之前留了什么话」，
// 这三件事拼不出一个具体的时刻——所以要有班次实体。
type OpsShift struct {
	Id           int    `json:"id" gorm:"primaryKey"`
	OwnerId      int    `json:"owner_id" gorm:"index;default:0"`
	OwnerName    string `json:"owner_name" gorm:"size:64;default:''"`
	StartedAt    int64  `json:"started_at" gorm:"index;default:0"`
	EndedAt      int64  `json:"ended_at" gorm:"default:0"`
	HandoverNote string `json:"handover_note" gorm:"type:text"`
	CreatedAt    int64  `json:"created_at" gorm:"default:0"`
}

// StartShift 开始一个新班次，并结束上一个未结束的班次。
//
// 同一时刻只允许一个未结束的班次：否则「当前谁在值班」会有多个答案，
// 而交接的价值完全建立在这个答案是唯一的之上。
// 用事务保证「关旧班 + 开新班」不会只成功一半。
func StartShift(ownerId int, ownerName, note string) (*OpsShift, error) {
	now := time.Now().Unix()
	// 新班不带交接备注：那段话是「上一班走之前留的」，
	// 写在旧班上才读得通。写在新班会让同一个句子出现两次。
	shift := &OpsShift{
		OwnerId:   ownerId,
		OwnerName: ownerName,
		StartedAt: now,
		CreatedAt: now,
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&OpsShift{}).
			Where("ended_at = 0").
			Updates(map[string]any{
				"ended_at": now,
				// 交班备注记在旧班上：它描述的是「这一班结束时的状态」，
				// 记在新班上就变成了「接班时的提醒」，语义不同。
				"handover_note": note,
			}).Error; err != nil {
			return err
		}
		return tx.Create(shift).Error
	})
	if err != nil {
		return nil, err
	}
	return shift, nil
}

// CurrentShift 返回当前未结束的班次，没有则 nil。
func CurrentShift() (*OpsShift, error) {
	var shift OpsShift
	err := DB.Model(&OpsShift{}).Where("ended_at = 0").
		Order("started_at DESC").First(&shift).Error
	if err != nil {
		return nil, nil
	}
	return &shift, nil
}

// ListShifts 返回最近若干个班次，最新的排前面。
func ListShifts(limit int) ([]OpsShift, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var out []OpsShift
	// id 作次级排序：同一秒内连续接班时 started_at 相同，
	// 只按它排序结果不稳定，前端 find「最近结束的班」会取到更早的那条，
	// 于是显示的交接留言不是刚走的人留下的。
	err := DB.Model(&OpsShift{}).Order("started_at DESC, id DESC").Limit(limit).Find(&out).Error
	return out, err
}
