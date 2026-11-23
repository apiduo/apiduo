package service

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// 最小可用的流式 xlsx 写入器。
//
// 为什么不用 excelize：账单导出只在出账任务与当月手动导出时跑，两种场景都是
// 「顺序写、写完不再改」。xlsx 本身是一个 zip 包着几个 XML，顺序写不需要
// 任何内存模型；引入一个第三方库只为了拼这几个 XML，代价是 go.mod 多一条
// 直接依赖与若干间接依赖 —— 这个仓库要和上游反复合并，那是最贵的部分。
//
// 能力边界写清楚，避免以后有人拿它当通用表格库：
//   - 只写字符串、整数、定点小数字符串；不写公式、图片、合并单元格。
//   - 只有三张表的样式：默认、加粗表头（带底色）、千分位整数、两位小数。
//   - 单元格用 inlineStr 而不是共享字符串表：共享表要求先扫一遍全部行，
//     与流式写入互斥；代价是文件大一些，deflate 之后差距很小。
//   - 列数上限 26（A–Z）。账单最宽的一张表是 14 列。

const xlsxMaxColumns = 26

// 样式下标，与 styles.xml 里 cellXfs 的顺序一一对应。
const (
	xlsxStyleDefault = 0
	xlsxStyleHeader  = 1
	xlsxStyleInteger = 2
	xlsxStyleDecimal = 3
)

// XLSXCellKind 决定单元格写进 XML 的形态。
type XLSXCellKind int

const (
	// XLSXText 是字符串，Excel 里左对齐、不参与计算。
	XLSXText XLSXCellKind = iota
	// XLSXInt 是整数，带千分位显示。
	XLSXInt
	// XLSXDecimal 是已经格式化好的定点小数字符串。保留字符串而不是 float64，
	// 是为了让「金额在 Go 里怎么算的」与「文件里写的是什么」逐位一致。
	XLSXDecimal
)

// XLSXCell 是一个单元格的值。
type XLSXCell struct {
	Kind XLSXCellKind
	Text string
	Int  int64
}

// Text 构造一个字符串单元格。
func TextCell(value string) XLSXCell { return XLSXCell{Kind: XLSXText, Text: value} }

// IntCell 构造一个整数单元格。
func IntCell(value int64) XLSXCell { return XLSXCell{Kind: XLSXInt, Int: value} }

// DecimalCell 构造一个定点小数单元格，value 必须是能直接写进 XML 的十进制串。
func DecimalCell(value string) XLSXCell { return XLSXCell{Kind: XLSXDecimal, Text: value} }

// XLSXColumn 是一列的表头与宽度。宽度按字符估算，中文按 2 个字符算。
type XLSXColumn struct {
	Title string
	Width float64
}

// XLSXSheet 是一张工作表。Rows 按行回调，写完即释放，不保留任何行数据。
type XLSXSheet struct {
	Name    string
	Columns []XLSXColumn
	Rows    func(row *XLSXRowWriter) error
}

// XLSXRowWriter 是 Rows 回调里的写入端。只允许顺序追加行。
type XLSXRowWriter struct {
	out     *bufio.Writer
	columns int
	row     int
	err     error
}

// Row 追加一行。cells 少于列数时后面的单元格留空，多于列数时忽略多余部分
// —— 这两种情况都是调用方的错，但不该让整份账单生成失败。
func (w *XLSXRowWriter) Row(cells ...XLSXCell) error {
	if w.err != nil {
		return w.err
	}
	w.row++
	if _, err := fmt.Fprintf(w.out, `<row r="%d">`, w.row); err != nil {
		w.err = err
		return err
	}
	for i := range w.columns {
		var cell XLSXCell
		if i < len(cells) {
			cell = cells[i]
		}
		ref := columnName(i) + strconv.Itoa(w.row)
		if err := writeCell(w.out, ref, cell); err != nil {
			w.err = err
			return err
		}
	}
	_, err := w.out.WriteString("</row>")
	w.err = err
	return err
}

