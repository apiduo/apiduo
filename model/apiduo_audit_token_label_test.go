package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 审计只存指纹，展示层要把它还原成名字。这条链路要成立三件事：
// 普通 PAT 取令牌名、agent 令牌取 key 名、查不到的指纹不返回。
func TestAuditTokenLabels(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&AgentKey{}))

	pad := strings.Repeat("0", 63)
	human := UserAccessToken{UserId: 1, Name: "deploy-bot", TokenHash: "a" + pad, ExpiresAt: 100}
	require.NoError(t, DB.Create(&human).Error)

	key := AgentKey{UserId: 1, Name: "ci-runner", PublicKey: "test-key", CreatedAt: 1}
	require.NoError(t, DB.Create(&key).Error)
	agentToken := UserAccessToken{UserId: 1, Name: "ci-runner#run-42", TokenHash: "b" + pad, AgentKeyId: key.Id}
	require.NoError(t, DB.Create(&agentToken).Error)

	t.Cleanup(func() {
		DB.Where("token_hash IN ?", []string{human.TokenHash, agentToken.TokenHash}).Delete(&UserAccessToken{})
		DB.Where("id = ?", key.Id).Delete(&AgentKey{})
	})

	labels, err := AuditTokenLabels([]string{
		human.TokenHash,
		human.TokenHash, // 重复输入只查一次
		agentToken.TokenHash,
		"c" + pad, // 已删除的令牌
		"",        // 空指纹
	})
	require.NoError(t, err)

	// 查不到的指纹不出现在结果里，由调用方回落成「只剩指纹」。
	require.Len(t, labels, 2)

	assert.Equal(t, "deploy-bot", labels[human.TokenHash].Name)
	assert.Empty(t, labels[human.TokenHash].AgentKey, "人工令牌没有 agent key")

	assert.Equal(t, "ci-runner#run-42", labels[agentToken.TokenHash].Name)
	assert.Equal(t, "ci-runner", labels[agentToken.TokenHash].AgentKey)
}

// 空输入必须是一次「什么都不做」，不能退化成全表扫描或空 IN 语法错误。
func TestAuditTokenLabelsEmpty(t *testing.T) {
	labels, err := AuditTokenLabels(nil)
	require.NoError(t, err)
	assert.Empty(t, labels)

	labels, err = AuditTokenLabels([]string{"", "  "})
	require.NoError(t, err)
	assert.Empty(t, labels)
}

// 下拉的范围必须跟审计可见范围一致：管理员全站、普通用户只看自己。
// 这一条是防「筛选项变成用户枚举」的边界，不能只靠前端隐藏。
func TestAuditTokenOptionsScope(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&AgentKey{}))

	pad := strings.Repeat("0", 63)
	mine := UserAccessToken{UserId: 11, Name: "mine", TokenHash: "d" + pad, LastUsedAt: 200}
	require.NoError(t, DB.Create(&mine).Error)
	theirs := UserAccessToken{UserId: 22, Name: "theirs", TokenHash: "e" + pad, LastUsedAt: 100}
	require.NoError(t, DB.Create(&theirs).Error)
	t.Cleanup(func() {
		DB.Where("token_hash IN ?", []string{mine.TokenHash, theirs.TokenHash}).Delete(&UserAccessToken{})
	})

	own, err := AuditTokenOptions(11, false, "", 50)
	require.NoError(t, err)
	require.Len(t, own, 1, "普通用户只能看到自己的令牌")
	assert.Equal(t, "mine", own[0].Name)
	assert.Equal(t, 11, own[0].UserId)

	all, err := AuditTokenOptions(11, true, "", 50)
	require.NoError(t, err)
	names := make([]string, 0, len(all))
	for _, option := range all {
		names = append(names, option.Name)
	}
	assert.Contains(t, names, "mine")
	assert.Contains(t, names, "theirs", "管理员能看到全站")

	// 最近使用的排前面：筛选时想找的几乎总是刚用过的那把。
	var first, second int
	for i, option := range all {
		switch option.Name {
		case "mine":
			first = i
		case "theirs":
			second = i
		}
	}
	assert.Less(t, first, second)
}

// 关键字只按名字过滤，且必须带 limit —— 没有 limit 的列表接口迟早被当成导出用。
func TestAuditTokenOptionsKeywordAndLimit(t *testing.T) {
	pad := strings.Repeat("0", 63)
	rows := []UserAccessToken{
		{UserId: 33, Name: "deploy-a", TokenHash: "f" + pad},
		{UserId: 33, Name: "deploy-b", TokenHash: "1" + strings.Repeat("0", 63)},
		{UserId: 33, Name: "other", TokenHash: "2" + strings.Repeat("0", 63)},
	}
	for i := range rows {
		require.NoError(t, DB.Create(&rows[i]).Error)
	}
	t.Cleanup(func() {
		for i := range rows {
			DB.Where("token_hash = ?", rows[i].TokenHash).Delete(&UserAccessToken{})
		}
	})

	matched, err := AuditTokenOptions(33, false, "deploy", 50)
	require.NoError(t, err)
	assert.Len(t, matched, 2)
	for _, option := range matched {
		assert.Contains(t, option.Name, "deploy")
	}

	capped, err := AuditTokenOptions(33, false, "", 1)
	require.NoError(t, err)
	assert.Len(t, capped, 1, "limit 必须生效")
}
