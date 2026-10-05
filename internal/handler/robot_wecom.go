package handler

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// WecomGateway is the 企业微信 wire layer: signature checks, the AES envelope, the passive
// reply body and the active-send API call. It is split off RobotHandler because the two change
// for unrelated reasons - this file changes when WeCom changes their callback format, that one
// when the conversation commands or the agent behaviour do - and because the crypto helpers
// here have no interest in sessions, roles or approvals.
//
// What it does not own is the replay guard: acceptFreshWecomRequest also serves the Lark
// callback, so the de-duplication map stays with the handler that both platforms enter
// through, and this type asks for it.
type WecomGateway struct {
	cfg     *config.Config
	logger  *zap.Logger
	inbound wecomInbound
}

// wecomInbound is the conversation half the gateway hands a decoded message to. Three
// methods, each one a question the transport cannot answer itself.
type wecomInbound interface {
	acceptFreshWecomRequest(timestamp, nonce, signature string) bool
	handleRobotCommand(platform, userID, text string) (string, bool)
	HandleMessage(platform, userID, text string) (reply string)
}

func NewWecomGateway(cfg *config.Config, logger *zap.Logger, inbound wecomInbound) *WecomGateway {
	return &WecomGateway{cfg: cfg, logger: logger, inbound: inbound}
}

// wecomRequireToken 企业微信回调必须配置 Token；未配置时拒绝请求，防止未授权触发 Agent。
func (g *WecomGateway) wecomRequireToken(c *gin.Context) (string, bool) {
	token := strings.TrimSpace(g.cfg.Robots.Wecom.Token)
	if token == "" {
		g.logger.Warn("企业微信已启用但未配置 token，已拒绝回调（请在配置中设置 robots.wecom.token）")
		c.String(http.StatusForbidden, "")
		return "", false
	}
	return token, true
}

// HandleWecomGET 企业微信 URL 校验（GET）
func (g *WecomGateway) HandleWecomGET(c *gin.Context) {
	if !g.cfg.Robots.Wecom.Enabled {
		c.String(http.StatusNotFound, "")
		return
	}
	token, ok := g.wecomRequireToken(c)
	if !ok {
		return
	}
	// Gin 的 Query() 会自动 URL 解码，拿到的就是正确的 base64 字符串
	echostr := c.Query("echostr")
	msgSignature := c.Query("msg_signature")
	timestamp := c.Query("timestamp")
	nonce := c.Query("nonce")

	// 验证签名：将 token、timestamp、nonce、echostr 四个参数排序后拼接计算 SHA1
	signature := g.signWecomRequest(token, timestamp, nonce, echostr)
	if signature != msgSignature {
		g.logger.Warn("企业微信 URL 验证签名失败", zap.String("expected", msgSignature), zap.String("got", signature))
		c.String(http.StatusBadRequest, "invalid signature")
		return
	}

	if echostr == "" {
		c.String(http.StatusBadRequest, "missing echostr")
		return
	}

	// 如果配置了 EncodingAESKey，说明是加密模式，需要解密 echostr
	if g.cfg.Robots.Wecom.EncodingAESKey != "" {
		decrypted, err := wecomDecrypt(g.cfg.Robots.Wecom.EncodingAESKey, echostr)
		if err != nil {
			g.logger.Warn("企业微信 echostr 解密失败", zap.Error(err))
			c.String(http.StatusBadRequest, "decrypt failed")
			return
		}
		c.String(http.StatusOK, string(decrypted))
		return
	}

	// 明文模式直接返回 echostr
	c.String(http.StatusOK, echostr)
}

// signWecomRequest 生成企业微信请求签名
// 企业微信签名算法：将 token、timestamp、nonce、echostr 四个值排序后拼接成字符串，再计算 SHA1
func (g *WecomGateway) signWecomRequest(token, timestamp, nonce, echostr string) string {
	strs := []string{token, timestamp, nonce, echostr}
	sort.Strings(strs)
	s := strings.Join(strs, "")
	hash := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", hash)
}

