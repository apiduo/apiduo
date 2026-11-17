package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/endpoint"
	"github.com/QuantumNous/new-api/pkg/modelalias"
	"github.com/QuantumNous/new-api/pkg/urlguard"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/bytedance/gopkg/util/gopool"
)

// APIDUO:local-image-async BEGIN
//
// The worker that turns a locally executed task into a result.
//
// A local task is one the gateway promised to run itself: a synchronous image
// upstream accepted no task id, so there is nothing to poll. The worker replays
// the stored request once, writes what came back, and settles.
//
// Never retried. A synchronous generation is not idempotent: replaying it makes
// a second image and charges again. A failure is final and refunded.

// apiduoLocalImageAsyncTimeout bounds one replay. Longer than the client would
// have waited, because the whole point of the async entry is not to hold the
// connection while the vendor works.
const apiduoLocalImageAsyncTimeout = 5 * time.Minute

// apiduoLocalImageAsyncClock is the single time source for stamping a claim and
// for deciding a claim is dead.
//
// Both sides must read the same clock. Claiming writes "this worker started at"
// and reaping asks "is that older than the timeout"; with one node on its own
// clock and another on the database's, a minute of skew is the difference
// between a live task and a corpse. A variable so a test can prove the two
// sides share it.
var apiduoLocalImageAsyncClock = model.DatabaseNow

// RunLocalImageAsyncPass dispatches pending local tasks and returns how many it
// started. It is safe to call from the periodic scheduler: claiming is a
// compare-and-swap, so two overlapping passes cannot run the same task.
//
// Dispatch, not execute. A synchronous generation takes tens of seconds, and
// running it here would hold the scheduler's own row open for that long —
// measured: one 20-second batch of eight left the scheduler with a single tick
// in two minutes, which also starves every other periodic task. Each task
// therefore runs on its own goroutine, bounded by a process-wide slot count, and
// the pass returns immediately.
func RunLocalImageAsyncPass(ctx context.Context, limit int) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		return 0
	}
	apiduoReapStaleLocalTasks(ctx)

	dispatched := 0
	// Round-robin across channels, and never let one channel hold every slot.
	//
	// Ordering alone is not enough: dispatch happens on submission, so the first
	// channel fills the pool before the second one's request even exists —
	// measured 39 seconds of starvation. A hard half-share fixes that but halves
	// single-vendor throughput (122 seconds for 24 tasks against 62). Reserving
	// one slot is the smallest change that does both: a newcomer starts
	// immediately, and a lone channel still gets all but one.
	for _, task := range apiduoLocalImageAsyncInterleaved(model.ClaimLocalImageAsyncTasks(limit * 8)) {
		if dispatched >= limit {
			break
		}
		if !apiduoLocalImageAsyncHoldChannel(task.ChannelId, limit) {
			continue
		}
		// Take the process slot before claiming: a claimed row nobody runs
		// would be invisible to every other pass until the reaper noticed it.
		select {
		case apiduoLocalImageAsyncSlots <- struct{}{}:
		default:
			apiduoLocalImageAsyncReleaseChannel(task.ChannelId)
			return dispatched
		}
		if !apiduoClaimLocalTask(task) {
			<-apiduoLocalImageAsyncSlots
			apiduoLocalImageAsyncReleaseChannel(task.ChannelId)
			continue
		}
		dispatched++
		// WithoutCancel keeps the request's values but drops its lifetime: the
		// scheduler cancels its context when its row finishes, and a task must
		// outlive that. The task carries its own timeout, and the reaper covers
		// a process that dies mid-flight.
		gopool.Go(func() {
			defer func() {
				<-apiduoLocalImageAsyncSlots
				apiduoLocalImageAsyncReleaseChannel(task.ChannelId)
				// The slot is free; a queued task may be waiting for it.
				KickLocalImageAsyncWorker()
			}()
			// A panic in one task must not take the process down with it: this
			// goroutine handles untrusted vendor payloads, and the blast radius
			// of a nil dereference would be every other request on the gateway.
			if recovered := apiduoRunLocalTaskGuarded(context.WithoutCancel(ctx), task, func() {
				apiduoRunLocalTask(context.WithoutCancel(ctx), task)
			}); recovered != nil {
				// Its row is still IN_PROGRESS and no worker holds it, so fail
				// it now rather than leaving it for the reaper six minutes on.
				apiduoFailLocalTask(ctx, task, "the worker failed unexpectedly while executing this task")
			}
		})
	}
	return dispatched
}

