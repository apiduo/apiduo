package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 上游查询回 status=failure 时，第一次只记下来：包装层自己回源 502 也会这么回，
// 而生成往往还在跑。第二次仍报失败才落终态并退款。
func TestPollReportedFailureNeedsSecondSighting(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 520, 520, 520
	const initialQuota, preConsumed, tokenRemain = 10_000, 4_000, 7_000
	seedUser(t, userID, initialQuota)
	seedToken(t, tokenID, userID, "sk-poll-confirm", tokenRemain)
	ch := &model.Channel{Id: channelID, Type: constant.ChannelTypeKling, Name: "poll", Key: "sk-test", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(ch).Error)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_poll_confirm"
	task.PrivateData.UpstreamTaskID = "upstream_poll_confirm"
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &scriptedPollingAdaptor{
		statusCode: http.StatusOK,
		parse: &relaycommon.TaskInfo{
			Status: model.TaskStatusFailure,
			Reason: "<html><head><title>502 Bad Gateway</title></head></html>",
		},
	}
	poll := func() model.Task {
		require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, ch, task.GetUpstreamTaskID(), map[string]*model.Task{
			task.GetUpstreamTaskID(): task,
		}))
		var stored model.Task
		require.NoError(t, model.DB.First(&stored, task.ID).Error)
		return stored
	}

	first := poll()
	assert.EqualValues(t, model.TaskStatusInProgress, first.Status, "一次失败不足以判死")
	assert.Empty(t, first.FailReason)
	assert.Equal(t, "upstream returned an HTML error page (HTTP 200)", first.PrivateData.PendingFailure)

	task = &first
	second := poll()
	assert.EqualValues(t, model.TaskStatusFailure, second.Status)
	assert.Equal(t, "upstream returned an HTML error page (HTTP 200)", second.FailReason)
	assert.Zero(t, second.Quota)
	assert.Equal(t, initialQuota+preConsumed, getUserQuota(t, userID))
}
