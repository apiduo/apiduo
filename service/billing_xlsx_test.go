package service

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// 读取 zip 里的一个部件。
func readXLSXPart(t *testing.T, archive *zip.Reader, name string) string {
	t.Helper()
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer reader.Close()
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(body)
	}
	t.Fatalf("missing part %s", name)
	return ""
}

func TestWriteXLSXStructuresWorkbook(t *testing.T) {
	const detailRows = 1200
	var buffer bytes.Buffer

	sheets := []XLSXSheet{
		{
			Name:    "账单信息",
			Columns: []XLSXColumn{{Title: "项目", Width: 18}, {Title: "值", Width: 32}},
			Rows: func(row *XLSXRowWriter) error {
				if err := row.Row(TextCell("账期"), TextCell("2026-10")); err != nil {
					return err
				}
				// 转义必须落在 XML 里，否则一次请求 ID 带 & 就能让文件打不开。
				return row.Row(TextCell("备注"), TextCell(`a&b <c> "d" 'e'`))
			},
		},
		{
			Name:    "汇总",
			Columns: []XLSXColumn{{Title: "模型", Width: 24}, {Title: "金额", Width: 14}},
			Rows: func(row *XLSXRowWriter) error {
				return row.Row(TextCell("claude-sonnet-4.5"), DecimalCell("1234.567890"))
			},
		},
		{
			Name: "明细",
			Columns: []XLSXColumn{
				{Title: "时间", Width: 20}, {Title: "请求 ID", Width: 36},
				{Title: "输入", Width: 12}, {Title: "金额", Width: 14},
			},
			Rows: func(row *XLSXRowWriter) error {
				for i := range detailRows {
					if err := row.Row(
						TextCell("2026-10-01 00:00:01"),
						TextCell(fmt.Sprintf("req-%d", i)),
						IntCell(int64(i)),
						DecimalCell("0.000123"),
					); err != nil {
						return err
					}
				}
				return nil
			},
		},
	}

	if err := WriteXLSX(&buffer, sheets...); err != nil {
		t.Fatalf("WriteXLSX: %v", err)
	}

	archive, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatalf("output is not a zip: %v", err)
	}

	for _, name := range []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"xl/workbook.xml",
		"xl/_rels/workbook.xml.rels",
		"xl/styles.xml",
		"xl/worksheets/sheet1.xml",
		"xl/worksheets/sheet2.xml",
		"xl/worksheets/sheet3.xml",
	} {
		_ = readXLSXPart(t, archive, name)
	}

	workbook := readXLSXPart(t, archive, "xl/workbook.xml")
	for _, name := range []string{"账单信息", "汇总", "明细"} {
		if !strings.Contains(workbook, `name="`+name+`"`) {
			t.Fatalf("workbook.xml missing sheet %q:\n%s", name, workbook)
		}
	}

	contentTypes := readXLSXPart(t, archive, "[Content_Types].xml")
	if got := strings.Count(contentTypes, "spreadsheetml.worksheet+xml"); got != 3 {
		t.Fatalf("content types declare %d worksheets, want 3", got)
	}

	// cols 必须在 sheetData 之前，否则列宽被忽略。
	detail := readXLSXPart(t, archive, "xl/worksheets/sheet3.xml")
	if strings.Index(detail, "<cols>") > strings.Index(detail, "<sheetData>") {
		t.Fatal("cols must precede sheetData")
	}
	// 表头 + 数据行；少一行就是流式写入丢数据。
	if got := strings.Count(detail, "<row "); got != detailRows+1 {
		t.Fatalf("detail sheet has %d rows, want %d", got, detailRows+1)
	}
	if !strings.Contains(detail, `t="inlineStr"`) {
		t.Fatal("text cells must be inlineStr")
	}
	if !strings.Contains(detail, "<v>0.000123</v>") {
		t.Fatal("decimal cells must keep the exact string")
	}

	info := readXLSXPart(t, archive, "xl/worksheets/sheet1.xml")
	if !strings.Contains(info, `a&amp;b &lt;c&gt; &quot;d&quot; &apos;e&apos;`) {
		t.Fatalf("special characters were not escaped:\n%s", info)
	}
}

func TestWriteXLSXRejectsMoreThanColumnLimit(t *testing.T) {
	columns := make([]XLSXColumn, xlsxMaxColumns+1)
	err := WriteXLSX(io.Discard, XLSXSheet{
		Name:    "too wide",
		Columns: columns,
		Rows:    func(*XLSXRowWriter) error { return nil },
	})
	if err == nil {
		t.Fatal("want an error for more columns than the sheet supports")
	}
}

// 行回调返回错误时不能留下一个看起来完整的文件。
func TestWriteXLSXPropagatesRowError(t *testing.T) {
	want := fmt.Errorf("source read failed")
	err := WriteXLSX(io.Discard, XLSXSheet{
		Name:    "明细",
		Columns: []XLSXColumn{{Title: "时间"}},
		Rows: func(row *XLSXRowWriter) error {
			_ = row.Row(TextCell("2026-10-01 00:00:01"))
			return want
		},
	})
	if err != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}