// WriteXLSX 把若干工作表顺序写进 w。整个过程中内存占用与行数无关。
func WriteXLSX(w io.Writer, sheets ...XLSXSheet) error {
	if len(sheets) == 0 {
		return fmt.Errorf("xlsx: no sheets")
	}
	for _, sheet := range sheets {
		if len(sheet.Columns) > xlsxMaxColumns {
			return fmt.Errorf("xlsx: sheet %q has %d columns, limit is %d", sheet.Name, len(sheet.Columns), xlsxMaxColumns)
		}
		if sheet.Rows == nil {
			return fmt.Errorf("xlsx: sheet %q has no rows callback", sheet.Name)
		}
	}

	archive := zip.NewWriter(w)
	if err := writeStaticPart(archive, "[Content_Types].xml", xlsxContentTypes(len(sheets))); err != nil {
		return err
	}
	if err := writeStaticPart(archive, "_rels/.rels", xlsxRootRels); err != nil {
		return err
	}
	if err := writeStaticPart(archive, "xl/workbook.xml", xlsxWorkbook(sheets)); err != nil {
		return err
	}
	if err := writeStaticPart(archive, "xl/_rels/workbook.xml.rels", xlsxWorkbookRels(len(sheets))); err != nil {
		return err
	}
	if err := writeStaticPart(archive, "xl/styles.xml", xlsxStyles); err != nil {
		return err
	}
	for i, sheet := range sheets {
		if err := writeWorksheet(archive, i+1, sheet); err != nil {
			return err
		}
	}
	return archive.Close()
}

// writeStaticPart 写一个体积很小的固定/半固定 XML 部件。
func writeStaticPart(archive *zip.Writer, name, body string) error {
	part, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return err
	}
	_, err = io.WriteString(part, body)
	return err
}

// writeWorksheet 流式写一张表：列宽 → 表头 → 数据行。
func writeWorksheet(archive *zip.Writer, index int, sheet XLSXSheet) error {
	part, err := archive.CreateHeader(&zip.FileHeader{
		Name:   fmt.Sprintf("xl/worksheets/sheet%d.xml", index),
		Method: zip.Deflate,
	})
	if err != nil {
		return err
	}
	out := bufio.NewWriterSize(part, 64*1024)
	if _, err := io.WriteString(out, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`+
		`<sheetViews><sheetView workbookViewId="0">`+
		`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`+
		`</sheetView></sheetViews>`); err != nil {
		return err
	}

	writer := &XLSXRowWriter{out: out, columns: len(sheet.Columns)}
	// XML 里 cols 必须早于 sheetData，否则 Excel 会忽略列宽。
	if len(sheet.Columns) > 0 {
		if _, err := io.WriteString(out, "<cols>"); err != nil {
			return err
		}
		for i, column := range sheet.Columns {
			width := column.Width
			if width <= 0 {
				width = 14
			}
			if _, err := fmt.Fprintf(out, `<col min="%d" max="%d" width="%s" customWidth="1"/>`,
				i+1, i+1, strconv.FormatFloat(width, 'f', 1, 64)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(out, "</cols>"); err != nil {
			return err
		}
	}

	if _, err := io.WriteString(out, "<sheetData>"); err != nil {
		return err
	}

	header := make([]XLSXCell, len(sheet.Columns))
	for i, column := range sheet.Columns {
		header[i] = XLSXCell{Kind: XLSXText, Text: column.Title}
	}
	// 表头固定用加粗样式，与数据行的样式下标不同，单独写一遍。
	if err := writeHeaderRow(out, header); err != nil {
		return err
	}

	writer.row = 1
	if err := sheet.Rows(writer); err != nil {
		return err
	}
	if _, err := io.WriteString(out, `</sheetData></worksheet>`); err != nil {
		return err
	}
	return out.Flush()
}

// writeHeaderRow 写第一行并套用表头样式。
func writeHeaderRow(out *bufio.Writer, cells []XLSXCell) error {
	if _, err := io.WriteString(out, `<row r="1" ht="20" customHeight="1">`); err != nil {
		return err
	}
	for i, cell := range cells {
		if _, err := fmt.Fprintf(out, `<c r="%s1" t="inlineStr" s="%d"><is><t xml:space="preserve">`,
			columnName(i), xlsxStyleHeader); err != nil {
			return err
		}
		if err := writeEscaped(out, cell.Text); err != nil {
			return err
		}
		if _, err := io.WriteString(out, `</t></is></c>`); err != nil {
			return err
		}
	}
	_, err := io.WriteString(out, "</row>")
	return err
}

// writeCell 按类型写一个数据单元格。
func writeCell(out *bufio.Writer, ref string, cell XLSXCell) error {
	switch cell.Kind {
	case XLSXInt:
		_, err := fmt.Fprintf(out, `<c r="%s" s="%d"><v>%d</v></c>`, ref, xlsxStyleInteger, cell.Int)
		return err
	case XLSXDecimal:
		if _, err := fmt.Fprintf(out, `<c r="%s" s="%d"><v>`, ref, xlsxStyleDecimal); err != nil {
			return err
		}
		if err := writeEscaped(out, cell.Text); err != nil {
			return err
		}
		_, err := io.WriteString(out, "</v></c>")
		return err
	default:
		if _, err := fmt.Fprintf(out, `<c r="%s" t="inlineStr" s="%d"><is><t xml:space="preserve">`,
			ref, xlsxStyleDefault); err != nil {
			return err
		}
		if err := writeEscaped(out, cell.Text); err != nil {
			return err
		}
		_, err := io.WriteString(out, `</t></is></c>`)
		return err
	}
}

// writeEscaped 按 XML 规则转义。手动替换而不是调 xml.EscapeText，
// 是因为后者会把换行也转义成实体，导出的单元格里出现 &#xA; 反而难看。
func writeEscaped(out *bufio.Writer, value string) error {
	if !strings.ContainsAny(value, `&<>"'`) {
		_, err := out.WriteString(value)
		return err
	}
	for _, r := range value {
		var entity string
		switch r {
		case '&':
			entity = "&amp;"
		case '<':
			entity = "&lt;"
		case '>':
			entity = "&gt;"
		case '"':
			entity = "&quot;"
		case '\'':
			entity = "&apos;"
		}
		if entity == "" {
			if _, err := out.WriteRune(r); err != nil {
				return err
			}
			continue
		}
		if _, err := out.WriteString(entity); err != nil {
			return err
		}
	}
	return nil
}