// HandleWecomPOST 企业微信消息回调（POST），支持明文与加密模式
func (g *WecomGateway) HandleWecomPOST(c *gin.Context) {
	if !g.cfg.Robots.Wecom.Enabled {
		g.logger.Debug("企业微信机器人未启用，跳过请求")
		c.String(http.StatusOK, "")
		return
	}
	// 从 URL 获取签名参数（加密模式回复时需要用到）
	timestamp := c.Query("timestamp")
	nonce := c.Query("nonce")
	msgSignature := c.Query("msg_signature")

	// 先读取请求体，后续解析/签名验证都会用到
	bodyRaw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		g.logger.Warn("企业微信 POST 读取请求体失败", zap.Error(err))
		c.String(http.StatusOK, "")
		return
	}
	g.logger.Debug("企业微信 POST 收到请求", zap.String("body", string(bodyRaw)))

	// 验证请求签名防止伪造。企业微信签名算法同 URL 验证，使用 token、timestamp、nonce、 Encrypt 四个字段。
	// 启用企业微信时必须配置 token 并校验签名，避免未授权请求触发 Agent。
	token, ok := g.wecomRequireToken(c)
	if !ok {
		return
	}
	if msgSignature == "" {
		g.logger.Warn("企业微信 POST 缺少签名，已拒绝（需确保回调携带 msg_signature）")
		c.String(http.StatusOK, "")
		return
	}
	var tmp wecomXML
	if err := xml.Unmarshal(bodyRaw, &tmp); err != nil {
		g.logger.Warn("企业微信 POST 签名验证前解析 XML 失败", zap.Error(err))
		c.String(http.StatusOK, "")
		return
	}
	expected := g.signWecomRequest(token, timestamp, nonce, tmp.Encrypt)
	if expected != msgSignature {
		g.logger.Warn("企业微信 POST 签名验证失败", zap.String("expected", expected), zap.String("got", msgSignature))
		c.String(http.StatusOK, "")
		return
	}
	if !g.inbound.acceptFreshWecomRequest(timestamp, nonce, msgSignature) {
		g.logger.Warn("企业微信 POST 时间戳过期或请求重放，已拒绝")
		c.String(http.StatusOK, "")
		return
	}

	var body wecomXML
	if err := xml.Unmarshal(bodyRaw, &body); err != nil {
		g.logger.Warn("企业微信 POST 解析 XML 失败", zap.Error(err))
		c.String(http.StatusOK, "")
		return
	}
	g.logger.Debug("企业微信 XML 解析成功", zap.String("ToUserName", body.ToUserName), zap.String("FromUserName", body.FromUserName), zap.String("MsgType", body.MsgType), zap.String("Content", body.Content), zap.String("Encrypt", body.Encrypt))

	// 保存企业 ID（用于明文模式回复）
	enterpriseID := body.ToUserName

	// 配置了 EncodingAESKey 时必须走加密消息，拒绝明文 XML 绕过
	if strings.TrimSpace(g.cfg.Robots.Wecom.EncodingAESKey) != "" && strings.TrimSpace(body.Encrypt) == "" {
		g.logger.Warn("企业微信已配置加密模式但收到明文消息，已拒绝")
		c.String(http.StatusOK, "")
		return
	}

	// 加密模式：先解密再解析内层 XML
	if body.Encrypt != "" && g.cfg.Robots.Wecom.EncodingAESKey != "" {
		g.logger.Debug("企业微信进入加密模式解密流程")
		decrypted, err := wecomDecrypt(g.cfg.Robots.Wecom.EncodingAESKey, body.Encrypt)
		if err != nil {
			g.logger.Warn("企业微信消息解密失败", zap.Error(err))
			c.String(http.StatusOK, "")
			return
		}
		g.logger.Debug("企业微信解密成功", zap.String("decrypted", string(decrypted)))
		if err := xml.Unmarshal(decrypted, &body); err != nil {
			g.logger.Warn("企业微信解密后 XML 解析失败", zap.Error(err))
			c.String(http.StatusOK, "")
			return
		}
		g.logger.Debug("企业微信内层 XML 解析成功", zap.String("FromUserName", body.FromUserName), zap.String("Content", body.Content))
	}

	tenantKey := strings.TrimSpace(enterpriseID)
	if tenantKey == "" {
		tenantKey = strings.TrimSpace(g.cfg.Robots.Wecom.CorpID)
	}
	if tenantKey == "" {
		tenantKey = "default"
	}
	rawUserID := strings.TrimSpace(body.FromUserName)
	replyUserID := rawUserID
	userID := ""
	if rawUserID != "" {
		userID = "t:" + tenantKey + "|u:" + rawUserID
	}
	text := strings.TrimSpace(body.Content)
	if userID == "" {
		g.logger.Warn("企业微信消息缺少可用用户标识，已忽略")
		c.String(http.StatusOK, "success")
		return
	}

	// 限制回复内容长度（企业微信限制 2048 字节）
	maxReplyLen := 2000
	limitReply := func(s string) string {
		if len(s) > maxReplyLen {
			return s[:maxReplyLen] + "\n\n（内容过长，已截断）"
		}
		return s
	}

	if body.MsgType != "text" {
		g.logger.Debug("企业微信收到非文本消息", zap.String("MsgType", body.MsgType))
		g.sendWecomReply(c, replyUserID, enterpriseID, limitReply("暂仅支持文本消息，请发送文字。"), timestamp, nonce)
		return
	}

	// 文本消息：先判断是否为内置命令（如 帮助/列表/新对话 等），这类命令处理很快，可以直接走被动回复，避免依赖主动发送 API。
	if cmdReply, ok := g.inbound.handleRobotCommand("wecom", userID, text); ok {
		g.logger.Debug("企业微信收到命令消息，走被动回复", zap.String("userID", userID), zap.String("text", text))
		g.sendWecomReply(c, replyUserID, enterpriseID, limitReply(cmdReply), timestamp, nonce)
		return
	}

	g.logger.Debug("企业微信开始处理消息（异步 AI）", zap.String("userID", userID), zap.String("text", text))

	// 企业微信被动回复有 5 秒超时限制，而 AI 调用通常超过该时长。
	// 这里采用推荐做法：立即返回 success（或空串），然后通过主动发送接口推送完整回复。
	c.String(http.StatusOK, "success")

	// 异步处理消息并通过企业微信主动消息接口发送结果
	go func() {
		reply := g.inbound.HandleMessage("wecom", userID, text)
		reply = limitReply(reply)
		g.logger.Debug("企业微信消息处理完成", zap.String("userID", userID), zap.String("reply", reply))
		// 调用企业微信 API 主动发送消息
		g.sendWecomMessageViaAPI(rawUserID, enterpriseID, reply)
	}()
}

