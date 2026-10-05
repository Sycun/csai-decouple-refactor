package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// newWecomTestGateway builds the wire layer alone: the conversation half is a stub, because
// these tests are about signatures, encryption and the reply envelope - none of which may
// depend on sessions or the agent answering.
type stubWecomInbound struct{ reply string }

func (s *stubWecomInbound) acceptFreshWecomRequest(timestamp, nonce, signature string) bool {
	return true
}
func (s *stubWecomInbound) handleRobotCommand(platform, userID, text string) (string, bool) {
	return "", false
}
func (s *stubWecomInbound) HandleMessage(platform, userID, text string) string {
	if s.reply == "" {
		return "stub reply"
	}
	return s.reply
}

func newWecomTestHandler(token string, aesKey string) *WecomGateway {
	return NewWecomGateway(&config.Config{
		Robots: config.RobotsConfig{
			Wecom: config.RobotWecomConfig{
				Enabled:        true,
				Token:          token,
				EncodingAESKey: aesKey,
			},
		},
	}, zap.NewNop(), &stubWecomInbound{})
}

func TestHandleWecomPOST_rejectsWhenTokenEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newWecomTestHandler("", "")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `<?xml version="1.0"?><xml><FromUserName>attacker</FromUserName><MsgType>text</MsgType><Content>hi</Content></xml>`
	c.Request = httptest.NewRequest(http.MethodPost, "/api/robot/wecom", strings.NewReader(body))

	h.HandleWecomPOST(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if w.Body.String() == "success" {
		t.Fatal("expected rejection, got success")
	}
}

func TestHandleWecomPOST_rejectsPlaintextWhenEncryptionConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newWecomTestHandler("secret-token", "abcdefghijklmnopqrstuvwxyz0123456789ABCD")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `<?xml version="1.0"?><xml><FromUserName>attacker</FromUserName><MsgType>text</MsgType><Content>hi</Content></xml>`
	c.Request = httptest.NewRequest(http.MethodPost, "/api/robot/wecom?timestamp=1&nonce=2&msg_signature=fake", strings.NewReader(body))

	h.HandleWecomPOST(c)

	if w.Body.String() == "success" {
		t.Fatal("expected rejection for plaintext in encryption mode, got success")
	}
}

func TestHandleWecomGET_rejectsWhenTokenEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newWecomTestHandler("", "")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/robot/wecom?msg_signature=x&timestamp=1&nonce=2&echostr=abc", nil)

	h.HandleWecomGET(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
