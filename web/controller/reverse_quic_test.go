package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestReverseQUICBridgeAPIIsWithinAuthenticatedXUIRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/private/xui", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	NewReverseQUICController(group)
	for _, endpoint := range []string{"config", "save", "status"} {
		request := httptest.NewRequest("POST", "/private/xui/reverse-quic/"+endpoint, strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s bypassed XUI login: %d", endpoint, response.Code)
		}
	}
}
