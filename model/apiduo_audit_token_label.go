package model

// 审计只存令牌指纹（SHA-256，64 位十六进制），不存明文也不存名字 —— 这是有意的：
// 审计库泄露不能变成钥匙泄露。代价是「这是哪把钥匙」需要回主库查一次。
//
// 审计在日志库（生产是 ClickHouse）、令牌在主库，两个库不能 join，
// 所以只能应用层批量补：一次 IN 查询覆盖整页，不做 N+1。
//
// UserAccessToken.TokenHash 的注释里写明了它「doubles as the audit token_ref」，
// 这里就靠这个约定按指纹回查，不需要再存一份映射。

import "strings"

// TokenRefLabel 是指纹还原出来的可读身份。
type TokenRefLabel struct {
	Ref       string `json:"ref"`
	Name      string `json:"name"`
	AgentKey  string `json:"agent_key,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}

// AuditTokenLabels 按指纹批量还原令牌名，返回的 key 是指纹本身。
//
// 查不到的指纹不出现在结果里 —— 令牌已被删除是正常形态（离职回收、定期轮换），
// 调用方按「只剩指纹」展示即可，不该让审计页因为一条老记录打不开。
func AuditTokenLabels(refs []string) (map[string]TokenRefLabel, error) {
	unique := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		unique = append(unique, ref)
	}
	out := make(map[string]TokenRefLabel, len(unique))
	if len(unique) == 0 || DB == nil {
		return out, nil
	}

	var tokens []struct {
		TokenHash  string
		Name       string
		AgentKeyId int
		ExpiresAt  int64
	}
	if err := DB.Table("user_access_tokens").
		Select("token_hash, name, agent_key_id, expires_at").
		Where("token_hash IN ?", unique).
		Scan(&tokens).Error; err != nil {
		return nil, err
	}

	// Agent 换出来的令牌名是每次握手现起的（可能带任务号），稳定标识是它背后的
	// key 名。两个都取回来，展示层自己决定怎么拼 —— 这里替它选一个就是丢信息。
	agentKeyIds := make([]int, 0, len(tokens))
	for _, token := range tokens {
		if token.AgentKeyId > 0 {
			agentKeyIds = append(agentKeyIds, token.AgentKeyId)
		}
	}
	keyNames, err := agentKeyNames(agentKeyIds)
	if err != nil {
		return nil, err
	}

	for _, token := range tokens {
		out[token.TokenHash] = TokenRefLabel{
			Ref:       token.TokenHash,
			Name:      token.Name,
			AgentKey:  keyNames[token.AgentKeyId],
			ExpiresAt: token.ExpiresAt,
		}
	}
	return out, nil
}

// agentKeyNames 按 id 批量取 key 名。
//
// 两次查询（令牌、key）而不是一次 join：agent_keys 与 user_access_tokens 都在主库，
// join 得动，但令牌查询在「筛选下拉」和「审计列表」两处形状不同，
// join 会让两边的 Select 各自长出一份。分开查更好维护，代价是一次索引命中。
func agentKeyNames(ids []int) (map[int]string, error) {
	names := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	var keys []struct {
		Id   int
		Name string
	}
	if err := DB.Table("agent_keys").Select("id, name").Where("id IN ?", ids).Scan(&keys).Error; err != nil {
		return nil, err
	}
	for _, key := range keys {
		names[key.Id] = key.Name
	}
	return names, nil
}

// AuditTokenOption 是审计筛选下拉的一行。
type AuditTokenOption struct {
	Ref       string `json:"ref"`
	Name      string `json:"name"`
	AgentKey  string `json:"agent_key,omitempty"`
	UserId    int    `json:"user_id"`
	Username  string `json:"username"`
	ExpiresAt int64  `json:"expires_at"`
}

// AuditTokenOptions 列出当前查看者可以拿来筛选审计的令牌。
//
// 范围与审计可见范围对齐：管理员看全站，普通用户只看自己的 —— 否则下拉本身
// 就成了一个用户枚举入口（能看见别人有几把钥匙、叫什么名字）。
//
// 按最近使用倒序：筛选时想找的几乎总是刚用过的那把。limit 由调用方封顶，
// 这里不做二次截断，否则「截断了多少」没法回给前端。
func AuditTokenOptions(userId int, allUsers bool, keyword string, limit int) ([]AuditTokenOption, error) {
	out := make([]AuditTokenOption, 0, limit)
	if DB == nil || limit <= 0 {
		return out, nil
	}

	q := DB.Table("user_access_tokens AS t").
		Select("t.token_hash, t.name, t.agent_key_id, t.expires_at, t.user_id, u.username").
		Joins("LEFT JOIN users u ON u.id = t.user_id")
	if !allUsers {
		q = q.Where("t.user_id = ?", userId)
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q = q.Where("t.name LIKE ?", "%"+keyword+"%")
	}
	var rows []struct {
		TokenHash  string
		Name       string
		AgentKeyId int
		ExpiresAt  int64
		UserId     int
		Username   string
	}
	if err := q.Order("t.last_used_at DESC, t.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}

	agentKeyIds := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.AgentKeyId > 0 {
			agentKeyIds = append(agentKeyIds, row.AgentKeyId)
		}
	}
	keyNames, err := agentKeyNames(agentKeyIds)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		out = append(out, AuditTokenOption{
			Ref:       row.TokenHash,
			Name:      row.Name,
			AgentKey:  keyNames[row.AgentKeyId],
			UserId:    row.UserId,
			Username:  row.Username,
			ExpiresAt: row.ExpiresAt,
		})
	}
	return out, nil
}