// apiduoReapStaleLocalTasks fails local tasks whose worker is gone.
//
// A local task has no vendor to re-ask, so a row left IN_PROGRESS by a crash has
// nothing that would ever move it: every poll would answer "queued" forever and
// the reserved quota would never be refunded.
func apiduoReapStaleLocalTasks(ctx context.Context) int {
	cutoff := apiduoLocalImageAsyncReapCutoff(apiduoLocalImageAsyncClock())
	reaped := 0
	for _, task := range model.ClaimStaleLocalImageAsyncTasks(cutoff, apiduoLocalImageAsyncBatch) {
		apiduoFailLocalTask(ctx, task, "the worker executing this task stopped before it finished")
		reaped++
	}
	return reaped
}

// apiduoClaimLocalTask marks the row IN_PROGRESS under CAS.
//
// The query that selected it did not reserve anything, so this is the only place
// two workers can race — and the loser must not execute.
func apiduoClaimLocalTask(task *model.Task) bool {
	if task == nil {
		return false
	}
	from := task.Status
	task.Status = model.TaskStatusInProgress
	task.StartTime = apiduoLocalImageAsyncClock()
	task.Progress = "30%"
	won, err := task.UpdateWithStatus(from)
	if err != nil {
		return false
	}
	return won
}

func apiduoRunLocalTask(ctx context.Context, task *model.Task) {
	channel, err := model.GetChannelById(task.ChannelId, true)
	if err != nil || channel == nil {
		apiduoFailLocalTask(ctx, task, "the channel that accepted this task no longer exists")
		return
	}
	body := task.PrivateData.Execution
	if body == nil || (len(body.Request) == 0 && len(body.OriginRequest) == 0) {
		apiduoFailLocalTask(ctx, task, "the stored request for this task is missing")
		return
	}
	// The address was resolved at submit from the channel that accepted the
	// task, so it is the channel's own answer and not one this worker could
	// reproduce. Absent means the row predates that field, and guessing a path
	// would post the request somewhere no channel approved.
	if strings.TrimSpace(body.UpstreamURL) == "" {
		apiduoFailLocalTask(ctx, task, "the stored request has no upstream address")
		return
	}
	url := body.UpstreamURL
	// Prepared per channel, not inherited from submission: a task that failed
	// over to another channel has to be re-mapped for it.
	requestBody, _, prepareErr := apiduoLocalImageBody(channel, task)
	if prepareErr != nil || len(requestBody) == 0 {
		apiduoFailLocalTask(ctx, task, "the stored request cannot be prepared for this channel")
		return
	}
	key, _ := model.GetTaskKey(channel, task)

	requestCtx, cancel := context.WithTimeout(ctx, apiduoLocalImageAsyncTimeout)
	defer cancel()
	setting := channel.GetSetting()
	client, err := GetHttpClientWithProxySettings(setting.Proxy, kitdto.ChannelSettings{})
	if err != nil {
		apiduoFailLocalTask(ctx, task, "failed to build the upstream client")
		return
	}

	var payload []byte
	var requestErr error
	for attempt := 1; ; attempt++ {
		replay, err := apiduoReplayLocalTask(requestCtx, client, url, key, requestBody)
		payload, requestErr = replay.Payload, err
		if err == nil || replay.Status != http.StatusTooManyRequests {
			break
		}
		// Rate limited. Another channel is the better answer — it can serve the
		// request now instead of later — so try that first.
		if apiduoRotateLocalTask(ctx, task, channel) {
			return
		}
		// No other channel serves this model. Wait for this one instead of
		// refunding: the slot is held, so the wait is also what stops the
		// process from asking again while the vendor is still busy. The backoff
		// is on the order of a generation, not of a network hiccup — a two
		// second retry would only be refused again.
		if attempt >= apiduoLocalImageAsyncRateLimitAttempts {
			break
		}
		delay := replay.RetryAfter
		if delay <= 0 {
			delay = apiduoLocalImageAsyncRetryDelay(attempt)
		}
		select {
		case <-requestCtx.Done():
			apiduoFailLocalTask(ctx, task, "the upstream kept rate limiting this image request")
			return
		case <-time.After(delay):
		}
	}
	if requestErr != nil {
		apiduoFailLocalTask(ctx, task, requestErr.Error())
		return
	}

	url2, parseErr := apiduoLocalImageResultURL(payload)
	if parseErr != nil {
		apiduoFailLocalTask(ctx, task, parseErr.Error())
		return
	}
	apiduoSucceedLocalTask(ctx, task, url2)
}

