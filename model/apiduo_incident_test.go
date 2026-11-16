package model

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// 值班交接最要命的失败：两个人同时点「认领」，都收到成功。
// 之后两个人互相以为对方在处理，事故就一直挂着没人动。
func TestClaimIncidentIsExclusive(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&OpsIncident{}))

	inc, err := CreateIncident(OpsIncident{Title: "gpt-4o 全渠道 500", Severity: 1})
	require.NoError(t, err)

	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := ClaimIncident(inc.Id, n, "op"); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	require.Equal(t, 1, success, "并发认领必须只有一个成功")
}

// 结单后不能再被认领，也不能二次结单。
func TestIncidentStateMachine(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&OpsIncident{}))

	inc, err := CreateIncident(OpsIncident{Title: "x"})
	require.NoError(t, err)

	require.NoError(t, ClaimIncident(inc.Id, 7, "alice"))
	require.ErrorIs(t, ClaimIncident(inc.Id, 8, "bob"), ErrIncidentNotClaimable)

	require.NoError(t, CloseIncident(inc.Id, IncidentStatusResolved, "上游恢复"))
	require.ErrorIs(t, CloseIncident(inc.Id, IncidentStatusResolved, "again"), ErrIncidentNotClaimable)
	require.ErrorIs(t, ClaimIncident(inc.Id, 9, "carol"), ErrIncidentNotClaimable)

	got, err := ListIncidents(true, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, IncidentStatusResolved, got[0].Status)
	require.Equal(t, 7, got[0].OwnerId)

	open, err := ListIncidents(false, 10)
	require.NoError(t, err)
	require.Len(t, open, 0, "已结单的不出现在待办列表")
}

// 交接判据：未结的能转交，已结的不能。
// 已结单的负责人是事实记录，改了就失去追溯价值。
func TestReassignIncident(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&OpsIncident{}))

	inc, err := CreateIncident(OpsIncident{Title: "handover"})
	require.NoError(t, err)

	require.NoError(t, ClaimIncident(inc.Id, 1, "alice"))
	require.NoError(t, ReassignIncident(inc.Id, 2, "bob"), "未结事项应能转交")

	got, err := ListIncidents(false, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 2, got[0].OwnerId)
	require.Equal(t, "bob", got[0].OwnerName)
	require.Equal(t, IncidentStatusClaimed, got[0].Status, "转交后仍是已认领")

	require.NoError(t, CloseIncident(inc.Id, IncidentStatusResolved, "done"))
	require.ErrorIs(t, ReassignIncident(inc.Id, 3, "carol"), ErrIncidentNotClaimable,
		"已结单的不能转交")
}
