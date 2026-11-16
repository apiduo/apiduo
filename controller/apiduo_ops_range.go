package controller

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// opsAlignGranularity 是时间窗对齐粒度（秒）。
//
// 为什么必须对齐：前端每次请求都用 Date.now() 算 from/to，
// 两个相隔一秒的请求参数完全不同，缓存 key 就永远对不上——
// 实测缓存命中率接近 0，等于没做。
// 对齐之后，同一个 30 分钟窗口内的所有请求共享一个 key。
//
// 代价是数据最多滞后一个粒度。对「看板」这个场景，
// 滞后 30 分钟完全可接受；要秒级实时该去看原始日志，不是看趋势图。
const opsAlignGranularity = 1800

// opsRange 解析并归一化时间窗。默认最近 24 小时。
func opsRange(c *gin.Context) (int64, int64) {
	now := time.Now().Unix()
	from, _ := strconv.ParseInt(c.Query("from"), 10, 64)
	to, _ := strconv.ParseInt(c.Query("to"), 10, 64)
	if to <= 0 {
		to = now
	}
	if from <= 0 || from >= to {
		from = to - 24*3600
	}
	return alignWindow(from, to)
}

// alignWindow 把时间窗对齐到桶边界。
//
// to 向下取整：不能把还没走完的桶算进来。
// from 同样向下取整，但必须由「对齐后的 to 减跨度」推导，
// 而不是直接用原始 from 取整——后者的结果会随 to 的秒级抖动而变化。
func alignWindow(from, to int64) (int64, int64) {
	span := to - from
	if span < opsAlignGranularity {
		return from, to
	}
	alignedTo := to / opsAlignGranularity * opsAlignGranularity
	alignedFrom := (alignedTo - span) / opsAlignGranularity * opsAlignGranularity
	return alignedFrom, alignedTo
}