// apiduoLocalImageResultURL reads the image address out of the vendor reply.
//
// Only a URL is accepted. Inlining base64 would put the bytes into the task row
// and then into every poll response, so a vendor configured to answer that way
// is reported as a configuration problem instead of silently stored.
func apiduoLocalImageResultURL(payload []byte) (string, error) {
	var decoded struct {
		Data []struct {
			URL string `json:"url"`
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("upstream did not return valid JSON")
	}
	for _, item := range decoded.Data {
		if strings.TrimSpace(item.B64) != "" && strings.TrimSpace(item.URL) == "" {
			return "", fmt.Errorf("upstream returned inline base64; this channel needs a URL response")
		}
		if url := strings.TrimSpace(item.URL); url != "" {
			return urlguard.ValidateTaskResultURL(url)
		}
	}
	return "", fmt.Errorf("upstream reported success without an image url")
}

func apiduoSucceedLocalTask(ctx context.Context, task *model.Task, resultURL string) {
	from := task.Status
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"
	task.FinishTime = time.Now().Unix()
	task.PrivateData.ResultURL = resultURL
	won, err := task.UpdateWithStatus(from)
	if err != nil || !won {
		logger.LogWarn(ctx, common.LogText("local image task %s changed state before it could be completed", task.TaskID))
	}
}

// apiduoFailLocalTask marks the task failed and refunds it.
//
// The refund happens only after the CAS wins: a row that another path already
// settled must not be refunded a second time.
func apiduoFailLocalTask(ctx context.Context, task *model.Task, reason string) {
	from := task.Status
	now := time.Now().Unix()
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.FinishTime = now
	task.FailReason = reason
	won, err := task.UpdateWithStatus(from)
	if err != nil || !won {
		logger.LogWarn(ctx, common.LogText("local image task %s changed state before it could be failed", task.TaskID))
		return
	}
	if task.Quota != 0 {
		RefundTaskQuota(ctx, task, reason)
	}
}

func apiduoTruncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// APIDUO:local-image-async END

// APIDUO:local-image-async:BEGIN

// apiduoLocalImageAsyncBatch bounds how many local tasks one scheduling pass
// dispatches, and apiduoLocalImageAsyncSlots bounds how many run at once in one
// process. They are the same number on purpose: a pass should be able to fill
// the process, and no more, so the slot is never the thing that silently drops
// work after a row was already claimed.
//
// The number is about the vendor, not the CPU: every slot holds one upstream
// connection for the whole generation. Eight is a conservative default; a
// deployment that expects bursts raises it, and the ceiling stops a typo from
// opening sixty-four connections at once.
var apiduoLocalImageAsyncBatch = apiduoLocalImageAsyncConcurrency()

const (
	// Each slot holds one upstream connection for a whole generation, and the
	// work is waiting on a vendor, not on this process. Sixteen is the default
	// because a burst that queues behind a smaller pool costs latency without
	// buying anything: the vendor's own limit is enforced by its 429s, which the
	// worker answers by moving the task or waiting.
	apiduoLocalImageAsyncDefaultConcurrency = 16
	apiduoLocalImageAsyncMaxConcurrency     = 64
)

func apiduoLocalImageAsyncConcurrency() int {
	raw := strings.TrimSpace(os.Getenv("LOCAL_IMAGE_ASYNC_CONCURRENCY"))
	if raw == "" {
		return apiduoLocalImageAsyncDefaultConcurrency
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return apiduoLocalImageAsyncDefaultConcurrency
	}
	return min(value, apiduoLocalImageAsyncMaxConcurrency)
}

var apiduoLocalImageAsyncSlots = make(chan struct{}, apiduoLocalImageAsyncBatch)

var (
	apiduoLocalImageAsyncKick     = make(chan struct{}, 1)
	apiduoLocalImageAsyncKickOnce sync.Once
)

// KickLocalImageAsyncWorker starts a dispatch immediately.
//
// The periodic pass is a safety net, not the delivery path: waiting for it adds
// up to one scheduler interval between "the client was told the task exists" and
// "the upstream was asked to start". A submission already knows it created work,
// so it says so. Signals coalesce — a burst of submissions is one pass, and a
// finished task signals again because it freed a slot.
func KickLocalImageAsyncWorker() {
	if !common.LocalImageAsyncEnabled() {
		return
	}
	apiduoLocalImageAsyncKickOnce.Do(func() {
		gopool.Go(apiduoLocalImageAsyncDispatcher)
	})
	select {
	case apiduoLocalImageAsyncKick <- struct{}{}:
	default:
	}
}

func apiduoLocalImageAsyncDispatcher() {
	for range apiduoLocalImageAsyncKick {
		apiduoDrainLocalImageAsyncQueue()
	}
}

// apiduoDrainLocalImageAsyncQueue dispatches until the process is full or the
// queue is empty.
//
// Wrapped in its own recover: this is the only loop that turns "a slot was
// freed" into "start another task". If it unwinds, immediate dispatch is gone
// for the lifetime of the process and every task waits for the 15-second
// scheduler pass instead — a silent capacity regression, not a visible error.
func apiduoDrainLocalImageAsyncQueue() {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.LogError(context.Background(),
				common.LogText("local image async dispatcher recovered: %v", recovered))
		}
	}()
	for RunLocalImageAsyncPass(context.Background(), apiduoLocalImageAsyncBatch) == apiduoLocalImageAsyncBatch {
	}
}