// sendWecomReply 发送企业微信回复（加密模式自动加密）
// 参数：toUser=用户 ID, fromUser=企业 ID（明文模式）/CorpID（加密模式）, content=回复内容，timestamp/nonce=请求参数
func (g *WecomGateway) sendWecomReply(c *gin.Context, toUser, fromUser, content, timestamp, nonce string) {
	// 加密模式：判断 EncodingAESKey 是否配置
	if g.cfg.Robots.Wecom.EncodingAESKey != "" {
		// 加密模式使用 CorpID 进行加密
		corpID := g.cfg.Robots.Wecom.CorpID
		if corpID == "" {
			g.logger.Warn("企业微信加密模式缺少 CorpID 配置")
			c.String(http.StatusOK, "")
			return
		}

		// 构造完整的明文 XML 回复（格式严格按企业微信文档要求）
		plainResp := fmt.Sprintf(`<xml>
<ToUserName><![CDATA[%s]]></ToUserName>
<FromUserName><![CDATA[%s]]></FromUserName>
<CreateTime>%d</CreateTime>
<MsgType><![CDATA[text]]></MsgType>
<Content><![CDATA[%s]]></Content>
</xml>`, toUser, fromUser, time.Now().Unix(), content)

		encrypted, err := wecomEncrypt(g.cfg.Robots.Wecom.EncodingAESKey, plainResp, corpID)
		if err != nil {
			g.logger.Warn("企业微信回复加密失败", zap.Error(err))
			c.String(http.StatusOK, "")
			return
		}
		// 使用请求中的 timestamp/nonce 生成签名（企业微信要求回复时使用与请求相同的 timestamp 和 nonce）
		msgSignature := g.signWecomRequest(g.cfg.Robots.Wecom.Token, timestamp, nonce, encrypted)

		g.logger.Debug("企业微信发送加密回复",
			zap.String("Encrypt", encrypted[:50]+"..."),
			zap.String("MsgSignature", msgSignature),
			zap.String("TimeStamp", timestamp),
			zap.String("Nonce", nonce))

		// 加密模式仅返回 4 个核心字段（企业微信官方要求）
		xmlResp := fmt.Sprintf(`<xml><Encrypt><![CDATA[%s]]></Encrypt><MsgSignature><![CDATA[%s]]></MsgSignature><TimeStamp><![CDATA[%s]]></TimeStamp><Nonce><![CDATA[%s]]></Nonce></xml>`, encrypted, msgSignature, timestamp, nonce)
		// also log the final response body so we can cross-check with the
		// network traffic or developer console
		g.logger.Debug("企业微信加密回复包", zap.String("xml", xmlResp))
		// for additional confidence, decrypt the payload ourselves and log it
		if dec, err2 := wecomDecrypt(g.cfg.Robots.Wecom.EncodingAESKey, encrypted); err2 == nil {
			g.logger.Debug("企业微信加密回复解密检查", zap.String("plain", string(dec)))
		} else {
			g.logger.Warn("企业微信加密回复解密检查失败", zap.Error(err2))
		}

		// 使用 c.Writer.Write 直接写入响应，避免 c.String 的转义问题
		c.Writer.WriteHeader(http.StatusOK)
		// use text/xml as that's what WeCom examples show
		c.Writer.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = c.Writer.Write([]byte(xmlResp))
		g.logger.Debug("企业微信加密回复已发送")
		return
	}

	// 明文模式
	g.logger.Debug("企业微信发送明文回复", zap.String("ToUserName", toUser), zap.String("FromUserName", fromUser), zap.String("Content", content[:50]+"..."))

	// 手动构造 XML 响应（使用 CDATA 包裹所有字段，并包含 AgentID）
	xmlResp := fmt.Sprintf(`<xml>
<ToUserName><![CDATA[%s]]></ToUserName>
<FromUserName><![CDATA[%s]]></FromUserName>
<CreateTime>%d</CreateTime>
<MsgType><![CDATA[text]]></MsgType>
<Content><![CDATA[%s]]></Content>
</xml>`, toUser, fromUser, time.Now().Unix(), content)

	// log the exact plaintext response for debugging
	g.logger.Debug("企业微信明文回复包", zap.String("xml", xmlResp))

	// use text/xml as recommended by WeCom docs
	c.Header("Content-Type", "text/xml; charset=utf-8")
	c.String(http.StatusOK, xmlResp)
	g.logger.Debug("企业微信明文回复已发送")
}

