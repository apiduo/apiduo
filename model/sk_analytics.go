package model

import (
	"gorm.io/gorm"
)

// SkQuotaData is one (token, user, model) bucket aggregated from quota_data.
// It powers the SK analytics section of the dashboard.
type SkQuotaData struct {
	TokenID             int    `json:"token_id"`
	TokenName           string `json:"token_name"`
	TokenKey            string `json:"token_key"`
	UserID              int    `json:"user_id"`
	Username            string `json:"username"`
	ModelName           string `json:"model_name"`
	Count               int    `json:"count"`
	Quota               int    `json:"quota"`
	TokenUsed           int    `json:"token_used"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
}

// SkQuotaTotals is the whole-range total for the same filter set. It is
// computed without grouping so the UI can still show the real total when the
// row list is truncated by the limit.
type SkQuotaTotals struct {
	Quota               int64 `json:"quota" gorm:"column:quota"`
	Count               int64 `json:"count" gorm:"column:count"`
	TokenUsed           int64 `json:"token_used" gorm:"column:token_used"`
	CacheReadTokens     int64 `json:"cache_read_tokens" gorm:"column:cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens" gorm:"column:cache_creation_tokens"`
}

// skQuotaFilter describes who is allowed to see the rows. UserID is only ever
// filled from the authenticated session, never from a query parameter, so a
// self-scoped request cannot be widened by the caller.
type skQuotaFilter struct {
	UserID   int
	Username string
	Model    string
}

func skQuotaQuery(startTime int64, endTime int64, filter skQuotaFilter) *gorm.DB {
	query := DB.Table("quota_data").
		Where("created_at >= ? and created_at <= ?", startTime, endTime)
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Username != "" {
		query = query.Where("username = ?", filter.Username)
	}
	if filter.Model != "" {
		query = query.Where("model_name = ?", filter.Model)
	}
	return query
}

// GetSkQuotaData returns the highest-spending (token, user, model) buckets plus
// totals for the full filter set. Rows are ordered by quota so a truncated
// result keeps the buckets that matter.
func GetSkQuotaData(startTime int64, endTime int64, username string, modelName string, limit int) ([]*SkQuotaData, *SkQuotaTotals, error) {
	return querySkQuota(startTime, endTime, skQuotaFilter{Username: username, Model: modelName}, limit)
}

// GetSelfSkQuotaData is GetSkQuotaData narrowed to one owner. A reseller is a
// regular user whose downstream customers are distinguished by token, so the
// owner id is the whole boundary: every row belongs to a key they own.
func GetSelfSkQuotaData(userID int, startTime int64, endTime int64, modelName string, limit int) ([]*SkQuotaData, *SkQuotaTotals, error) {
	if userID <= 0 {
		return []*SkQuotaData{}, &SkQuotaTotals{}, nil
	}
	return querySkQuota(startTime, endTime, skQuotaFilter{UserID: userID, Model: modelName}, limit)
}

func querySkQuota(startTime int64, endTime int64, filter skQuotaFilter, limit int) ([]*SkQuotaData, *SkQuotaTotals, error) {
	rows := make([]*SkQuotaData, 0, min(limit, 256))
	err := skQuotaQuery(startTime, endTime, filter).
		Select("token_id, user_id, username, model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, sum(cache_read_tokens) as cache_read_tokens, sum(cache_creation_tokens) as cache_creation_tokens").
		Group("token_id, user_id, username, model_name").
		Order("quota DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, nil, err
	}

	totals := &SkQuotaTotals{}
	err = skQuotaQuery(startTime, endTime, filter).
		Select("coalesce(sum(quota), 0) as quota, coalesce(sum(count), 0) as count, coalesce(sum(token_used), 0) as token_used, coalesce(sum(cache_read_tokens), 0) as cache_read_tokens, coalesce(sum(cache_creation_tokens), 0) as cache_creation_tokens").
		Scan(totals).Error
	if err != nil {
		return nil, nil, err
	}

	if err := fillSkTokenInfo(rows); err != nil {
		return rows, totals, err
	}
	return rows, totals, nil
}

func fillSkTokenInfo(rows []*SkQuotaData) error {
	tokenIDs := make([]int, 0, len(rows))
	seen := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		if row.TokenID <= 0 {
			continue
		}
		if _, ok := seen[row.TokenID]; ok {
			continue
		}
		seen[row.TokenID] = struct{}{}
		tokenIDs = append(tokenIDs, row.TokenID)
	}
	if len(tokenIDs) == 0 {
		return nil
	}

	var tokens []struct {
		Id   int    `gorm:"column:id"`
		Name string `gorm:"column:name"`
		Key  string `gorm:"column:key"`
	}
	// Soft-deleted tokens keep their historical usage, so resolve them too
	// instead of reporting an anonymous bucket.
	if err := DB.Unscoped().Model(&Token{}).Select("id, name, key").Where("id IN ?", tokenIDs).Find(&tokens).Error; err != nil {
		return err
	}

	type tokenInfo struct {
		name string
		key  string
	}
	byID := make(map[int]tokenInfo, len(tokens))
	for _, token := range tokens {
		byID[token.Id] = tokenInfo{name: token.Name, key: token.Key}
	}
	for _, row := range rows {
		info, ok := byID[row.TokenID]
		if !ok {
			continue
		}
		row.TokenName = info.name
		row.TokenKey = MaskTokenKey(info.key)
	}
	return nil
}
