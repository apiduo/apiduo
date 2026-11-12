package mcp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// taskID 是自包含签名的任务标识。
//
// 为什么不存表：MCP 是无状态多副本的，任何副本收到 task.get 都要能处理。
// 把 model 与上游任务键编进 ID，外面套一层 HMAC 防伪造，就不需要共享存储。
//
// 密钥由 api_key 派生：换 key 即作废旧任务 ID —— 这正是期望行为，
// key 换了，旧任务的调用资格本来就该一起失效。
type taskPayload struct {
	Model    string `json:"m"`
	Upstream string `json:"u"`
	Kind     string `json:"k"`
	IssuedAt int64  `json:"t"`
}

var errTaskIDMalformed = errors.New("task_id 不合法")

func deriveTaskKey(apiKey string) []byte {
	h := sha256.Sum256([]byte("apiduo/mcp/task-id/v1\x00" + apiKey))
	return h[:]
}

func signTaskID(apiKey string, p taskPayload) string {
	body, _ := json.Marshal(p)
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, deriveTaskKey(apiKey))
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

func parseTaskID(apiKey, token string) (taskPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return taskPayload{}, errTaskIDMalformed
	}
	mac := hmac.New(sha256.New, deriveTaskKey(apiKey))
	mac.Write([]byte(parts[0]))
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return taskPayload{}, errTaskIDMalformed
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return taskPayload{}, errTaskIDMalformed
	}
	var p taskPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return taskPayload{}, errTaskIDMalformed
	}
	if p.Model == "" || p.Upstream == "" {
		return taskPayload{}, errTaskIDMalformed
	}
	if p.Kind == "" {
		p.Kind = "image"
	}
	return p, nil
}
