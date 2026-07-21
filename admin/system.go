package admin

import (
	"net/http"
	"os"
	"time"

	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

// ShutdownSystem 收到后台停止指令后，直接终止进程。
func (h *Handler) ShutdownSystem(c *gin.Context) {
	security.SecurityAuditLog("ADMIN_SHUTDOWN_REQUESTED", "ip="+c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"message":  "服务已直接终止",
		"shutting": true,
	})

	// 异步延迟 100ms 允许 HTTP 200 响应成功写回给前端，然后直接退出进程
	go func() {
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}()
}
