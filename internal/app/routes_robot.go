package app

import (
	"cyberstrike-ai/internal/security"
	"time"

	"github.com/gin-gonic/gin"
)

// registerRobotRoutes registers the robot endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerRobotRoutes(protected *gin.RouterGroup) {
	robotHandler := deps.robotHandler
	wechatRobotHandler := deps.wechatRobotHandler

	// 机器人测试（需登录）：POST /api/robot/test，body: {"platform":"dingtalk","user_id":"test","text":"帮助"}，用于验证机器人逻辑
	protected.POST("/robot/test", robotHandler.HandleRobotTest)

	// 微信 iLink 扫码绑定（需登录）
	protected.POST("/robot/wechat/qrcode", wechatRobotHandler.HandleWechatQRCode)
	protected.GET("/robot/wechat/qrcode/status", wechatRobotHandler.HandleWechatQRCodeStatus)
	protected.POST("/robot/wechat/qrcode/verify", wechatRobotHandler.HandleWechatVerifyCode)
	protected.GET("/robot/wechat/status", wechatRobotHandler.HandleWechatStatus)
}

// registerRobotCallbackRoutes registers the machine-to-machine callbacks. They cannot be
// behind the session: the platforms POST to them with no login and no bearer token, so the
// defenses are the per-IP rate limit and each platform's own signature check inside the
// handler. Split out of setupRoutes for the same reason as the rest: the route table is
// asserted as a whole, and a registration buried in the wiring function is one nobody
// greps for.
func (deps routeDeps) registerRobotCallbackRoutes(api *gin.RouterGroup) {
	robotHandler := deps.robotHandler

	robotRL := security.NewRateLimiter(60, 1*time.Minute)
	robotGroup := api.Group("/robot")
	robotGroup.Use(security.RateLimitMiddleware(robotRL))
	{
		robotGroup.GET("/wecom", robotHandler.Wecom().HandleWecomGET)
		robotGroup.POST("/wecom", robotHandler.Wecom().HandleWecomPOST)
		robotGroup.POST("/dingtalk", robotHandler.HandleDingtalkPOST)
		robotGroup.POST("/lark", robotHandler.HandleLarkPOST)
	}
}
