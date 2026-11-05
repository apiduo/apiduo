package model

import (
	"fmt"
	"strconv"
	"time"
)

// SeriesPoint is one day of aggregated usage.
type SeriesPoint struct {
	Date   string `json:"date"`
	Quota  int64  `json:"quota"`
	Count  int64  `json:"count"`
	Tokens int64  `json:"token_used"`
}

// SeriesGroup is a named daily series: one model, one user, or the caller.
type SeriesGroup struct {
	Key    string         `json:"key"`
	Label  string         `json:"label"`
	Points []*SeriesPoint `json:"points"`
}

const secondsPerDay = 86400

// GetUsageSeries aggregates quota_data into daily buckets.
//
// dimension is "model", "user" or "self". tzOffsetMinutes shifts the bucket
// boundary so a day matches the caller's local midnight instead of UTC; without
// it every bucket would be off by the caller's timezone.
//
// Days with no usage are omitted rather than padded: the caller knows the
// requested range and can fill gaps, which keeps the payload small for models
// that are used intermittently.
func GetUsageSeries(startTime int64, endTime int64, dimension string, tzOffsetMinutes int) ([]*SeriesGroup, error) {
	dimColumn := "model_name"
	if dimension == "user" {
		dimColumn = "username"
	}
	if dimension == "channel" {
		dimColumn = "channel_id"
	}
	// FLOOR keeps the day index an integer in MySQL, where / returns a decimal.
	offsetSeconds := int64(tzOffsetMinutes) * 60
	dayExpr := fmt.Sprintf("FLOOR((created_at + ?) / %d)", secondsPerDay)

	query := DB.Table("quota_data").
		Select(dimColumn+" as dim, "+dayExpr+" as day_index, coalesce(sum(quota), 0) as quota, coalesce(sum(count), 0) as count, coalesce(sum(token_used), 0) as token_used", offsetSeconds).
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("dim, day_index").
		Order("dim, day_index")

	var rows []struct {
		Dim       string
		DayIndex  int64
		Quota     int64
		Count     int64
		TokenUsed int64
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}

	byKey := make(map[string]*SeriesGroup, 32)
	order := make([]string, 0, 32)
	for _, row := range rows {
		group, ok := byKey[row.Dim]
		if !ok {
			group = &SeriesGroup{Key: row.Dim, Label: row.Dim, Points: make([]*SeriesPoint, 0, 32)}
			byKey[row.Dim] = group
			order = append(order, row.Dim)
		}
		group.Points = append(group.Points, &SeriesPoint{
			Date:   dayIndexToDate(row.DayIndex),
			Quota:  row.Quota,
			Count:  row.Count,
			Tokens: row.TokenUsed,
		})
	}

	groups := make([]*SeriesGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, byKey[key])
	}
	if dimension == "channel" {
		if err := fillSeriesChannelLabels(groups); err != nil {
			return groups, err
		}
	}
	return groups, nil
}

