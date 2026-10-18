package rsxdiag

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const ReportBudget = int64(256 << 20)
const MaxReportInput = int64(64 << 20)

type ReportInfo struct {
	ID       string            `json:"id"`
	Created  time.Time         `json:"created"`
	Bytes    int64             `json:"bytes"`
	SHA256   string            `json:"sha256"`
	Redacted bool              `json:"redacted"`
	Partial  bool              `json:"partial"`
	Missing  []string          `json:"missing"`
	Files    map[string]string `json:"files"`
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func reportDir(root string) (string, error) {
	dir := filepath.Join(root, "reports")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := privateDirectory(dir); err != nil {
		return "", err
	}
	return dir, nil
}
func safeReportName(name string) bool {
	return name != "" && !strings.HasPrefix(name, "/") && !strings.Contains(name, "\\") && !strings.Contains(name, ":") && filepath.Clean(name) == name && !strings.Contains(name, "..")
}
func WriteReport(root, id string, files map[string][]byte, summary interface{}, missing []string) (ReportInfo, error) {
	info := ReportInfo{ID: id, Created: time.Now().UTC(), Redacted: false, Partial: len(missing) > 0, Missing: missing, Files: map[string]string{}}
	if !validID(id) {
		return info, errors.New("invalid report identifier")
	}
	dir, err := reportDir(root)
	if err != nil {
		return info, err
	}
	var total int64
	for name, data := range files {
		if !safeReportName(name) {
			return info, errors.New("unsafe report entry")
		}
		total += int64(len(data))
		if total > MaxReportInput {
			return info, errors.New("report input exceeds 64 MiB cap")
		}
		sum := sha256.Sum256(data)
		info.Files[name] = hex.EncodeToString(sum[:])
	}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return info, err
	}
	filesCopy := make(map[string][]byte, len(files)+3)
	for k, v := range files {
		filesCopy[k] = v
	}
	filesCopy["summary.json"] = raw
	html, err := renderReport(info, string(raw))
	if err != nil {
		return info, err
	}
	filesCopy["report.html"] = html
	total += int64(len(raw) + len(html))
	if total > MaxReportInput {
		return info, errors.New("report including rendered summary exceeds 64 MiB cap")
	}
	for name, data := range map[string][]byte{"summary.json": raw, "report.html": html} {
		sum := sha256.Sum256(data)
		info.Files[name] = hex.EncodeToString(sum[:])
	}
	manifest, _ := json.MarshalIndent(info, "", "  ")
	filesCopy["manifest.json"] = manifest
	tmp, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return info, err
	}
	tempName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return info, err
	}
	hash := sha256.New()
	archive := zip.NewWriter(io.MultiWriter(tmp, hash))
	names := make([]string, 0, len(filesCopy))
	for name := range filesCopy {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := archive.Create(name)
		if err != nil {
			return info, err
		}
		if _, err := entry.Write(filesCopy[name]); err != nil {
			return info, err
		}
	}
	if err := archive.Close(); err != nil {
		return info, err
	}
	if err := tmp.Sync(); err != nil {
		return info, err
	}
	stat, err := tmp.Stat()
	if err != nil {
		return info, err
	}
	info.Bytes = stat.Size()
	info.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err := tmp.Close(); err != nil {
		return info, err
	}
	dest := filepath.Join(dir, id+".zip")
	if _, err := os.Lstat(dest); err == nil {
		return info, errors.New("report id already exists")
	}
	if err := os.Rename(tempName, dest); err != nil {
		return info, err
	}
	committed = true
	if err := writeJSONAtomic(filepath.Join(dir, id+".json"), info); err != nil {
		return info, err
	}
	if err := writePrivate(filepath.Join(dir, id+".html"), html); err != nil {
		return info, err
	}
	_ = PruneReports(root, 30, ReportBudget)
	return info, nil
}
func writePrivate(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing nonregular diagnostic file")
	}
	return os.WriteFile(path, data, 0600)
}
func writeJSONAtomic(path string, v interface{}) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".diagnostics-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe diagnostics file")
	}
	return os.Rename(name, path)
}
func WriteSettings(root string, v interface{}) error {
	return writeJSONAtomic(filepath.Join(root, "settings.json"), v)
}
func ReadSettings(root string, out interface{}) error {
	path := filepath.Join(root, "settings.json")
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return errors.New("diagnostic settings too large")
	}
	return json.Unmarshal(raw, out)
}
func WriteAlerts(root string, v interface{}) error {
	return writeJSONAtomic(filepath.Join(root, "alerts.json"), v)
}
func ReadAlerts(root string) ([]Alert, error) {
	f, err := os.Open(filepath.Join(root, "alerts.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out struct {
		Events []Alert `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Events, nil
}
func ListReports(root string) ([]ReportInfo, error) {
	dir, err := reportDir(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []ReportInfo{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || !validID(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var report ReportInfo
		if json.Unmarshal(raw, &report) == nil {
			out = append(out, report)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}
func ReportPath(root, id, format string) (string, error) {
	if !validID(id) || (format != "zip" && format != "html") {
		return "", errors.New("invalid report request")
	}
	dir, err := reportDir(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+"."+format)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe report file")
	}
	return path, nil
}
func DeleteReport(root, id string) error {
	if !validID(id) {
		return errors.New("invalid report ID")
	}
	dir, err := reportDir(root)
	if err != nil {
		return err
	}
	for _, ext := range []string{".zip", ".json", ".html"} {
		path := filepath.Join(dir, id+ext)
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("unsafe report file")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}
func PruneReports(root string, days int, budget int64) error {
	reports, err := ListReports(root)
	if err != nil {
		return err
	}
	var total int64
	for _, r := range reports {
		total += r.Bytes
	}
	cut := time.Now().AddDate(0, 0, -days)
	for i := len(reports) - 1; i >= 0; i-- {
		r := reports[i]
		if r.Created.Before(cut) || total > budget {
			if err := DeleteReport(root, r.ID); err != nil {
				return err
			}
			total -= r.Bytes
		}
	}
	return nil
}
func renderReport(info ReportInfo, summary string) ([]byte, error) {
	const source = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>RSX3 Diagnostics</title><style>body{font:16px system-ui;max-width:1100px;margin:36px auto;padding:20px;color:#233;background:#f8fafb}h1{font-size:30px}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:white;border:1px solid #ddd;padding:20px}small{color:#667}</style><h1>RSX3 Diagnostics</h1><p>报告 {{.Info.ID}} · {{.Info.Created}}</p><p><strong>未脱敏原始诊断报告，仅供本机管理者使用。</strong> 原始 IP、域名、端口、错误及关联标识保留。测量范围之外的状态为未知，不代表成功。</p>{{if .Info.Partial}}<p>报告不完整：{{range .Info.Missing}}{{.}}; {{end}}</p>{{end}}<h2>诊断摘要和原始指标</h2><pre>{{.Summary}}</pre><small>ZIP 包包含原始日志、qlog、JSON 快照与 SHA-256 清单。未主动收集业务负载、密码文件或私钥。既有日志内容不替换、不屏蔽。</small></html>`
	t, err := template.New("report").Parse(source)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = t.Execute(&out, struct {
		Info    ReportInfo
		Summary string
	}{info, summary})
	if err != nil {
		return nil, fmt.Errorf("render diagnostic report: %w", err)
	}
	return out.Bytes(), nil
}
