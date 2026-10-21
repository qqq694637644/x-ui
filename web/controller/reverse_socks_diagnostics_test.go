package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDiagnosticsRoutesInheritAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/private/xui", func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })
	a := &ReverseSocksController{}
	a.registerDiagnostics(group.Group("/reverse-socks"))
	for _, name := range []string{"snapshot", "history", "settings", "task", "cancel", "trace", "reports", "delete-report"} {
		request := httptest.NewRequest("POST", "/private/xui/reverse-socks/diagnostics/"+name, strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("diagnostic %s bypassed group auth: %d", name, response.Code)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/private/xui/reverse-socks/diagnostics/download/0123456789abcdef0123456789abcdef/zip", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatal("raw report download bypassed login")
	}
}
