package web

import (
	"html/template"
	"testing"
)

func TestSubscriptionTemplateParses(t *testing.T) {
	data, err := htmlFS.ReadFile("html/xui/subscription.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.New("subscription.html").Parse(string(data)); err != nil {
		t.Fatalf("subscription template parse error: %v", err)
	}
}
