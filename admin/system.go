package admin

import (
	"net/http"

	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

// ShutdownSystem 将关机请求交给 main 持有的服务生命周期控制器。
func (h *Handler) ShutdownSystem(c *gin.Context) {
	started := h.RequestShutdown("admin_api:" + c.ClientIP())
	if !started {
		security.SecurityAuditLog("ADMIN_SHUTDOWN_DUPLICATE", "ip="+c.ClientIP())
		c.JSON(http.StatusAccepted, gin.H{
			"message":  "服务关停已在进行",
			"shutting": true,
		})
		return
	}

	security.SecurityAuditLog("ADMIN_SHUTDOWN_REQUESTED", "ip="+c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"message":  "服务正在关闭",
		"shutting": true,
	})
}
