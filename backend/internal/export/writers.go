// Package export 生成清理/扫描结果的导出文件。
//
// XLSX 采用最小 OOXML 手写实现（archive/zip + 固定 XML），避免引入第三方库，
// 保证单二进制部署时体积与依赖可控；生成的文件 Excel / WPS / LibreOffice 均可打开。
package export

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Row 是导出的一行数据。
type Row struct {
	ScanTime   int64  `json:"scan_time"`
	CleanTime  int64  `json:"clean_time"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Kind       string `json:"media_kind"`
	GroupNo    string `json:"group_no"`
	KeeperPath string `json:"keeper_path"`
	Reason     string `json:"reason"`
	Rule       string `json:"rule"`
	Assoc      string `json:"assoc"`
	Released   int64  `json:"released"`
	Result     string `json:"result"`
	Error      string `json:"error"`
}

// Columns 定义列顺序与表头。
var Columns = []struct {
	Key   string
	Title string
}{
	{"scan_time", "扫描时间"},
	{"clean_time", "清理时间"},
	{"path", "原始路径"},
	{"name", "文件名"},
	{"size", "大小(字节)"},
	{"media_kind", "类型"},
	{"group_no", "重复组号"},
	{"keeper_path", "保留文件"},
	{"reason", "删除原因"},
	{"rule", "命中规则"},
	{"assoc", "关联文件"},
	{"released", "释放空间(字节)"},
	{"result", "执行结果"},
	{"error", "错误信息"},
}

func value(r Row, key string) string {
	switch key {
	case "scan_time":
		return formatTime(r.ScanTime)
	case "clean_time":
		return formatTime(r.CleanTime)
	case "path":
		return r.Path
	case "name":
		return r.Name
	case "size":
		return strconv.FormatInt(r.Size, 10)
	case "media_kind":
		return r.Kind
	case "group_no":
		return r.GroupNo
	case "keeper_path":
		return r.KeeperPath
	case "reason":
		return r.Reason
	case "rule":
		return r.Rule
	case "assoc":
		return r.Assoc
	case "released":
		return strconv.FormatInt(r.Released, 10)
	case "result":
		return r.Result
	case "error":
		return r.Error
	}
	return ""
}

func formatTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// Write 根据扩展名选择格式写出文件，返回实际路径与行数。
func Write(dir, base string, format string, rows []Row) (string, int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	name := sanitize(base) + "-" + time.Now().Format("20060102-150405")
	switch strings.ToLower(format) {
	case "csv":
		p := filepath.Join(dir, name+".csv")
		return p, len(rows), writeCSV(p, rows)
	case "json":
		p := filepath.Join(dir, name+".json")
		return p, len(rows), writeJSON(p, rows)
	case "html":
		p := filepath.Join(dir, name+".html")
		return p, len(rows), writeHTML(p, rows, base)
	case "xlsx", "excel":
		p := filepath.Join(dir, name+".xlsx")
		return p, len(rows), writeXLSX(p, rows)
	}
	return "", 0, fmt.Errorf("不支持的导出格式: %s", format)
}

func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "export"
	}
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-",
		"\"", "-", "<", "-", ">", "-", "|", "-", " ", "_")
	return repl.Replace(s)
}

func writeCSV(path string, rows []Row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// UTF-8 BOM：Excel 中文不乱码
	if _, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	w := csv.NewWriter(f)
	header := make([]string, len(Columns))
	for i, c := range Columns {
		header[i] = c.Title
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, r := range rows {
		line := make([]string, len(Columns))
		for i, c := range Columns {
			line[i] = value(r, c.Key)
		}
		if err := w.Write(line); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func writeJSON(path string, rows []Row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{
		"generated_at": time.Now().Format(time.RFC3339),
		"count":        len(rows),
		"rows":         rows,
	})
}

func writeHTML(path string, rows []Row, title string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<title>MediaCleaner 报告 - " + html.EscapeString(title) + "</title>")
	b.WriteString(`<style>
body{font-family:-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;margin:0;padding:32px;background:#f6f7f9;color:#1c1f23}
h1{font-size:22px;margin:0 0 6px}
.meta{color:#6b7280;font-size:13px;margin-bottom:20px}
table{width:100%;border-collapse:collapse;background:#fff;border-radius:10px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,.08);font-size:13px}
th{background:#eef1f5;text-align:left;padding:10px 12px;font-weight:600;white-space:nowrap}
td{padding:9px 12px;border-top:1px solid #eef0f3;word-break:break-all}
tr:hover td{background:#fafbfc}
.mono{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px}
.tag{display:inline-block;padding:1px 8px;border-radius:999px;background:#eef2ff;color:#3b4cca;font-size:12px}
.sum{margin:18px 0 10px;color:#374151;font-size:14px}
</style></head><body>`)
	b.WriteString("<h1>MediaCleaner Pro 清理报告</h1>")
	b.WriteString("<div class=\"meta\">生成时间 " + time.Now().Format("2006-01-02 15:04:05") +
		" · 共 " + strconv.Itoa(len(rows)) + " 条记录</div>")
	var total int64
	for _, r := range rows {
		total += r.Released
	}
	b.WriteString("<div class=\"sum\">预计/实际释放空间：<b>" + humanSize(total) + "</b></div>")
	b.WriteString("<table><thead><tr>")
	for _, c := range Columns {
		b.WriteString("<th>" + html.EscapeString(c.Title) + "</th>")
	}
	b.WriteString("</tr></thead><tbody>")
	for _, r := range rows {
		b.WriteString("<tr>")
		for _, c := range Columns {
			v := html.EscapeString(value(r, c.Key))
			if c.Key == "media_kind" && v != "" {
				v = "<span class=\"tag\">" + v + "</span>"
			}
			if c.Key == "path" || c.Key == "keeper_path" {
				v = "<span class=\"mono\">" + v + "</span>"
			}
			b.WriteString("<td>" + v + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table></body></html>")
	_, err = io.WriteString(f, b.String())
	return err
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %s", float64(n)/float64(div), units[exp])
}

// ---------- 最小 XLSX 实现 ----------

func writeXLSX(path string, rows []Row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	files := []struct {
		name string
		body string
	}{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", relsXML},
		{"docProps/app.xml", appXML},
		{"docProps/core.xml", fmt.Sprintf(coreXML, now, now)},
		{"xl/workbook.xml", workbookXML},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML},
		{"xl/styles.xml", stylesXML},
		{"xl/worksheets/sheet1.xml", buildSheet(rows)},
	}
	for _, item := range files {
		w, err := zw.Create(item.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, item.body); err != nil {
			return err
		}
	}
	return zw.Close()
}

func buildSheet(rows []Row) string {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetPr><outlinePr summaryBelow="1"/></sheetPr>`)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0" tabSelected="1"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	b.WriteString(`<cols>`)
	widths := []int{20, 20, 60, 32, 12, 10, 12, 60, 36, 24, 40, 14, 12, 30}
	for i, w := range widths {
		b.WriteString(fmt.Sprintf(`<col min="%d" max="%d" width="%d" customWidth="1"/>`, i+1, i+1, w))
	}
	b.WriteString(`</cols><sheetData>`)

	// 表头
	b.WriteString(`<row r="1" s="1" customFormat="1">`)
	for i, c := range Columns {
		ref := colName(i+1) + "1"
		b.WriteString(`<c r="` + ref + `" t="inlineStr" s="2"><is><t>` + xmlEscape(c.Title) + `</t></is></c>`)
	}
	b.WriteString(`</row>`)

	for ri, r := range rows {
		rowNum := ri + 2
		b.WriteString(fmt.Sprintf(`<row r="%d">`, rowNum))
		for ci, c := range Columns {
			v := value(r, c.Key)
			ref := colName(ci+1) + strconv.Itoa(rowNum)
			if c.Key == "size" || c.Key == "released" {
				b.WriteString(`<c r="` + ref + `"><v>` + v + `</v></c>`)
			} else {
				b.WriteString(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">` +
					xmlEscape(v) + `</t></is></c>`)
			}
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData><autoFilter ref="A1:` + colName(len(Columns)) + strconv.Itoa(len(rows)+1) + `"/></worksheet>`)
	return b.String()
}

func colName(n int) string {
	name := ""
	for n > 0 {
		n--
		name = string(rune('A'+n%26)) + name
		n /= 26
	}
	return name
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
</Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>`

const appXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties">
<Application>MediaCleaner Pro</Application>
</Properties>`

const coreXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<dcterms:created xsi:type="dcterms:W3CDTF">%s</dcterms:created>
<dcterms:modified xsi:type="dcterms:W3CDTF">%s</dcterms:modified>
</cp:coreProperties>`

const workbookXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="清理清单" sheetId="1" r:id="rId1"/></sheets>
</workbook>`

const workbookRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

const stylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2">
<font><sz val="11"/><name val="Calibri"/></font>
<font><b/><sz val="11"/><name val="Calibri"/></font>
</fonts>
<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="3">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>
<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>
</cellXfs>
</styleSheet>`
