package advancedcustomasync

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const ChannelName = "advanced_custom_async"

// Adaptor drives a vendor task API described entirely by the channel's
// advanced_async configuration. It supports the submit-then-poll shape:
// POST a request, keep the vendor task id, poll a query endpoint until a
// terminal status, then read the result URL out of the reply.
type Adaptor struct {
	info       *relaycommon.RelayInfo
	config     *kitdto.AdvancedAsyncConfig
	submitBody []byte
}

var _ channel.TaskAdaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.info = info
	if info == nil {
		a.config = nil
		return
	}
	a.config = info.ChannelOtherSettings.AdvancedAsync
}

func (a *Adaptor) configOrError() (*kitdto.AdvancedAsyncConfig, error) {
	if a.config == nil || !a.config.IsEnabled() {
		return nil, fmt.Errorf("advanced_async is not configured for this channel")
	}
	if err := a.config.Validate(); err != nil {
		return nil, err
	}
	return a.config, nil
}

// taskConfig reads the async configuration for a persisted task. Polling runs
// without relay info, and one adaptor instance may serve several tasks
// concurrently, so the channel row is read per call instead of cached on the
// adaptor.
func (a *Adaptor) taskConfig(task *model.Task) (*kitdto.AdvancedAsyncConfig, error) {
	if a.config != nil && a.config.IsEnabled() {
		return a.config, nil
	}
	if task == nil {
		return nil, fmt.Errorf("advanced_async is not configured for this channel")
	}
	channel, err := model.CacheGetChannel(task.ChannelId)
	if err != nil {
		return nil, fmt.Errorf("load channel for advanced_async task: %w", err)
	}
	config := channel.GetOtherSettings().AdvancedAsync
	if config == nil || !config.IsEnabled() {
		return nil, fmt.Errorf("advanced_async is not configured for this channel")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// ValidateRequestAndSetAction reads the client request once, keeps the bytes
// for the upstream submit, and restores the body so later handlers can read it.
func (a *Adaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	if _, err := a.configOrError(); err != nil {
		return taskError(err, "advanced_async_invalid", http.StatusBadRequest)
	}

	body, err := readAndRestoreRequestBody(c)
	if err != nil {
		return taskError(err, "invalid_request", http.StatusBadRequest)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return taskError(fmt.Errorf("request body is required"), "invalid_request", http.StatusBadRequest)
	}
	a.submitBody = body
	if info != nil && info.Action == "" {
		info.Action = "generate"
	}
	return nil
}

func (a *Adaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	return nil
}

func (a *Adaptor) AdjustBillingOnSubmit(info *relaycommon.RelayInfo, taskData []byte) map[string]float64 {
	return nil
}

func (a *Adaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	return 0
}

func (a *Adaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	config, err := a.configOrError()
	if err != nil {
		return "", err
	}
	upstreamPath := config.Submit.UpstreamPath
	if info != nil {
		upstreamPath = strings.ReplaceAll(upstreamPath, "{model}", info.UpstreamModelName)
	}
	target, err := resolveTargetURL(channelBaseURL(info), upstreamPath)
	if err != nil {
		return "", err
	}
	return applyQueryAuth(target, config.Submit.Auth, apiKey(info))
}

func (a *Adaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	config, err := a.configOrError()
	if err != nil {
		return err
	}
	if config.Submit.ContentType == kitdto.AdvancedAsyncContentTypeMultipart {
		if contentType := c.GetHeader("Content-Type"); contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	applyHeaderAuth(req.Header, config.Submit.Auth, apiKey(info))
	return nil
}

func (a *Adaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	if _, err := a.configOrError(); err != nil {
		return nil, err
	}
	if len(a.submitBody) == 0 {
		return nil, fmt.Errorf("advanced_async submit body is empty")
	}
	return bytes.NewReader(a.submitBody), nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	config, err := a.configOrError()
	if err != nil {
		return nil, err
	}
	method := config.Submit.Method
	if method == "" {
		method = http.MethodPost
	}
	originalMethod := c.Request.Method
	c.Request.Method = method
	defer func() { c.Request.Method = originalMethod }()
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

// ParseResponse keeps the vendor reply as task data and extracts the id the
// query endpoint needs. A vendor that answers the result inline terminates the
// task immediately instead of leaving it to the poller.
func (a *Adaptor) ParseResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*channel.TaskSubmitResponse, *taskdto.TaskError) {
	config, err := a.configOrError()
	if err != nil {
		return nil, taskError(err, "advanced_async_invalid", http.StatusBadRequest)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, taskError(err, "fail_to_fetch_task", http.StatusBadGateway)
	}
	var decoded any
	if err := common.Unmarshal(body, &decoded); err != nil {
		return nil, taskError(fmt.Errorf("upstream returned invalid JSON: %w", err), "invalid_upstream_response", http.StatusBadGateway)
	}
	taskID, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.TaskIDPath)
	if !ok || taskID == "" {
		return nil, taskError(fmt.Errorf("upstream response has no task id at %s", strings.Join(config.Fields.TaskIDPath, ".")), "invalid_upstream_response", http.StatusBadGateway)
	}
	return &channel.TaskSubmitResponse{
		UpstreamTaskID: taskID,
		TaskData:       body,
	}, nil
}

// FetchTask polls the vendor query endpoint with the configuration's auth.
func (a *Adaptor) FetchTask(baseURL, key string, task *model.Task, proxy string) (*http.Response, error) {
	config, err := a.taskConfig(task)
	if err != nil {
		return nil, err
	}
	taskID := ""
	if task != nil {
		taskID = task.GetUpstreamTaskID()
	}
	if taskID == "" {
		return nil, fmt.Errorf("task has no upstream task id")
	}
	target, err := buildQueryURL(baseURL, config.Query, taskID)
	if err != nil {
		return nil, err
	}
	method := config.Query.Method
	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	applyHeaderAuth(request.Header, config.Query.Auth, key)

	client, err := service.GetHttpClientWithProxySettings(proxy, kitdto.ChannelSettings{})
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

// ParseTaskResult maps the vendor reply onto the gateway task state machine.
func (a *Adaptor) ParseTaskResult(task *model.Task, resp *http.Response, respBody []byte) (*relaycommon.TaskInfo, error) {
	config, err := a.taskConfig(task)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := common.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("upstream returned invalid JSON: %w", err)
	}
	rawStatus, _ := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.StatusPath)
	status := config.Fields.NormalizeAdvancedAsyncStatus(rawStatus)

	result := &relaycommon.TaskInfo{Status: status}
	if resultURL, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.ResultURLPath); ok {
		result.Url = resultURL
	}
	if progress, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.ProgressPath); ok {
		result.Progress = progress
	}
	if reason, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.ErrorPath); ok {
		result.Reason = reason
	}
	if status == "FAILURE" && result.Reason == "" {
		result.Reason = "upstream reported failure"
	}
	return result, nil
}

