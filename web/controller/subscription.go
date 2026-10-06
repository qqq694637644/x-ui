package controller

import (
	"net/http"

	"x-ui/web/service"

	"github.com/gin-gonic/gin"
)

type SubscriptionController struct {
	subscriptionService service.SubscriptionService
}

func NewSubscriptionController(g *gin.RouterGroup) *SubscriptionController {
	a := &SubscriptionController{}
	g.GET("/sub/:token", a.subscription)
	return a
}

func (a *SubscriptionController) subscription(c *gin.Context) {
	body, err := a.subscriptionService.Generate(c.Param("token"))
	if err != nil {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.String(http.StatusNotFound, "Not Found")
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.String(http.StatusOK, body)
}
