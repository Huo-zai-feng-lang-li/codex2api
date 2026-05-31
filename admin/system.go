package admin

import (
	"net/http"

	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

// ShutdownSystem requests a graceful process shutdown from the authenticated admin API.
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
