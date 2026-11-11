package model

import (
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// 任务日志的「模型」与「状态集合」筛选。
//
// 两件事上游的 SyncTaskQueryParams 都表达不了：
//
//  1. 模型名不在 tasks 表的列上，只写在 properties JSON 的 origin_model_name /
//     upstream_model_name 里（InitTask 提交时从 RelayInfo 抄进去）。做成列要改
//     InitTask 并回填历史行，收益只是省一次扫描；而这条条件只在运维显式填写时
//     才拼进 WHERE，其余请求零成本。
//  2. 上游的 status 是等值条件，而「排队中」在库里是 NOT_START / SUBMITTED /
//     QUEUED 三个状态。集合条件只能在等值条件之外补，所以调用方会先把
//     Status 清空、把值交给 Statuses。
//
// 四个查询入口（列表/计数 × 管理员/本人）各加一行 applyApiduoTaskFilters，
// 语义天然一致 —— 分开写必然漂。

// apiduoTaskStatusWhitelist 是允许筛选的任务状态全集（与常量一一对应）。
// 状态走 IN 拼接，白名单比转义更省心：非法值直接丢弃，不会把脏字符串塞进 SQL。
var apiduoTaskStatusWhitelist = []string{
	"NOT_START",
	"SUBMITTED",
	"QUEUED",
	"IN_PROGRESS",
	"SUCCESS",
	"FAILURE",
	"UNKNOWN",
}

// ParseApiduoTaskStatuses 把逗号分隔的状态过滤值收敛成白名单内的去重集合。
// 大小写不敏感，空片段与重复值丢弃，顺序保持入参顺序（便于日志与测试阅读）。
func ParseApiduoTaskStatuses(raw string) []string {
	statuses := make([]string, 0, 3)
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part == "" || slices.Contains(statuses, part) {
			continue
		}
		if !slices.Contains(apiduoTaskStatusWhitelist, part) {
			continue
		}
		statuses = append(statuses, part)
	}
	return statuses
}

// applyApiduoTaskFilters 追加 apiduo 侧的筛选条件，返回新的查询对象。
func applyApiduoTaskFilters(query *gorm.DB, params SyncTaskQueryParams) *gorm.DB {
	if len(params.Statuses) > 0 {
		query = query.Where("status IN ?", params.Statuses)
	}
	if modelName := strings.TrimSpace(params.Model); modelName != "" {
		query = applyApiduoTaskModelFilter(query, modelName)
	}
	return query
}

// applyApiduoTaskModelFilter 按模型名做大小写不敏感的子串匹配。
//
// 之所以是子串而不是精确：模型名偏长（MiniMax-Hailuo-2.3、flux-3-image），
// 运维手里通常只有片段；properties 上没有索引，精确匹配省不下扫描，却会把
// 「少打两个字符」变成空结果。
//
// origin 与 upstream 两个键都要命中：改名/映射的渠道上两者不同，用户记得住的
// 可能是任意一个。输入里的 % _ ! 一律按字面量转义，不给通配语义 —— 这条路径
// 是「找模型」不是「写 LIKE」。
func applyApiduoTaskModelFilter(query *gorm.DB, modelName string) *gorm.DB {
	originExpr, upstreamExpr, validExpr := apiduoTaskModelJSONExpr(query.Dialector.Name())
	if originExpr == "" {
		return query
	}
	pattern := "%" + strings.ToLower(apiduoTaskModelLikeLiteral(modelName)) + "%"
	return query.Where(
		fmt.Sprintf(
			"(%s AND (LOWER(%s) LIKE ? ESCAPE '!' OR LOWER(%s) LIKE ? ESCAPE '!'))",
			validExpr, originExpr, upstreamExpr,
		),
		pattern, pattern,
	)
}

// apiduoTaskModelJSONExpr 返回三种方言下取 JSON 字段的表达式与有效性判断。
//
// JSON 列在 MySQL/PostgreSQL 上是原生类型、在 SQLite 上是 TEXT，取字段的写法
// 各不相同；MySQL 与 SQLite 还会在脏数据上直接报错（一行坏 JSON 让整页日志
// 500），所以都先过各自的有效性函数再取字段。PostgreSQL 的 json 列由数据库
// 保证合法，不需要额外判断。
func apiduoTaskModelJSONExpr(dialect string) (originExpr, upstreamExpr, validExpr string) {
	switch dialect {
	case "postgres":
		return "properties->>'origin_model_name'",
			"properties->>'upstream_model_name'",
			"properties IS NOT NULL"
	case "mysql":
		return "JSON_UNQUOTE(JSON_EXTRACT(properties, '$.origin_model_name'))",
			"JSON_UNQUOTE(JSON_EXTRACT(properties, '$.upstream_model_name'))",
			"properties IS NOT NULL AND JSON_VALID(properties)"
	case "sqlite":
		return "json_extract(properties, '$.origin_model_name')",
			"json_extract(properties, '$.upstream_model_name')",
			"properties IS NOT NULL AND json_valid(properties)"
	}
	return "", "", ""
}

// apiduoTaskModelLikeLiteral 转义 LIKE 元字符，配合 ESCAPE '!' 使用。
// 先转义转义符自身，否则 "!" 会被后面的替换二次处理。
func apiduoTaskModelLikeLiteral(value string) string {
	value = strings.ReplaceAll(value, "!", "!!")
	value = strings.ReplaceAll(value, "%", "!%")
	value = strings.ReplaceAll(value, "_", "!_")
	return value
}