// apiduoRunLocalTaskGuarded runs one task and reports a panic instead of
// letting it escape.
//
// The worker parses vendor payloads and talks to the database, so it can hit a
// nil dereference like any other code. Without this, one such bug takes down the
// whole gateway — every unrelated request included — because a panic in a
// goroutine is not recoverable by its caller.
func apiduoRunLocalTaskGuarded(ctx context.Context, task *model.Task, run func()) (recovered any) {
	defer func() {
		if value := recover(); value != nil {
			recovered = value
			taskID := ""
			if task != nil {
				taskID = task.TaskID
			}
			logger.LogError(ctx, common.LogText("local image task %s panicked: %v", taskID, value))
		}
	}()
	run()
	return nil
}

// APIDUO:local-image-async:END

// APIDUO:local-image-async:BEGIN

// Admission limits. The queue is the task table, so without a ceiling a burst
// is accepted in full: every submission reserves quota immediately and the last
// one waits behind all the others. The per-user share stops one client from
// filling the global queue and starving everyone else.
const (
	apiduoLocalImageAsyncDefaultQueueLimit     = 200
	apiduoLocalImageAsyncDefaultUserQueueLimit = 20
)

// ErrLocalImageAsyncQueueFull is returned when admission refuses a submission.
var ErrLocalImageAsyncQueueFull = errors.New("the asynchronous image queue is full; retry later")

// AdmitLocalImageAsync reports whether another local task may be queued.
//
// Checked before pricing: a rejected submission must not reserve quota, and the
// rejection itself should be the cheapest thing the gateway does.
func AdmitLocalImageAsync(userID int) error {
	total, mine, err := model.CountLocalImageAsyncQueue(userID)
	if err != nil {
		// A count failure is not a reason to refuse work the gateway can still
		// store; the queue limit is a protection, not an invariant.
		return nil
	}
	if total >= int64(apiduoLocalImageAsyncEnvLimit("LOCAL_IMAGE_ASYNC_MAX_QUEUE", apiduoLocalImageAsyncDefaultQueueLimit)) {
		return fmt.Errorf("%w (%d waiting)", ErrLocalImageAsyncQueueFull, total)
	}
	if mine >= int64(apiduoLocalImageAsyncEnvLimit("LOCAL_IMAGE_ASYNC_MAX_QUEUE_PER_USER", apiduoLocalImageAsyncDefaultUserQueueLimit)) {
		return fmt.Errorf("%w (%d of your requests waiting)", ErrLocalImageAsyncQueueFull, mine)
	}
	return nil
}

