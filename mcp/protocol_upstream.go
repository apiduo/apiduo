package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
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

// 读类调用的超时来自 mcp_settings（默认 15s / 30s），不再编进常量：
// 上游变慢时要能立刻放宽，而不是发一个版本。

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
			// 线上真实数据：qwen n=4 的一次同步生成用了 5 分钟以上，300s 超时
			// 恰好在响应到达前触发 —— 上游照常完成并计费 20000，产物 URL 全丢。
			// 8 分钟覆盖已观测到的最慢同步批量；再长的场景本应走异步任务。
			Timeout: 8 * time.Minute,
			Transport: &http.Transport{
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// do 发起请求。apiKey 为空时拒绝出网 —— 没有凭证就不该发请求。
//
// 第二个返回值是 relay 的 X-Oneapi-Request-Id：MCP 层写的观察日志必须能
// 指回它触发的那条 relay 消费日志，否则「MCP 说上游 400」与「relay 记了
// 一次消费」在日志页上是两条互不相干的记录，只能靠同秒猜。
// 失败路径也返回它 —— 恰恰是失败时最需要对上账。
func (c *upClient) do(ctx context.Context, apiKey, method, path string, body any) ([]byte, string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, "", &upError{Status: http.StatusUnauthorized, Msg: "缺少 api_key"}
	}
	var rdr io.Reader
	contentType := ""
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, "", &errNotSent{Err: fmt.Errorf("编码请求失败: %w", err)}
		}
		rdr = bytes.NewReader(raw)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, "", &errNotSent{Err: fmt.Errorf("构造请求失败: %w", err)}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", c.wrapNetErr(err, apiKey)
	}
	defer resp.Body.Close()
	relayRequestId := resp.Header.Get(common.RequestIdKey)

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, relayRequestId, fmt.Errorf("读取上游响应失败: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return data, relayRequestId, nil
	}

	up := parseUpError(resp.StatusCode, data)
	// 错误体是外部输入：4xx 常把请求头里的 key 原样回显
	// （"Incorrect API key provided: sk-xxx"）。它要进日志、进 MCP 响应，
	// 所以必须和网络错误一样过一遍脱敏 —— 只覆盖一半等于没覆盖。
	up.Msg = redact(up.Msg, apiKey)
	up.Code = redact(up.Code, apiKey)
	up.Method, up.Path = method, path
	return nil, relayRequestId, up
}

// errNotSent 表示请求根本没离开本进程（编码失败、构造失败、拨号失败）。
//
// 与「发出去了但没等到响应」必须分开：前者可以安全重试，后者重试可能是
// 第二次扣费。幂等键要不要释放，全靠这个区分。
type errNotSent struct{ Err error }

func (e *errNotSent) Error() string { return e.Err.Error() }
func (e *errNotSent) Unwrap() error { return e.Err }

// errUpstreamTimeout 标记「等上游超时」，与普通网络错误分开归类。
//
// BeforeSend 区分两种超时，处置完全相反：
//   - 拨号超时：请求根本没到上游，什么都没发生，可以放心重试；
//   - 等响应超时：请求已经发出，上游很可能已经受理并计费，重试就是第二次扣费。
type errUpstreamTimeout struct {
	Msg        string
	BeforeSend bool
}

func (e *errUpstreamTimeout) Error() string { return e.Msg }

// wrapNetErr 把网络层错误变成可以直接给 agent 看的一段话。
//
// 两件事必须做：
//  1. 抹掉 api_key（上游可能在错误里回显）；
//  2. 抹掉内网地址。原始报错是 `Post "http://127.0.0.1:3000/v1/images/generations"`，
//     这个地址对调用方毫无意义，却把部署结构暴露给了外部。
func (c *upClient) wrapNetErr(err error, apiKey string) error {
	msg := redact(err.Error(), apiKey)
	if host := c.schemeHost(); host != "" {
		msg = strings.ReplaceAll(msg, host, "")
	}
	if isTimeoutErr(err) {
		return &errUpstreamTimeout{Msg: "请求上游超时: " + msg, BeforeSend: isDialErr(err)}
	}
	return fmt.Errorf("请求上游失败: %w", errors.New(msg))
}

// schemeHost 返回 baseURL 的 scheme://host，用于从错误文本里抹掉。
func (c *upClient) schemeHost() string {
	i := strings.Index(c.baseURL, "://")
	if i < 0 {
		return ""
	}
	rest := c.baseURL[i+3:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return c.baseURL[:i+3] + rest
}

// isDialErr 判断错误发生在拨号阶段（连接都没建立，请求肯定没发出去）。
func isDialErr(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) {
		return op.Op == "dial"
	}
	return false
}

func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (c *upClient) listModels(ctx context.Context, apiKey string) ([]map[string]any, error) {
	// 与生图分开：/models 是一次轻量读，卡住 8 分钟没有意义。
	ctx, cancel := context.WithTimeout(ctx, secondsToDuration(LoadSettings().ModelsTimeoutS))
	defer cancel()
	data, _, err := c.do(ctx, apiKey, http.MethodGet, "/models", nil)
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

// generate 同时返回 relay 的 request id，供观察日志回填关联键。
func (c *upClient) generate(ctx context.Context, apiKey string, t transport, payload map[string]any) (map[string]any, string, error) {
	data, relayRequestId, err := c.do(ctx, apiKey, http.MethodPost, t.Generate, payload)
	if err != nil {
		return nil, relayRequestId, err
	}
	out, err := decodeObject(data)
	return out, relayRequestId, err
}

func (c *upClient) getTask(ctx context.Context, apiKey string, t transport, taskKey string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, secondsToDuration(LoadSettings().TaskTimeoutS))
	defer cancel()
	if t.Query == "" {
		return nil, fmt.Errorf("协议 %s 不支持任务查询", t.Name)
	}
	if strings.TrimSpace(taskKey) == "" {
		return nil, errors.New("缺少上游任务键")
	}
	data, _, err := c.do(ctx, apiKey, http.MethodGet, t.queryPath(taskKey), nil)
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