// sendWecomMessageViaAPI 通过企业微信 API 主动发送消息（用于异步处理后的结果发送）
func (g *WecomGateway) sendWecomMessageViaAPI(toUser, toParty, content string) {
	if !g.cfg.Robots.Wecom.Enabled {
		return
	}

	secret := g.cfg.Robots.Wecom.Secret
	corpID := g.cfg.Robots.Wecom.CorpID
	agentID := g.cfg.Robots.Wecom.AgentID

	if secret == "" || corpID == "" {
		g.logger.Warn("企业微信主动 API 缺少 secret 或 corpID 配置")
		return
	}

	// 第 1 步：获取 access_token
	tokenURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s", corpID, secret)
	resp, err := http.Get(tokenURL)
	if err != nil {
		g.logger.Warn("企业微信获取 token 失败", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		g.logger.Warn("企业微信 token 响应解析失败", zap.Error(err))
		return
	}
	if tokenResp.ErrCode != 0 {
		g.logger.Warn("企业微信 token 获取错误", zap.String("errmsg", tokenResp.ErrMsg), zap.Int("errcode", tokenResp.ErrCode))
		return
	}

	// 第 2 步：构造发送消息请求
	msgReq := map[string]interface{}{
		"touser":  toUser,
		"msgtype": "text",
		"agentid": agentID,
		"text": map[string]interface{}{
			"content": content,
		},
	}

	msgBody, err := json.Marshal(msgReq)
	if err != nil {
		g.logger.Warn("企业微信消息序列化失败", zap.Error(err))
		return
	}

	// 第 3 步：发送消息
	sendURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=%s", tokenResp.AccessToken)
	msgResp, err := http.Post(sendURL, "application/json", bytes.NewReader(msgBody))
	if err != nil {
		g.logger.Warn("企业微信主动发送消息失败", zap.Error(err))
		return
	}
	defer msgResp.Body.Close()

	var sendResp struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		InvalidUser string `json:"invaliduser"`
		MsgID       string `json:"msgid"`
	}
	if err := json.NewDecoder(msgResp.Body).Decode(&sendResp); err != nil {
		g.logger.Warn("企业微信发送响应解析失败", zap.Error(err))
		return
	}

	if sendResp.ErrCode == 0 {
		g.logger.Debug("企业微信主动发送消息成功", zap.String("msgid", sendResp.MsgID))
	} else {
		g.logger.Warn("企业微信主动发送消息失败", zap.String("errmsg", sendResp.ErrMsg), zap.Int("errcode", sendResp.ErrCode), zap.String("invaliduser", sendResp.InvalidUser))
	}
}
