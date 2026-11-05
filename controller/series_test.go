package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type seriesPayload struct {
	Success bool `json:"success"`
	Data    struct {
		Groups []model.SeriesGroup `json:"groups"`
	} `json:"data"`
	Message string `json:"message"`
}

type selfSeriesPayload struct {
	Success bool              `json:"success"`
	Data    model.SeriesGroup `json:"data"`
	Message string            `json:"message"`
}

var (
	seriesDay1 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix()
	seriesDay2 = seriesDay1 + 86400
)

func setupSeriesTestDB(t *testing.T) {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.QuotaData{}))

	rows := []*model.QuotaData{
		// Same model, same day: must merge into one point.
		{UserID: 1, Username: "alice", ModelName: "gpt-a", ChannelID: 1, CreatedAt: seriesDay1 + 3600, Quota: 100, Count: 1, TokenUsed: 10},
		{UserID: 1, Username: "alice", ModelName: "gpt-a", ChannelID: 1, CreatedAt: seriesDay1 + 7200, Quota: 50, Count: 1, TokenUsed: 5},
		{UserID: 1, Username: "alice", ModelName: "gpt-a", ChannelID: 1, CreatedAt: seriesDay2 + 3600, Quota: 80, Count: 1, TokenUsed: 8},
		// Different model and user on day 2.
		{UserID: 2, Username: "bob", ModelName: "gpt-b", ChannelID: 2, CreatedAt: seriesDay2 + 3600, Quota: 30, Count: 1, TokenUsed: 3},
		// UTC evening that falls on the next day in UTC+8.
		{UserID: 2, Username: "bob", ModelName: "gpt-tz", ChannelID: 1, CreatedAt: time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC).Unix(), Quota: 70, Count: 1, TokenUsed: 7},
	}
	for _, row := range rows {
		require.NoError(t, model.DB.Create(row).Error)
	}
	require.NoError(t, model.DB.Create(&model.Channel{Id: 1, Name: "east"}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 2, Name: "west"}).Error)
}

