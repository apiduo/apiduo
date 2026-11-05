package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// skQuotaPayload mirrors the controller response without importing gin.H.
type skQuotaPayload struct {
	Success bool `json:"success"`
	Data    struct {
		Items     []model.SkQuotaData `json:"items"`
		Totals    model.SkQuotaTotals `json:"totals"`
		Truncated bool                `json:"truncated"`
		Limit     int                 `json:"limit"`
	} `json:"data"`
}

func setupSkAnalyticsTestDB(t *testing.T) {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.QuotaData{}))

	require.NoError(t, model.DB.Create(&model.Token{Id: 11, UserId: 1, Key: "sk-primary", Name: "primary"}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 22, UserId: 2, Key: "sk-backup", Name: "backup"}).Error)

	rows := []*model.QuotaData{
		// Same (token, user, model) bucket as the next row: must be merged.
		{UserID: 1, Username: "alice", TokenID: 11, ModelName: "gpt-a", CreatedAt: 1100, Count: 2, Quota: 100, TokenUsed: 40},
		{UserID: 1, Username: "alice", TokenID: 11, ModelName: "gpt-a", CreatedAt: 1101, Count: 1, Quota: 50, TokenUsed: 10},
		// Same token, different model: must stay a separate bucket.
		{UserID: 1, Username: "alice", TokenID: 11, ModelName: "gpt-b", CreatedAt: 1100, Count: 1, Quota: 70, TokenUsed: 20},
		// Different token and user.
		{UserID: 2, Username: "bob", TokenID: 22, ModelName: "gpt-a", CreatedAt: 1100, Count: 1, Quota: 30, TokenUsed: 5},
		// Usage with no token attached.
		{UserID: 3, Username: "carol", TokenID: 0, ModelName: "gpt-c", CreatedAt: 1100, Count: 1, Quota: 5, TokenUsed: 1},
	}
	for _, row := range rows {
		require.NoError(t, model.DB.Create(row).Error)
	}
}

func callSkQuotaDates(t *testing.T, query string) skQuotaPayload {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/data/sk", GetSkQuotaDates)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/data/sk"+query, nil)
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload skQuotaPayload
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success, recorder.Body.String())
	return payload
}

func TestSkQuotaDatesAggregatesTokenUserModel(t *testing.T) {
	setupSkAnalyticsTestDB(t)

	payload := callSkQuotaDates(t, "?start_timestamp=1000&end_timestamp=1200")
	items := payload.Data.Items
	require.Len(t, items, 4)

	// Ordered by quota desc, so the merged alice/gpt-a bucket comes first.
	require.Equal(t, 150, items[0].Quota)
	require.Equal(t, 3, items[0].Count)
	require.Equal(t, 50, items[0].TokenUsed)
	require.Equal(t, "alice", items[0].Username)
	require.Equal(t, "gpt-a", items[0].ModelName)
	require.Equal(t, 11, items[0].TokenID)

	// Totals cover every bucket, including the one without a token.
	require.Equal(t, int64(255), payload.Data.Totals.Quota)
	require.Equal(t, int64(6), payload.Data.Totals.Count)
	require.False(t, payload.Data.Truncated)
}

func TestSkQuotaDatesMasksTokenKey(t *testing.T) {
	setupSkAnalyticsTestDB(t)

	payload := callSkQuotaDates(t, "?start_timestamp=1000&end_timestamp=1200")
	require.NotEmpty(t, payload.Data.Items)
	for _, item := range payload.Data.Items {
		require.NotContains(t, item.TokenKey, "primary")
		require.NotContains(t, item.TokenKey, "backup")
	}
	require.Equal(t, "sk-p**********mary", payload.Data.Items[0].TokenKey)
}

func TestSkQuotaDatesResolvesSoftDeletedToken(t *testing.T) {
	setupSkAnalyticsTestDB(t)
	require.NoError(t, model.DB.Delete(&model.Token{}, 22).Error)

	payload := callSkQuotaDates(t, "?start_timestamp=1000&end_timestamp=1200")
	for _, item := range payload.Data.Items {
		if item.TokenID != 22 {
			continue
		}
		require.Equal(t, "backup", item.TokenName, "soft-deleted tokens keep their usage label")
		return
	}
	t.Fatal("expected a bucket for the deleted token")
}

func TestSkQuotaDatesTruncatesButKeepsTotals(t *testing.T) {
	setupSkAnalyticsTestDB(t)

	payload := callSkQuotaDates(t, "?start_timestamp=1000&end_timestamp=1200&limit=2")
	require.Len(t, payload.Data.Items, 2)
	require.True(t, payload.Data.Truncated)
	require.Equal(t, 2, payload.Data.Limit)
	// The kept rows are the two largest buckets, and totals stay complete.
	require.Equal(t, 150, payload.Data.Items[0].Quota)
	require.Equal(t, 70, payload.Data.Items[1].Quota)
	require.Equal(t, int64(255), payload.Data.Totals.Quota)
}

func TestSkQuotaDatesFiltersByModelAndRange(t *testing.T) {
	setupSkAnalyticsTestDB(t)

	byModel := callSkQuotaDates(t, "?start_timestamp=1000&end_timestamp=1200&model=gpt-b")
	require.Len(t, byModel.Data.Items, 1)
	require.Equal(t, 70, byModel.Data.Items[0].Quota)
	require.Equal(t, int64(70), byModel.Data.Totals.Quota)

	// End at 1100 keeps only rows recorded at 1100.
	byRange := callSkQuotaDates(t, "?start_timestamp=1000&end_timestamp=1100")
	require.Equal(t, int64(205), byRange.Data.Totals.Quota)
}

func TestSkQuotaDatesRejectsInvalidLimit(t *testing.T) {
	setupSkAnalyticsTestDB(t)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/data/sk", GetSkQuotaDates)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/data/sk?start_timestamp=1000&end_timestamp=1200&limit=abc", nil)
	engine.ServeHTTP(recorder, request)

	var payload struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.False(t, payload.Success)
}
