package web

import (
	"bytes"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticsTemplateCompilesAndScriptIsValid(t *testing.T) {
	raw, err := htmlFS.ReadFile("html/xui/component/rsx_diagnostics.html")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("diagnostics").Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := tmpl.ExecuteTemplate(&output, "component/rsxDiagnostics", nil); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "Vue.component('rsx-diagnostics'") || !strings.Contains(text, "原始报告") {
		t.Fatal("diagnostic component missing from rendered template")
	}
	start, end := strings.Index(text, "<script>"), strings.LastIndex(text, "</script>")
	if start < 0 || end <= start {
		t.Fatal("rendered JS missing")
	}
	script := text[start+len("<script>") : end]
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable for syntax validation")
	}
	path := filepath.Join(t.TempDir(), "diagnostics.js")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("rendered JS syntax failed: %v\n%s", err, output)
	}
}
