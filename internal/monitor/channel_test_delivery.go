package monitor

import (
	"bidmon/internal/checks"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"net/http"
	"time"
)

// An explicit administrator test sends synthetic content and creates no event.
func (s *Server) testChannel(ctx context.Context, ch checks.ChannelSpec) error {
	token := ""
	if ch.TokenSecret.Env != "" || ch.TokenSecret.File != "" {
		var err error
		token, err = s.Cfg.Runtime.resolveSecret(ch.TokenSecret)
		if err != nil {
			return errors.New("渠道凭据不可用")
		}
	}
	id := uuid.NewString()
	payload := M{"event_id": id, "revision": 1, "task_id": "channel-test", "check_id": "channel-test", "rule_code": "channel-test", "episode": 0, "status": "test", "channel": ch.ID, "diagnosis_mode": "none", "business_recovery": "not_applicable", "kind": "test", "test_message": true, "observed_at": stamp(time.Now()), "summary": "bid_mon 通知渠道测试，无业务异常"}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", ch.URL, bytes.NewReader(raw))
	if err != nil {
		return errors.New("渠道地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test:"+id+":"+ch.ID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := HTTP.Do(req)
	if err != nil {
		return errors.New("测试消息未确认，请核对网络和渠道地址")
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("测试消息未获成功确认")
	}
	if ch.Confirm == "code_zero" {
		var body struct {
			Code *int `json:"code"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Code == nil || *body.Code != 0 {
			return errors.New("渠道未返回 code=0 确认")
		}
	}
	return nil
}
