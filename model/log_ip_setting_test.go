package model

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The IP column of a log row follows the caller's setting. The auth middleware
// already writes that setting into the context from the same user row it read,
// so the log writer must not read the row again: without Redis, GetUserSetting
// is a database round trip per request. Both directions matter — a context that
// opts out must not be overridden by a stored default, and a context that opts
// in must win over a stored opt-out.
func TestRecordConsumeLogTakesIPSettingFromContext(t *testing.T) {
	truncateTables(t)

	off, on := false, true
	cases := []struct {
		name        string
		context     *dto.UserSetting
		stored      *bool
		wantIP      string
		explanation string
	}{
		{"context opts out, stored default", &dto.UserSetting{RecordIpLog: &off}, nil, "", "the setting already loaded for this request wins"},
		{"context opts in, stored opt-out", &dto.UserSetting{RecordIpLog: &on}, &off, "203.0.113.7", "a stale stored row must not override the context"},
		{"no context, stored opt-out", nil, &off, "", "the fallback still reads the stored row"},
		{"no context, stored default", nil, nil, "203.0.113.7", "an unset choice records the IP"},
	}

	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.NoError(t, DB.Exec("DELETE FROM logs").Error)
			user := createIPSettingTestUser(t, index)
			if testCase.stored != nil {
				setting, err := common.Marshal(dto.UserSetting{RecordIpLog: testCase.stored})
				require.NoError(t, err)
				require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("setting", string(setting)).Error)
			}

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			c.Request.RemoteAddr = "203.0.113.7:4321"
			if testCase.context != nil {
				common.SetContextKey(c, constant.ContextKeyUserSetting, *testCase.context)
			}

			RecordConsumeLog(c, user.Id, RecordConsumeLogParams{ModelName: "test-model", Quota: 1, TokenId: 1, ChannelId: 1})

			var stored Log
			require.NoError(t, LOG_DB.Order("id desc").First(&stored).Error)
			assert.Equal(t, testCase.wantIP, stored.Ip, testCase.explanation)
		})
	}
}

// shouldRecordIP must keep serving callers that have no authenticated context.
func TestShouldRecordIPWithoutContext(t *testing.T) {
	truncateTables(t)
	user := createIPSettingTestUser(t, 99)

	off := false
	setting, err := common.Marshal(dto.UserSetting{RecordIpLog: &off})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("setting", string(setting)).Error)

	assert.False(t, shouldRecordIP(nil, user.Id))
	// 没有用户行时 GetUserSetting 返回零值设置，即「未表态」= 记 IP。这与改动前
	// 的行为一致：默认开启不是靠用户行存在才成立的。
	assert.True(t, shouldRecordIP(nil, user.Id+5000))
}

func createIPSettingTestUser(t *testing.T, seq int) User {
	t.Helper()
	user := User{
		Username:    fmt.Sprintf("ip-setting-user-%d", seq),
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		AffCode:     fmt.Sprintf("ip-setting-aff-%d", seq),
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}