func apiduoLocalImageAsyncEnvLimit(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// APIDUO:local-image-async:END

// apiduoLocalImageAsyncReapCutoff is the age past which a running claim is
// considered abandoned: the task's own timeout plus a minute for the worker to
// observe it and write the failure itself.
func apiduoLocalImageAsyncReapCutoff(now int64) int64 {
	return now - int64((apiduoLocalImageAsyncTimeout+time.Minute)/time.Second)
}

// apiduoLocalImageAsyncInterleaved reorders candidates so consecutive picks
// alternate between channels, preserving each channel's own order.
func apiduoLocalImageAsyncInterleaved(tasks []*model.Task) []*model.Task {
	if len(tasks) < 2 {
		return tasks
	}
	order := make([]int, 0, 4)
	queues := make(map[int][]*model.Task, 4)
	for _, task := range tasks {
		if _, seen := queues[task.ChannelId]; !seen {
			order = append(order, task.ChannelId)
		}
		queues[task.ChannelId] = append(queues[task.ChannelId], task)
	}
	if len(order) < 2 {
		return tasks
	}
	out := make([]*model.Task, 0, len(tasks))
	for len(out) < len(tasks) {
		for _, channelID := range order {
			if queue := queues[channelID]; len(queue) > 0 {
				out = append(out, queue[0])
				queues[channelID] = queue[1:]
			}
		}
	}
	return out
}

// apiduoLocalImageAsyncInFlight counts, per channel, how many tasks this process
// is running right now.
//
// A counter rather than a channel-backed semaphore because the allowance depends
// on the pool size, which is configuration: a fixed buffered channel would have
// to be rebuilt when it changes.
var (
	apiduoLocalImageAsyncInFlight   = map[int]int{}
	apiduoLocalImageAsyncInFlightMu sync.Mutex
)

// apiduoLocalImageAsyncHoldChannel reserves one slot for a channel.
//
// The allowance is what the channel declares, clamped to the pool; without a
// declaration it is one less than the pool, so a channel can never own the
// process outright and a request arriving behind a flood still starts. With a
// pool of one there is nothing to reserve.
func apiduoLocalImageAsyncHoldChannel(channelID int, limit int) bool {
	allowance := max(1, limit-1)
	if channel, err := model.GetChannelById(channelID, false); err == nil && channel != nil {
		// The vendor's own concurrency limit is the only defensible number here,
		// and the channel is where it is written down.
		if declared := channel.GetOtherSettings().LocalWorker; declared != nil && declared.Concurrency > 0 {
			allowance = min(declared.Concurrency, limit)
		}
	}
	apiduoLocalImageAsyncInFlightMu.Lock()
	defer apiduoLocalImageAsyncInFlightMu.Unlock()
	if apiduoLocalImageAsyncInFlight[channelID] >= allowance {
		return false
	}
	apiduoLocalImageAsyncInFlight[channelID]++
	return true
}

func apiduoLocalImageAsyncReleaseChannel(channelID int) {
	apiduoLocalImageAsyncInFlightMu.Lock()
	defer apiduoLocalImageAsyncInFlightMu.Unlock()
	if apiduoLocalImageAsyncInFlight[channelID] > 1 {
		apiduoLocalImageAsyncInFlight[channelID]--
		return
	}
	delete(apiduoLocalImageAsyncInFlight, channelID)
}

// apiduoLocalReplay is what one attempt at a vendor answered.
type apiduoLocalReplay struct {
	Payload    []byte
	Status     int
	RetryAfter time.Duration
}

// apiduoReplayLocalTask posts the frozen request once.
//
// One attempt per call; the caller decides whether another is worth making. The
// retry that matters here is not a network retry — a synchronous image
// generation is not idempotent, so a 5xx may mean "an image exists and the reply
// was lost". Only a rate limit is retried, because it proves the vendor rejected
// the request before doing anything.
func apiduoReplayLocalTask(ctx context.Context, client *http.Client, url, key string, body []byte) (apiduoLocalReplay, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return apiduoLocalReplay{}, errors.New("failed to build the upstream request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if strings.TrimSpace(key) != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}

	response, err := client.Do(request)
	if err != nil {
		return apiduoLocalReplay{}, fmt.Errorf("the upstream request failed: %w", err)
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if readErr != nil {
		return apiduoLocalReplay{Status: response.StatusCode}, errors.New("failed to read the upstream response")
	}
	replay := apiduoLocalReplay{
		Payload:    payload,
		Status:     response.StatusCode,
		RetryAfter: apiduoLocalImageAsyncRetryAfter(response.Header.Get("Retry-After")),
	}
	if response.StatusCode/100 != 2 {
		return replay, fmt.Errorf("upstream returned %d: %s",
			response.StatusCode, apiduoTruncate(string(payload), 512))
	}
	return replay, nil
}

// APIDUO:local-image-async END

// APIDUO:local-failover:BEGIN

// LocalImageURLResolver turns one channel plus one model into the address that
// channel serves images at. It is injected by the controller because resolving a
// URL needs the relay adaptors, and relay imports this package.
var LocalImageURLResolver func(channel *model.Channel, upstreamModel string) (string, error)

// apiduoLocalImageAsyncMaxChannels bounds how many channels one task may try.
// A task that has been rate-limited three times is not unlucky, it is early.
const apiduoLocalImageAsyncMaxChannels = 3

// apiduoRotateLocalTask moves a rate-limited task to another channel and reports
// whether it did.
//
// Rate limiting is the one failure worth re-dispatching rather than refunding:
// nothing was generated, so no image exists to duplicate. What cannot be retried
// is the same channel — a channel that just said "too many requests" will say it
// again. The useful recovery is another channel, which is why this re-points the
// task instead of re-sending it.
//
// The search is the same shape as the original submission, not a narrower one:
// priority tiers are walked in order (a lower tier is a channel the operator
// deliberately ranked below the first, which is exactly what a fallback is), and
// if the client's model name finds nothing, the upstream name does — a channel
// may expose the vendor's name directly while another reaches it through a
// mapping.
func apiduoRotateLocalTask(ctx context.Context, task *model.Task, exhausted *model.Channel) bool {
	snapshot := task.PrivateData.Execution
	if snapshot == nil || exhausted == nil || LocalImageURLResolver == nil {
		return false
	}
	if len(snapshot.TriedChannels) >= apiduoLocalImageAsyncMaxChannels-1 {
		return false
	}
	originModel := strings.TrimSpace(task.Properties.OriginModelName)
	if originModel == "" || strings.TrimSpace(task.Group) == "" {
		return false
	}

	tried := append(slices.Clone(snapshot.TriedChannels), exhausted.Id)
	filters := []dto.ChannelFilter{
		{Kind: dto.FilterExcludeChannels, ExcludeChannelIDs: tried},
		{Kind: dto.FilterRequestPath, RequestPath: endpoint.AsyncImageSubmitPath},
	}

	// Selected by the client's model name, never by the vendor name the current
	// channel mapped it to. A channel's model list is the operator's statement of
	// what a client may ask it for; a channel that only declares the vendor name
	// was never offered for this request, and its cost basis and group policy may
	// belong to that other name entirely. Reaching it would spend money on a
	// model nobody approved for this price.
	//
	// retry=0 is already "the best remaining tier": the exclusion filter runs
	// before tiers are counted, so the candidate set the selector ranks never
	// contains a channel this task has tried.
	next, err := model.GetRandomSatisfiedChannel(task.Group, originModel, 0, filters)
	if err != nil || next == nil {
		return false
	}
	served := originModel

	// The name the new channel must be asked for. When it was found through the
	// upstream name, applying its own mapping again would rename it twice.
	upstreamModel, _, mapErr := modelalias.ApplyModelMapping(next.GetModelMapping(), served)
	if mapErr != nil {
		return false
	}
	url, err := LocalImageURLResolver(next, upstreamModel)
	if err != nil {
		return false
	}

	snapshot.TriedChannels = tried
	snapshot.UpstreamURL = url
	task.ChannelId = next.Id
	task.Status = model.TaskStatusNotStart
	task.Progress = "0%"
	task.StartTime = 0
	// The credential binding belonged to the channel that just failed.
	task.PrivateData.UpstreamKey = ""
	task.PrivateData.UpstreamKeyAt = 0
	task.PrivateData.MultiKeyIndex = 0
	task.PrivateData.MultiKeyFingerprint = ""
	task.Properties.UpstreamModelName = upstreamModel

	won, err := task.UpdateWithStatus(model.TaskStatusInProgress)
	if err != nil || !won {
		return false
	}
	logger.LogInfo(ctx, common.LogText("local image task %s moved from channel %d to %d (model %s) after a rate limit",
		task.TaskID, exhausted.Id, next.Id, served))
	KickLocalImageAsyncWorker()
	return true
}

// APIDUO:local-failover:END

// APIDUO:local-rate-limit:BEGIN

// A rate-limited task waits for the vendor instead of refunding, but the wait is
// bounded and measured against a generation, not a network hiccup: the vendor is
// refusing because its own concurrency is full, and its concurrency frees up when
// the generations in front of it finish.
const (
	apiduoLocalImageAsyncRateLimitAttempts = 3
	apiduoLocalImageAsyncRetryBaseDelay    = 8 * time.Second
	apiduoLocalImageAsyncRetryMaxDelay     = 25 * time.Second
)

// apiduoLocalImageAsyncRetryDelay backs off between attempts to a vendor that
// said "too many requests": 8 seconds, then 16.
func apiduoLocalImageAsyncRetryDelay(attempt int) time.Duration {
	delay := apiduoLocalImageAsyncRetryBaseDelay
	for range attempt - 1 {
		delay *= 2
	}
	return min(delay, apiduoLocalImageAsyncRetryMaxDelay)
}

// apiduoLocalImageAsyncRetryAfter reads the vendor's own instruction, capped:
// waiting a minute inside a five-minute task leaves no room for the generation.
func apiduoLocalImageAsyncRetryAfter(raw string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, apiduoLocalImageAsyncRetryMaxDelay)
}

// APIDUO:local-rate-limit:END

// APIDUO:local-body:BEGIN

// RebuildLocalImageBody rewrites the one member a model mapping changes and
// leaves every other byte's meaning intact.
//
// Decoded with UseNumber: a body carries interleaved integers (seeds, counts,
// ids) that float64 would round, and a silently altered seed is a different
// image request.
func RebuildLocalImageBody(raw []byte, model string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	payload["model"] = model
	return json.Marshal(payload)
}

// apiduoLocalImageBody prepares the stored client request for one channel.
//
// Preparation happens here, not at submission, because a task can move: a
// rate-limited channel hands the task to another one, and each channel may name
// the model differently. Freezing the mapped name would make every channel that
// maps the model unusable as a fallback — which is exactly backwards, since a
// mapping is how an operator declares "this channel can serve that model".
func apiduoLocalImageBody(channel *model.Channel, task *model.Task) ([]byte, string, error) {
	snapshot := task.PrivateData.Execution
	origin := snapshot.OriginRequest
	if len(origin) == 0 {
		// A row written before this field existed already holds a body prepared
		// for the channel it names; sending it verbatim is the only safe reading.
		return snapshot.Request, task.Properties.UpstreamModelName, nil
	}
	// Always resolved from the client's model through this channel's mapping: the
	// task may run on a channel other than the one that accepted it, and each may
	// name the same vendor model differently.
	upstream, _, err := modelalias.ApplyModelMapping(channel.GetModelMapping(), task.Properties.OriginModelName)
	if err != nil {
		return nil, "", err
	}
	body, err := RebuildLocalImageBody(origin, upstream)
	if err != nil {
		return nil, "", err
	}
	return body, upstream, nil
}

// APIDUO:local-body:END
