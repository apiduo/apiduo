package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 解析出来的工作表：只取本测试需要的字段。
type parsedSheet struct {
	Rows [][]string
}

// readSheet 解开一份 xlsx 里的一张表，按行返回单元格文本。
func readSheet(t *testing.T, archive *zip.Reader, name string) parsedSheet {
	t.Helper()
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		require.NoError(t, err)
		defer reader.Close()
		body, err := io.ReadAll(reader)
		require.NoError(t, err)

		var document struct {
			Rows []struct {
				Cells []struct {
					Type string `xml:"t,attr"`
					Is   struct {
						T string `xml:"t"`
					} `xml:"is"`
					Value string `xml:"v"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		require.NoError(t, xml.Unmarshal(body, &document))

		sheet := parsedSheet{}
		for _, row := range document.Rows {
			cells := make([]string, 0, len(row.Cells))
			for _, cell := range row.Cells {
				if cell.Is.T != "" {
					cells = append(cells, cell.Is.T)
					continue
				}
				cells = append(cells, cell.Value)
			}
			sheet.Rows = append(sheet.Rows, cells)
		}
		return sheet
	}
	t.Fatalf("sheet %s not found", name)
	return parsedSheet{}
}

func openXLSX(t *testing.T, path string) *zip.Reader {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	return archive
}

// seedExportFixture 造一份最小的月度账单：一条消费、一条退款、一条失败。
func seedExportFixture(t *testing.T) (*model.BillingStatement, BillingSnapshot) {
	t.Helper()
	db := setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)

	seedBillingLog(t, db, model.Log{
		UserId: 7, Username: "bob", CreatedAt: start + 60,
		Type: model.LogTypeConsume, ModelName: "claude-sonnet-4.5", Group: "vip",
		TokenName: "sk-bob", RequestId: "req-1", Quota: 100000,
		PromptTokens: 1000, CompletionTokens: 500, UseTimeMs: 321,
		Other: `{"cache_tokens":200,"cache_write_tokens":100,"request_path":"/v1/messages"}`,
	})
	seedBillingLog(t, db, model.Log{
		UserId: 7, Username: "bob", CreatedAt: start + 120,
		Type: model.LogTypeRefund, ModelName: "seedance-1.0-pro", Group: "vip",
		TokenName: "sk-bob", RequestId: "req-2", Quota: 20000,
		Other: `{"task_id":"task-9","request_path":"/v1/videos"}`,
	})
	seedBillingLog(t, db, model.Log{
		UserId: 7, Username: "bob", CreatedAt: start + 180,
		Type: model.LogTypeError, ModelName: "gpt-5.1", Group: "vip",
		TokenName: "sk-bob", RequestId: "req-3", Quota: 0,
		Other: `{"request_path":"/v1/chat/completions"}`,
	})

	snapshot := BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"}
	statement, created, err := BuildBillingStatement(context.Background(), "2026-10", 7, "bob", snapshot)
	require.NoError(t, err)
	require.True(t, created)
	return statement, snapshot
}

// 两份投影都必须是三张表、明细不含失败行、金额与账单头同源。
func TestBillingExportMatchesStatementTotals(t *testing.T) {
	t.Setenv(ExportFileDirEnv, t.TempDir())
	statement, _ := seedExportFixture(t)

	adminReport, err := LoadIssuedBillingReport(context.Background(), statement, BillingProjectionAdmin)
	require.NoError(t, err)
	adminExport, err := GenerateBillingExport(context.Background(), adminReport)
	require.NoError(t, err)

	adminPath, err := ExportFilePath(adminExport.ObjectKey)
	require.NoError(t, err)
	adminArchive := openXLSX(t, adminPath)

	info := readSheet(t, adminArchive, "xl/worksheets/sheet1.xml")
	summary := readSheet(t, adminArchive, "xl/worksheets/sheet2.xml")
	detail := readSheet(t, adminArchive, "xl/worksheets/sheet3.xml")

	// 账单信息：表头 + 若干行 KV，净费用那一行必须等于账单头。
	assert.Equal(t, []string{"项目", "值"}, info.Rows[0])
	assert.Contains(t, info.Rows, []string{"净费用", statement.TotalAmount})
	assert.Contains(t, info.Rows, []string{"内部 quota 合计", "80000"})

	// 汇总：表头 + 两行模型 + 合计行。金额按快照折算。
	assert.Equal(t, "模型", summary.Rows[0][0])
	total := summary.Rows[len(summary.Rows)-1]
	assert.Equal(t, "合计", total[0])
	assert.Equal(t, statement.TotalAmount, total[len(total)-1], "合计金额必须与账单头一致")

	// 明细：失败行不进导出，剩下消费 + 退款。
	assert.Len(t, detail.Rows, 3, "header + consume + refund, error row is excluded")
	assert.Equal(t, []string{"时间", "请求 ID", "类型", "令牌", "模型", "分组", "请求类型", "输入 token",
		"缓存读", "缓存写", "输出 token", "总 token", "Quota", "金额", "任务 ID", "耗时(ms)"}, detail.Rows[0])
	assert.Equal(t, "消费", detail.Rows[1][2])
	assert.Equal(t, "700", detail.Rows[1][7], "输入 token 要扣掉缓存读与缓存写")
	assert.Equal(t, "1500", detail.Rows[1][11], "总 token 为四项之和：700 + 200 + 100 + 500")
	assert.Equal(t, "/v1/messages", detail.Rows[1][6])
	assert.Equal(t, "退款", detail.Rows[2][2])
	assert.Equal(t, "-0.292000", detail.Rows[2][13], "退款金额为负，6 位小数")

	// 客户投影：少 quota、分组、内部折算率。
	customer := *adminReport
	customer.Projection = BillingProjectionCustomer
	customerExport, err := GenerateBillingExport(context.Background(), &customer)
	require.NoError(t, err)
	customerPath, err := ExportFilePath(customerExport.ObjectKey)
	require.NoError(t, err)
	customerArchive := openXLSX(t, customerPath)

	customerInfo := readSheet(t, customerArchive, "xl/worksheets/sheet1.xml")
	for _, row := range customerInfo.Rows {
		require.NotEqual(t, "内部 quota 合计", row[0], "customer projection must not expose internal quota")
	}
	customerDetail := readSheet(t, customerArchive, "xl/worksheets/sheet3.xml")
	assert.Equal(t, []string{"时间", "请求 ID", "类型", "令牌", "模型", "输入 token",
		"缓存读", "缓存写", "输出 token", "总 token", "金额", "任务 ID", "耗时(ms)"}, customerDetail.Rows[0])
	assert.Equal(t, customerExport.Rows, int64(len(customerDetail.Rows)-1))

	// 行数登记与实际内容一致：页头要能给客户看「这份文件有几行」。
	assert.EqualValues(t, 2, adminExport.Rows)
	assert.NotEmpty(t, adminExport.Sha256)
	assert.NotEmpty(t, adminExport.Bytes)
	assert.Contains(t, adminExport.ObjectKey, "BILL-2026-10-u7-admin.xlsx")
}

// 当月没有快照，也要能给出与出账同一口径的读数。
func TestBillingDraftReadsLogsInRealTime(t *testing.T) {
	setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)

	seedBillingLog(t, model.DB, model.Log{
		UserId: 9, Username: "carol", CreatedAt: start + 10,
		Type: model.LogTypeConsume, ModelName: "claude-sonnet-4.5", Group: "default",
		Quota: 50000, PromptTokens: 300, CompletionTokens: 100,
		Other: `{"cache_tokens":50}`,
	})
	seedBillingLog(t, model.DB, model.Log{
		UserId: 9, Username: "carol", CreatedAt: start + 20,
		Type: model.LogTypeRefund, ModelName: "claude-sonnet-4.5", Group: "default",
		Quota: 10000,
	})

	snapshot := BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"}
	report, err := LoadDraftBillingReport(context.Background(), "2026-10", 9, "carol", snapshot, BillingProjectionAdmin)
	require.NoError(t, err)

	assert.True(t, report.IsDraft)
	assert.Equal(t, model.BillingStatusDraft, report.Statement.Status)
	assert.EqualValues(t, 40000, report.Statement.TotalQuota)
	assert.Equal(t, snapshot.AmountOf(40000), report.Statement.TotalAmount)
	require.Len(t, report.Summary, 1)
	// 请求数只数消费与失败；退款是独立一笔，不并进请求数，否则退款率会被稀释。
	assert.EqualValues(t, 1, report.Summary[0].Requests)
	assert.EqualValues(t, 0, report.Summary[0].Errors)
	assert.EqualValues(t, 250, report.Summary[0].InputTokens, "300 - 50 cache read")
	assert.EqualValues(t, 1, report.Summary[0].Refunds)
}

// 翻页必须走完所有行，且同 created_at 的多行不能跳或重。
func TestBillingLinesPageWalksAllRowsWithCursor(t *testing.T) {
	setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)

	// 三行同一秒：cursor 只用 created_at 会漏行，必须带上 id。
	for i := range 3 {
		seedBillingLog(t, model.DB, model.Log{
			UserId: 11, CreatedAt: start + 100, Type: model.LogTypeConsume,
			ModelName: "m", Quota: 100, RequestId: "same-second-" + string(rune('a'+i)),
		})
	}
	seedBillingLog(t, model.DB, model.Log{
		UserId: 11, CreatedAt: start + 200, Type: model.LogTypeConsume,
		ModelName: "m", Quota: 100, RequestId: "later",
	})

	snapshot := BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"}
	report, err := LoadDraftBillingReport(context.Background(), "2026-10", 11, "dave", snapshot, BillingProjectionAdmin)
	require.NoError(t, err)

	var seen []string
	cursor := model.BillingLineCursor{}
	for range 10 {
		rows, next, err := LoadBillingLinesPage(context.Background(), report, model.BillingLineFilter{}, cursor, 2)
		require.NoError(t, err)
		for _, row := range rows {
			seen = append(seen, row.RequestId)
		}
		if !next.Valid {
			break
		}
		cursor = next
	}
	assert.Equal(t, []string{"same-second-a", "same-second-b", "same-second-c", "later"}, seen)
	assert.Equal(t, 4, len(seen))
}

func TestParseBillingLineCursorRejectsGarbage(t *testing.T) {
	assert.False(t, ParseBillingLineCursor("").Valid)
	assert.False(t, ParseBillingLineCursor("garbage").Valid)
	assert.False(t, ParseBillingLineCursor("0_0").Valid)
	assert.False(t, ParseBillingLineCursor("100_").Valid)
	assert.False(t, ParseBillingLineCursor("100_abc").Valid)

	parsed := ParseBillingLineCursor("1700000000_42")
	assert.True(t, parsed.Valid)
	assert.EqualValues(t, 1700000000, parsed.CreatedAt)
	assert.Equal(t, 42, parsed.Id)
	assert.Equal(t, "1700000000_42", FormatBillingLineCursor(parsed))
}

// 下载链路按段白名单校验 key，账单文件名必须落在三段以内。
func TestBillingExportKeyStaysWithinThreeSegments(t *testing.T) {
	key := BillingExportKey("2026-10", 88, 7, BillingProjectionCustomer)
	assert.Equal(t, "billing/2026-10/BILL-2026-10-u7-customer.xlsx", key)
	assert.Equal(t, 3, strings.Count(key, "/")+1)

	draft := BillingExportKey("2026-11", 0, 7, BillingProjectionAdmin)
	assert.Equal(t, "billing/2026-11/BILL-2026-11-u7-draft-admin.xlsx", draft)

	_, err := normalizeExportKey(key)
	assert.NoError(t, err, "归档 key 必须能通过下载端点的路径校验")
	_, err = normalizeExportKey(draft)
	assert.NoError(t, err)
}

// 页大小越界要夹到上界，而不是回落到默认 50：传 200 被服务层多取一行变成 201，
// 下游若再按 <=200 判一次，用户只会拿到 50 行且 next_cursor 为空 —— 静默截断。
func TestNormalizeBillingLinePageSize(t *testing.T) {
	assert.Equal(t, 50, NormalizeBillingLinePageSize(0))
	assert.Equal(t, 50, NormalizeBillingLinePageSize(-3))
	assert.Equal(t, 1, NormalizeBillingLinePageSize(1))
	assert.Equal(t, 200, NormalizeBillingLinePageSize(200))
	assert.Equal(t, 200, NormalizeBillingLinePageSize(201))
	assert.Equal(t, 200, NormalizeBillingLinePageSize(5000))
}

// ClickHouse 那边 logs.id 恒为 0，游标只能落在 request_id 上。
// 编码要能原样往返，且不和历史的 `<ts>_<id>` 混淆。
func TestParseBillingLineCursorRoundTripsRequestId(t *testing.T) {
	cursor := model.BillingLineCursor{
		Valid:     true,
		CreatedAt: 1700000000,
		RequestId: "202611010553185537899618268d9d6yzJAUWTn",
	}
	encoded := FormatBillingLineCursor(cursor)
	assert.Equal(t, "r.1700000000.202611010553185537899618268d9d6yzJAUWTn", encoded)

	parsed := ParseBillingLineCursor(encoded)
	assert.True(t, parsed.Valid)
	assert.EqualValues(t, 1700000000, parsed.CreatedAt)
	assert.Equal(t, cursor.RequestId, parsed.RequestId)
	assert.Equal(t, 0, parsed.Id, "request_id 游标不该带 id")

	assert.False(t, ParseBillingLineCursor("r.0.abc").Valid)
	assert.False(t, ParseBillingLineCursor("r.1700000000.").Valid)
	assert.False(t, ParseBillingLineCursor("r.1700000000").Valid)
}

// 同秒多行在 request_id 模式下必须既不重也不漏。
func TestBillingLinesCursorByRequestIdTieBreak(t *testing.T) {
	setupBillingTestDB(t)
	start, end, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)
	for i := range 3 {
		seedBillingLog(t, model.DB, model.Log{
			UserId: 12, CreatedAt: start + 100, Type: model.LogTypeConsume,
			ModelName: "m", Quota: 100, RequestId: "rid-" + string(rune('a'+i)),
		})
	}
	seedBillingLog(t, model.DB, model.Log{
		UserId: 12, CreatedAt: start + 200, Type: model.LogTypeConsume,
		ModelName: "m", Quota: 100, RequestId: "rid-z",
	})

	rows, err := model.ListUserLogsForBilling(context.Background(), 12, start, end,
		model.BillingLineFilter{},
		model.BillingLineCursor{Valid: true, CreatedAt: start + 100, RequestId: "rid-a"}, 10)
	require.NoError(t, err)

	var seen []string
	for _, row := range rows {
		seen = append(seen, row.RequestId)
	}
	assert.Equal(t, []string{"rid-b", "rid-c", "rid-z"}, seen)
}

// 一页要能拿满 200 行（旧实现里服务层多取一行触发下游 <=200 的兜底，只回 50 行）。
func TestBillingLinesPageHonoursLargePageSize(t *testing.T) {
	setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)
	for i := range 60 {
		seedBillingLog(t, model.DB, model.Log{
			UserId: 13, CreatedAt: start + 100 + int64(i), Type: model.LogTypeConsume,
			ModelName: "m", Quota: 100, RequestId: "rid-" + strconv.Itoa(i),
		})
	}

	snapshot := BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"}
	report, err := LoadDraftBillingReport(context.Background(), "2026-10", 13, "erin", snapshot, BillingProjectionAdmin)
	require.NoError(t, err)

	rows, next, err := LoadBillingLinesPage(context.Background(), report, model.BillingLineFilter{}, model.BillingLineCursor{}, 200)
	require.NoError(t, err)
	assert.Len(t, rows, 60)
	assert.False(t, next.Valid)
}

// 已出账明细与当月明细共用同一个上界：调用方多取一行，所以 201 必须放行。
// 旧实现按 >200 打回 50，page_size=200 会静默只给 50 行且 next_cursor 为空。
func TestIssuedBillingLinesHonoursLargePageSize(t *testing.T) {
	db := setupBillingTestDB(t)
	statement := model.BillingStatement{
		Period: "2026-09", UserId: 14, Username: "frank",
		Status: model.BillingStatusIssued, PeriodStart: 1, PeriodEnd: 2,
	}
	require.NoError(t, db.Create(&statement).Error)
	for i := range 60 {
		line := model.BillingStatementLine{
			StatementId: statement.Id, UserId: 14, CreatedAt: int64(1000 + i),
			RequestId: "rid-" + strconv.Itoa(i), Type: model.LogTypeConsume,
			Amount: "0.000100", Quota: 100,
		}
		require.NoError(t, db.Create(&line).Error)
	}

	rows, err := model.ListBillingStatementLinesAfter(statement.Id, model.BillingLineFilter{}, model.BillingLineCursor{}, model.BillingLinePageSizeMax+1)
	require.NoError(t, err)
	assert.Len(t, rows, 60)
}

// 已出账明细分页按 (created_at, id) 前进：同秒多行既不重也不漏。
func TestIssuedBillingLinesSameSecondCursor(t *testing.T) {
	db := setupBillingTestDB(t)
	statement := model.BillingStatement{
		Period: "2026-07", UserId: 16, Username: "gina",
		Status: model.BillingStatusIssued, PeriodStart: 1, PeriodEnd: 2,
	}
	require.NoError(t, db.Create(&statement).Error)
	for _, ts := range []int64{1000, 1000, 1000, 1100} {
		require.NoError(t, db.Create(&model.BillingStatementLine{
			StatementId: statement.Id, UserId: 16, CreatedAt: ts,
			Type: model.LogTypeConsume, Amount: "0.000100", Quota: 100,
		}).Error)
	}

	var seen []int64
	cursor := model.BillingLineCursor{}
	for range 5 {
		page, err := model.ListBillingStatementLinesAfter(statement.Id, model.BillingLineFilter{}, cursor, 2)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		for _, line := range page {
			seen = append(seen, line.CreatedAt)
		}
		last := page[len(page)-1]
		cursor = model.BillingLineCursor{Valid: true, CreatedAt: last.CreatedAt, Id: last.Id}
	}
	assert.Equal(t, []int64{1000, 1000, 1000, 1100}, seen)
}

// 用户被删除后 users 表查不到名字，但账单还在：代客查看要能继续。
func TestBillingStatementExistsForUser(t *testing.T) {
	db := setupBillingTestDB(t)
	require.NoError(t, db.Create(&model.BillingStatement{
		Period: "2026-08", UserId: 15, Username: "user#15",
		Status: model.BillingStatusIssued, PeriodStart: 1, PeriodEnd: 2,
	}).Error)

	exists, err := model.BillingStatementExistsForUser(context.Background(), 15, 0, 0)
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = model.BillingStatementExistsForUser(context.Background(), 999999, 0, 0)
	require.NoError(t, err)
	assert.False(t, exists)
}

// 当月还没出账时 billing_statements 是空的，只看那张表会把「有 60 万次请求
// 但用户已被删除」判成不存在 —— 线上 user_id=64 就踩过这个。
func TestBillingStatementExistsForUserDraftMonth(t *testing.T) {
	db := setupBillingTestDB(t)
	require.NoError(t, db.Create(&model.BillingStatement{
		Period: "2026-08", UserId: 64, Username: "user#64",
		Status: model.BillingStatusIssued, PeriodStart: 1, PeriodEnd: 2,
	}).Error)
	require.NoError(t, db.Create(&model.Log{
		UserId: 65, CreatedAt: 500, Type: model.LogTypeConsume, ModelName: "gpt-4o",
	}).Error)

	// 65 没有已出账快照，但 2026-11 有当月用量 → 仍在。
	exists, err := model.BillingStatementExistsForUser(context.Background(), 65, 400, 600)
	require.NoError(t, err)
	assert.True(t, exists)

	// 同一用户换到没有用量的账期 → 不存在。
	exists, err = model.BillingStatementExistsForUser(context.Background(), 65, 1000, 2000)
	require.NoError(t, err)
	assert.False(t, exists)

	// 已出账快照仍然优先，且不需要账期边界。
	exists, err = model.BillingStatementExistsForUser(context.Background(), 64, 0, 0)
	require.NoError(t, err)
	assert.True(t, exists)
}