// columnName 把 0 基列下标转成 A..Z。
func columnName(index int) string {
	if index < 0 || index >= xlsxMaxColumns {
		return "A"
	}
	return string(rune('A' + index))
}

func xlsxContentTypes(sheetCount int) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	for i := range sheetCount {
		fmt.Fprintf(&builder, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1)
	}
	builder.WriteString(`</Types>`)
	return builder.String()
}

const xlsxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

func xlsxWorkbook(sheets []XLSXSheet) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, sheet := range sheets {
		fmt.Fprintf(&builder, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`,
			escapeXMLString(sheet.Name), i+1, i+1)
	}
	builder.WriteString(`</sheets></workbook>`)
	return builder.String()
}

func xlsxWorkbookRels(sheetCount int) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := range sheetCount {
		fmt.Fprintf(&builder, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
	}
	// styles 的关系 id 必须排在所有 worksheet 之后，编号连续。
	fmt.Fprintf(&builder, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, sheetCount+1)
	builder.WriteString(`</Relationships>`)
	return builder.String()
}

const xlsxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<numFmts count="2">` +
	`<numFmt numFmtId="164" formatCode="#,##0"/>` +
	`<numFmt numFmtId="165" formatCode="#,##0.00"/>` +
	`</numFmts>` +
	`<fonts count="2">` +
	`<font><sz val="11"/><name val="Calibri"/></font>` +
	`<font><b/><sz val="11"/><color rgb="FF1F2937"/><name val="Calibri"/></font>` +
	`</fonts>` +
	`<fills count="3">` +
	`<fill><patternFill patternType="none"/></fill>` +
	`<fill><patternFill patternType="gray125"/></fill>` +
	`<fill><patternFill patternType="solid"><fgColor rgb="FFF1F5F9"/><bgColor indexed="64"/></patternFill></fill>` +
	`</fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="4">` +
	`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/>` +
	`<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`</cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`

// escapeXMLString 用于属性值这类短文本。
func escapeXMLString(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}