func callSeries(t *testing.T, path string, query string, userID int) (int, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/data/series", GetUsageSeries)
	engine.GET("/api/data/user-balances", GetUserBalanceSeries)
	engine.GET("/api/data/series/self", func(c *gin.Context) {
		c.Set("id", userID)
		GetSelfUsageSeries(c)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path+query, nil)
	engine.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func seriesQuery(tzOffset int) string {
	return "?start_timestamp=" + itoa(seriesDay1-86400) + "&end_timestamp=" + itoa(seriesDay2+86400) +
		"&tz_offset_minutes=" + itoa(int64(tzOffset))
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestUsageSeriesGroupsByModelAndDay(t *testing.T) {
	setupSeriesTestDB(t)

	code, body := callSeries(t, "/api/data/series", seriesQuery(0)+"&dimension=model", 0)
	require.Equal(t, http.StatusOK, code)
	var payload seriesPayload
	require.NoError(t, common.Unmarshal(body, &payload))
	require.True(t, payload.Success, string(body))
	require.Len(t, payload.Data.Groups, 3)

	byKey := map[string]model.SeriesGroup{}
	for _, group := range payload.Data.Groups {
		byKey[group.Key] = group
	}

	gptA := byKey["gpt-a"]
	require.Len(t, gptA.Points, 2)
	require.Equal(t, "2026-11-01", gptA.Points[0].Date)
	require.Equal(t, int64(150), gptA.Points[0].Quota)
	require.Equal(t, int64(15), gptA.Points[0].Tokens)
	require.Equal(t, "2026-11-02", gptA.Points[1].Date)
	require.Equal(t, int64(80), gptA.Points[1].Quota)

	require.Equal(t, int64(30), byKey["gpt-b"].Points[0].Quota)
}

func TestUsageSeriesShiftsDayBoundaryWithTimezone(t *testing.T) {
	setupSeriesTestDB(t)

	// 2026-10-31 20:00 UTC is 2026-11-01 04:00 in UTC+8.
	_, utcBody := callSeries(t, "/api/data/series", seriesQuery(0)+"&dimension=model", 0)
	_, shanghaiBody := callSeries(t, "/api/data/series", seriesQuery(480)+"&dimension=model", 0)

	findTZ := func(body []byte) string {
		var payload seriesPayload
		require.NoError(t, common.Unmarshal(body, &payload))
		for _, group := range payload.Data.Groups {
			if group.Key == "gpt-tz" {
				return group.Points[0].Date
			}
		}
		return ""
	}

	require.Equal(t, "2026-10-31", findTZ(utcBody))
	require.Equal(t, "2026-11-01", findTZ(shanghaiBody))
}

func TestUsageSeriesRejectsUnknownDimension(t *testing.T) {
	setupSeriesTestDB(t)
	code, body := callSeries(t, "/api/data/series", seriesQuery(0)+"&dimension=vendor", 0)
	require.Equal(t, http.StatusOK, code)
	var payload seriesPayload
	require.NoError(t, common.Unmarshal(body, &payload))
	require.False(t, payload.Success)
	require.Contains(t, payload.Message, "dimension")
}

func TestUsageSeriesGroupsByChannelWithNames(t *testing.T) {
	setupSeriesTestDB(t)

	code, body := callSeries(t, "/api/data/series", seriesQuery(0)+"&dimension=channel", 0)
	require.Equal(t, http.StatusOK, code)
	var payload seriesPayload
	require.NoError(t, common.Unmarshal(body, &payload))
	require.True(t, payload.Success, string(body))

	byKey := map[string]model.SeriesGroup{}
	for _, group := range payload.Data.Groups {
		byKey[group.Key] = group
	}

	east, ok := byKey["channel:1"]
	require.True(t, ok, "channel 1 should be keyed as channel:1")
	require.Equal(t, "east", east.Label)
	// 100 + 50 + 80 + 70 for channel 1, merged across models, users and days.
	eastTotal := int64(0)
	for _, point := range east.Points {
		eastTotal += point.Quota
	}
	require.Equal(t, int64(300), eastTotal)

	west, ok := byKey["channel:2"]
	require.True(t, ok)
	require.Equal(t, "west", west.Label)
	require.Len(t, west.Points, 1)
	require.Equal(t, int64(30), west.Points[0].Quota)
}

func TestUserBalanceSeriesFillsEmptyDays(t *testing.T) {
	setupSeriesTestDB(t)
	require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "alice", AffCode: "aff-a", Quota: 5000}).Error)
	require.NoError(t, model.DB.Create(&model.User{Id: 2, Username: "bob", AffCode: "aff-b", Quota: 0}).Error)

	// End right after day 2 so the 14-day window is deterministic.
	query := "?days=14&tz_offset_minutes=0&end_timestamp=" + itoa(seriesDay2+86400)
	code, body := callSeries(t, "/api/data/user-balances", query, 0)
	require.Equal(t, http.StatusOK, code)
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			StartDate string              `json:"start_date"`
			Users     []model.UserBalance `json:"users"`
		} `json:"data"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(body, &payload))
	require.True(t, payload.Success, string(body))
	require.Equal(t, "2026-10-21", payload.Data.StartDate)
	require.Len(t, payload.Data.Users, 2)

	var alice, bob *model.UserBalance
	for index := range payload.Data.Users {
		switch payload.Data.Users[index].UserID {
		case 1:
			alice = &payload.Data.Users[index]
		case 2:
			bob = &payload.Data.Users[index]
		}
	}
	require.NotNil(t, alice)
	require.NotNil(t, bob)

	// A dense series is what the projection needs; gaps must read as zero.
	require.Len(t, alice.Daily, 14)
	require.Equal(t, int64(0), alice.Daily[0])
	// Day 1 of the data lands on index 11, day 2 on index 12.
	require.Equal(t, int64(150), alice.Daily[11])
	require.Equal(t, int64(80), alice.Daily[12])
	require.Equal(t, int64(5000), alice.Quota)

	require.Equal(t, int64(70), bob.Daily[10])
	require.Equal(t, int64(30), bob.Daily[12])
	require.Equal(t, int64(0), bob.Quota)
}

func TestSelfUsageSeriesOnlyReturnsCaller(t *testing.T) {
	setupSeriesTestDB(t)

	code, body := callSeries(t, "/api/data/series/self", seriesQuery(0), 1)
	require.Equal(t, http.StatusOK, code)
	var payload selfSeriesPayload
	require.NoError(t, common.Unmarshal(body, &payload))
	require.True(t, payload.Success, string(body))
	require.Equal(t, "self", payload.Data.Key)
	require.Len(t, payload.Data.Points, 2)
	require.Equal(t, int64(150), payload.Data.Points[0].Quota)
	require.Equal(t, int64(80), payload.Data.Points[1].Quota)

	// Bob's 30 lives on another user and must not leak in.
	total := int64(0)
	for _, point := range payload.Data.Points {
		total += point.Quota
	}
	require.Equal(t, int64(230), total)
}
