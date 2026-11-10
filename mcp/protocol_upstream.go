package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// upClient 是 apiduo 自身的 /v1 客户端。
//
// MCP 合并进 apiduo 之后仍然走 HTTP 自调，而不是直接调 relay：
// 绕过 HTTP 会把改动面从「一个目录」放大到「重写 relay 调用链」，
// 而 relay 里渠道选择、计费、插件分发都是既有能力，不该被复制一份。
//
// 代价是长任务会占两条连接（MCP 一条、自调一条），
// MaxIdleConnsPerHost 已经按这个场景放大。
type upClient struct {
	baseURL string
	hc      *http.Client
}

// upError 是上游错误，保留状态码、业务码与实际请求位置。
//
// 记录方法+路径不是为了好看：不同协议族的端点不同，拿错端点时上游常返回
// 空 body 的 404，没有路径就完全看不出是「打错地方」还是「参数不对」。
type upError struct {
	Status int
	Code   string
	Msg    string
	Method string
	Path   string
}

func (e *upError) Error() string {
	where := ""
	if e.Method != "" || e.Path != "" {
		where = " (" + strings.TrimSpace(e.Method+" "+e.Path) + ")"
	}
	if e.Code != "" {
		return fmt.Sprintf("upstream %d %s%s: %s", e.Status, e.Code, where, e.Msg)
	}
	return fmt.Sprintf("upstream %d%s: %s", e.Status, where, e.Msg)
}

// defaultUpstreamPort 与 apiduo 的默认监听端口一致。
const defaultUpstreamPort = "3000"

func newUpClient() *upClient {
	base := strings.TrimSpace(os.Getenv("MCP_UPSTREAM_BASE_URL"))
	if base == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			port = defaultUpstreamPort
		}
		base = "http://127.0.0.1:" + port + "/v1"
	}
	return &upClient{
		baseURL: strings.TrimRight(base, "/"),
		hc: &http.Client{
			// 图片/视频生成是慢操作；更长的场景走异步任务，不要调大这个值。
			Timeout: 5 * time.Minute,
			Transport: &http.Transport{
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// do 发起请求。apiKey 为空时拒绝出网 —— 没有凭证就不该发请求。
func (c *upClient) do(ctx context.Context, apiKey, method, path string, body any) ([]byte, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, &upError{Status: http.StatusUnauthorized, Msg: "缺少 api_key"}
	}
	var rdr io.Reader
	contentType := ""
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("编码请求失败: %w", err)
		}
		rdr = bytes.NewReader(raw)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求上游失败: %w", redactErr(err, apiKey))
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取上游响应失败: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return data, nil
	}

	up := parseUpError(resp.StatusCode, data)
	// 错误体是外部输入：4xx 常把请求头里的 key 原样回显
	// （"Incorrect API key provided: sk-xxx"）。它要进日志、进 MCP 响应，
	// 所以必须和网络错误一样过一遍脱敏 —— 只覆盖一半等于没覆盖。
	up.Msg = redact(up.Msg, apiKey)
	up.Code = redact(up.Code, apiKey)
	up.Method, up.Path = method, path
	return nil, up
}

func (c *upClient) listModels(ctx context.Context, apiKey string) ([]map[string]any, error) {
	data, err := c.do(ctx, apiKey, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Data []map[string]any `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // 保留整数精度
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("解析 /v1/models 失败: %w", err)
	}
	return doc.Data, nil
}

func (c *upClient) generate(ctx context.Context, apiKey string, t transport, payload map[string]any) (map[string]any, error) {
	data, err := c.do(ctx, apiKey, http.MethodPost, t.Generate, payload)
	if err != nil {
		return nil, err
	}
	return decodeObject(data)
}

func (c *upClient) getTask(ctx context.Context, apiKey string, t transport, taskKey string) (map[string]any, error) {
	if t.Query == "" {
		return nil, fmt.Errorf("协议 %s 不支持任务查询", t.Name)
	}
	if strings.TrimSpace(taskKey) == "" {
		return nil, errors.New("缺少上游任务键")
	}
	data, err := c.do(ctx, apiKey, http.MethodGet, t.queryPath(taskKey), nil)
	if err != nil {
		return nil, err
	}
	return decodeObject(data)
}

func decodeObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("解析上游响应失败: %w", err)
	}
	return out, nil
}

// parseUpError 兼容 OpenAI 风格 {error:{message,code}} 与扁平结构。
func parseUpError(status int, data []byte) *upError {
	e := &upError{Status: status}
	var probe struct {
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
		Code    any    `json:"code"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(data, &probe); err == nil {
		switch {
		case probe.Error != nil && probe.Error.Message != "":
			e.Msg = probe.Error.Message
			e.Code = fmt.Sprint(probe.Error.Code)
		case probe.Message != "":
			e.Msg = probe.Message
			e.Code = fmt.Sprint(probe.Code)
		case probe.Msg != "":
			e.Msg = probe.Msg
		}
	}
	if e.Msg == "" {
		s := strings.TrimSpace(string(data))
		if len(s) > 300 {
			s = s[:300]
		}
		e.Msg = s
	}
	if e.Code == "<nil>" {
		e.Code = ""
	}
	return e
}

// credentialPattern 匹配常见的密钥形态。
//
// 光比对调用方自己的 key 不够：上游 4xx 常把**渠道** key 回显在错误里
// （"Incorrect API key provided: sk-xxx"），而 MCP 只知道用户 key。
// 这条错误要进 agent 的上下文，等于把渠道凭证发给了终端用户。
//
// 只认 sk/ak/pk 三种前缀、且尾段至少 8 位：key-value 这类技术词在这条链路上
// 比短 key 常见得多，把正常文本吃掉比漏一个短 key 的代价更大。
var credentialPattern = regexp.MustCompile(`\b(?:sk|ak|pk)-[A-Za-z0-9_-]{8,}`)

// redact 抹掉文本里出现的密钥。
//
// 两段：先按已知 key 精确替换（能处理任意形态的自定义 key），
// 再按形态扫一遍（覆盖上游回显的、我们不认识的 key）。
// 空 key 也走第二段 —— 「不知道具体 key」不等于「没有秘密可能泄露」。
func redact(s, apiKey string) string {
	if apiKey != "" {
		s = strings.ReplaceAll(s, apiKey, "***")
	}
	return credentialPattern.ReplaceAllString(s, "sk-***")
}

func redactErr(err error, apiKey string) error {
	if err == nil || apiKey == "" {
		return err
	}
	return fmt.Errorf("%s", redact(err.Error(), apiKey))
}
