package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 交接判据 1：同一时刻只能有一个未结束的班次。
// 「当前谁在值班」必须只有一个答案，否则交接失去意义。
func TestStartShiftClosesPrevious(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&OpsShift{}))

	first, err := StartShift(1, "alice", "")
	require.NoError(t, err)

	cur, err := CurrentShift()
	require.NoError(t, err)
	require.NotNil(t, cur)
	require.Equal(t, "alice", cur.OwnerName)

	// 第二个人接班，第一班自动结束并记下交班备注
	second, err := StartShift(2, "bob", "gpt-4o 还在报 500，已切到备用渠道")
	require.NoError(t, err)
	require.Equal(t, "bob", second.OwnerName)

	cur, _ = CurrentShift()
	require.Equal(t, "bob", cur.OwnerName)
	require.Equal(t, first.Id, cur.Id-1, "只能有一个未结束的班次")

	shifts, err := ListShifts(10)
	require.NoError(t, err)
	require.Len(t, shifts, 2)

	// 交班备注落在旧班上
	var old OpsShift
	require.NoError(t, DB.First(&old, first.Id).Error)
	require.NotZero(t, old.EndedAt, "旧班必须已结束")
	require.Contains(t, old.HandoverNote, "还在报 500")
}

// 交接备注只属于旧班。
// 同一句话同时挂在新旧两班，值班的人会以为是两条不同的留言。
func TestShiftNoteBelongsToPreviousOnly(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&OpsShift{}))

	_, err := StartShift(1, "alice", "")
	require.NoError(t, err)
	second, err := StartShift(2, "bob", "上游已恢复，但要看今晚")
	require.NoError(t, err)

	require.Empty(t, second.HandoverNote, "新班不应继承交接备注")

	var stored OpsShift
	require.NoError(t, DB.First(&stored, second.Id).Error)
	require.Empty(t, stored.HandoverNote)
}

// 排序判据：同一秒内连续接班，最近的必须排最前。
// 否则前端 find「最近结束的班」会取到更早那条，显示的交接留言就错了。
func TestListShiftsOrdersByIDOnTie(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&OpsShift{}))

	a, _ := StartShift(1, "alice", "")
	b, _ := StartShift(2, "bob", "第一条留言")
	c, _ := StartShift(3, "carol", "第二条留言")

	shifts, err := ListShifts(10)
	require.NoError(t, err)
	require.Len(t, shifts, 3)
	require.Equal(t, c.Id, shifts[0].Id, "最新班次排第一")
	require.Equal(t, b.Id, shifts[1].Id)
	require.Equal(t, a.Id, shifts[2].Id)

	// 前端逻辑：找最近结束的班 → 应该是 bob（留了第二条留言的那班之前）
	var closed *OpsShift
	for i := range shifts {
		if shifts[i].EndedAt > 0 {
			closed = &shifts[i]
			break
		}
	}
	require.NotNil(t, closed)
	require.Equal(t, "第二条留言", closed.HandoverNote, "应取最近的已结束班")
}