// fillSeriesChannelLabels swaps the raw channel id for its display name. The
// key keeps a prefix so a channel can never collide with a model name when the
// two dimensions are loaded into the same client cache.
func fillSeriesChannelLabels(groups []*SeriesGroup) error {
	channelIDs := make([]int, 0, len(groups))
	for _, group := range groups {
		id, err := strconv.Atoi(group.Key)
		if err != nil || id <= 0 {
			continue
		}
		channelIDs = append(channelIDs, id)
	}

	names := make(map[int]string, len(channelIDs))
	if len(channelIDs) > 0 {
		var channels []struct {
			Id   int    `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		if err := DB.Table("channels").Select("id, name").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			names[channel.Id] = channel.Name
		}
	}

	for _, group := range groups {
		id, err := strconv.Atoi(group.Key)
		if err != nil {
			continue
		}
		switch {
		case names[id] != "":
			group.Label = names[id]
		case id == 0:
			group.Label = "channel-unassigned"
		default:
			group.Label = fmt.Sprintf("channel-%d", id)
		}
		group.Key = fmt.Sprintf("channel:%d", id)
	}
	return nil
}

// GetSelfUsageSeries returns the caller's own daily usage as a single group.
func GetSelfUsageSeries(userID int, startTime int64, endTime int64, tzOffsetMinutes int) (*SeriesGroup, error) {
	offsetSeconds := int64(tzOffsetMinutes) * 60
	dayExpr := fmt.Sprintf("FLOOR((created_at + ?) / %d)", secondsPerDay)

	var rows []struct {
		DayIndex  int64
		Quota     int64
		Count     int64
		TokenUsed int64
	}
	err := DB.Table("quota_data").
		Select(dayExpr+" as day_index, coalesce(sum(quota), 0) as quota, coalesce(sum(count), 0) as count, coalesce(sum(token_used), 0) as token_used", offsetSeconds).
		Where("user_id = ? and created_at >= ? and created_at <= ?", userID, startTime, endTime).
		Group("day_index").
		Order("day_index").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	group := &SeriesGroup{Key: "self", Label: "self", Points: make([]*SeriesPoint, 0, len(rows))}
	for _, row := range rows {
		group.Points = append(group.Points, &SeriesPoint{
			Date:   dayIndexToDate(row.DayIndex),
			Quota:  row.Quota,
			Count:  row.Count,
			Tokens: row.TokenUsed,
		})
	}
	return group, nil
}

func dayIndexToDate(dayIndex int64) string {
	return time.Unix(dayIndex*secondsPerDay, 0).UTC().Format("2006-01-02")
}

// UserBalance is one user's current balance plus a dense daily spend array.
// Both are needed to project when the balance runs out.
type UserBalance struct {
	UserID   int     `json:"user_id"`
	Username string  `json:"username"`
	Group    string  `json:"group"`
	Quota    int64   `json:"quota"`
	Daily    []int64 `json:"daily"`
}

// GetUserBalanceSeries returns every user that spent anything in the window,
// with a zero-filled daily series of exactly `days` entries. The series is
// aligned to the last `days` calendar days ending at endTime, so the client can
// index it without knowing the raw timestamps.
func GetUserBalanceSeries(startTime int64, endTime int64, days int, tzOffsetMinutes int) ([]*UserBalance, string, error) {
	const maxDays = 400
	offsetSeconds := int64(tzOffsetMinutes) * 60
	dayExpr := fmt.Sprintf("FLOOR((created_at + ?) / %d)", secondsPerDay)

	if days <= 0 {
		return []*UserBalance{}, dayIndexToDate((endTime + offsetSeconds) / secondsPerDay), nil
	}
	days = min(days, maxDays)
	endDay := (endTime + offsetSeconds) / secondsPerDay
	startDay := endDay - int64(days) + 1
	totalDays := days

	var rows []struct {
		UserID   int
		DayIndex int64
		Quota    int64
	}
	err := DB.Table("quota_data").
		Select("user_id, "+dayExpr+" as day_index, coalesce(sum(quota), 0) as quota", offsetSeconds).
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("user_id, day_index").
		Order("user_id, day_index").
		Scan(&rows).Error
	if err != nil {
		return nil, "", err
	}

	daily := make(map[int][]int64, len(rows))
	for _, row := range rows {
		series, ok := daily[row.UserID]
		if !ok {
			series = make([]int64, totalDays)
			daily[row.UserID] = series
		}
		index := int(row.DayIndex - startDay)
		if index >= 0 && index < totalDays {
			series[index] = row.Quota
		}
	}
	if len(daily) == 0 {
		return []*UserBalance{}, dayIndexToDate(startDay), nil
	}

	ids := make([]int, 0, len(daily))
	for id := range daily {
		ids = append(ids, id)
	}
	var users []struct {
		Id       int
		Username string
		Quota    int64
		Group    string `gorm:"column:group_name"`
	}
	// "group" is a reserved word, so it is quoted per database dialect.
	err = DB.Table("users").
		Select("id, username, quota, "+commonGroupCol+" as group_name").
		Where("id IN ?", ids).
		Find(&users).Error
	if err != nil {
		return nil, "", err
	}

	balances := make([]*UserBalance, 0, len(users))
	for _, user := range users {
		balances = append(balances, &UserBalance{
			UserID:   user.Id,
			Username: user.Username,
			Group:    user.Group,
			Quota:    user.Quota,
			Daily:    daily[user.Id],
		})
	}
	return balances, dayIndexToDate(startDay), nil
}
