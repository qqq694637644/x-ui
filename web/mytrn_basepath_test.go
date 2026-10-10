package web

import (
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"x-ui/web/controller"
)

// Test the panel's non-root URL contract without starting Xray or contacting
// B. The router is mounted below webBasePath, and the MyTRN settings form
// must use the page's existing basePath rather than the site's root.
func TestMyTRNSaveRouteRespectsPanelBasePath(t *testing.T) {
	engine := gin.New()
	controller.NewMyTRNController(engine.Group("/panel/").Group("/xui"))
	found := false
	for _, route := range engine.Routes() {
		if route.Method == "POST" && route.Path == "/panel/xui/mytrn/save" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MyTRN POST route was not registered below /panel/")
	}
	page, err := os.ReadFile("html/xui/inbounds.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(page)
	if !strings.Contains(source, "fetch(basePath + 'xui/mytrn/save'") {
		t.Fatal("MyTRN save fetch does not use the existing panel basePath")
	}
	if strings.Contains(source, "fetch('/xui/mytrn/save'") {
		t.Fatal("MyTRN save still uses a root-absolute URL")
	}
}