func (a *Adaptor) GetModelList() []string { return nil }

func (a *Adaptor) GetChannelName() string { return ChannelName }

func readAndRestoreRequestBody(c *gin.Context) ([]byte, error) {
	seeker, err := common.GetRequestBody(c)
	if err != nil {
		return nil, err
	}
	reader, ok := seeker.(io.Reader)
	if !ok {
		return nil, fmt.Errorf("request body is not readable")
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return body, nil
}

func channelBaseURL(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	return strings.TrimSpace(info.ChannelBaseUrl)
}

func apiKey(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	return info.ApiKey
}

// resolveTargetURL joins a relative vendor path onto the channel base URL, or
// returns a full URL unchanged.
func resolveTargetURL(baseURL string, target string) (*url.URL, error) {
	trimmed := strings.TrimSpace(target)
	if strings.HasPrefix(trimmed, "/") {
		if strings.HasPrefix(trimmed, "//") {
			return nil, fmt.Errorf("advanced_async upstream path must be a full URL or a path starting with /")
		}
		if baseURL == "" {
			return nil, fmt.Errorf("channel base URL is required when advanced_async upstream path is relative")
		}
		base, err := url.Parse(baseURL)
		if err != nil || base.Scheme == "" || base.Host == "" {
			return nil, fmt.Errorf("channel base URL must be a full URL when advanced_async upstream path is relative")
		}
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return nil, err
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(parsed.Path, "/")
		base.RawPath = ""
		base.RawQuery = parsed.RawQuery
		return base, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("advanced_async upstream path must be a full URL or a path starting with /")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("advanced_async upstream path must use http or https")
	}
	return parsed, nil
}

func buildQueryURL(baseURL string, query kitdto.AdvancedAsyncQuery, taskID string) (string, error) {
	upstreamPath := strings.ReplaceAll(query.UpstreamPath, "{task_id}", url.PathEscape(taskID))
	target, err := resolveTargetURL(baseURL, upstreamPath)
	if err != nil {
		return "", err
	}
	if query.TaskIDLocation != kitdto.AdvancedAsyncTaskIDPath && !strings.Contains(query.UpstreamPath, "{task_id}") {
		values := target.Query()
		values.Set(query.TaskIDParam, taskID)
		target.RawQuery = values.Encode()
	}
	return applyQueryAuth(target, query.Auth, "")
}

func applyQueryAuth(target *url.URL, auth *kitdto.AdvancedCustomRouteAuth, key string) (string, error) {
	if auth == nil || strings.TrimSpace(auth.Type) != kitdto.AdvancedCustomAuthTypeQuery {
		return target.String(), nil
	}
	values := target.Query()
	values.Set(strings.TrimSpace(auth.Name), applyAuthTemplate(auth.Value, key))
	target.RawQuery = values.Encode()
	return target.String(), nil
}

func applyHeaderAuth(header http.Header, auth *kitdto.AdvancedCustomRouteAuth, key string) {
	if auth == nil {
		header.Set("Authorization", "Bearer "+key)
		return
	}
	switch strings.TrimSpace(auth.Type) {
	case kitdto.AdvancedCustomAuthTypeNone, kitdto.AdvancedCustomAuthTypeQuery:
	case kitdto.AdvancedCustomAuthTypeHeader:
		header.Set(strings.TrimSpace(auth.Name), applyAuthTemplate(auth.Value, key))
	default:
		header.Set("Authorization", "Bearer "+key)
	}
}

func applyAuthTemplate(template string, key string) string {
	return strings.ReplaceAll(template, "{api_key}", key)
}

func taskError(err error, code string, status int) *taskdto.TaskError {
	return &taskdto.TaskError{
		Code:       code,
		Message:    err.Error(),
		StatusCode: status,
		Error:      err,
		LocalError: true,
	}
}
