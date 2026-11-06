package controller

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/filerelay"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

// APIDUO:file-relay:BEGIN
//
// The five handlers below implement OpenAI's /v1/files surface on top of the
// gateway's own file records.
//
// Only the upload selects a channel, and it selects by purpose: the request
// body carries no model, so there is nothing else to route on. Every read path
// resolves a gateway-issued id and replays it to the channel that created it,
// which is also the authorization boundary — the lookup is scoped by user id.

// maxFileRequestBodyBytes caps an upload body before multipart parsing spools
// it to disk. The per-channel ceiling is enforced later, but by then gin has
// already buffered the whole part, so an unbounded body is a disk-fill attack
// rather than a rejected upload. 512 MiB is the largest per-file limit among the
// providers this gateway speaks to.
const maxFileRequestBodyBytes = int64(512) << 20

// multipartMemoryBytes keeps small parts in memory and spools the rest.
const multipartMemoryBytes = int32(32) << 20

type fileObjectView struct {
	Id        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	// Status is echoed from the provider so a client sees its own vocabulary
	// (ready / processed / uploaded) instead of a value the gateway invented.
	Status   string `json:"status,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

func fileObjectToView(object *model.FileObject) fileObjectView {
	createdAt := object.UpstreamCreatedAt
	if createdAt <= 0 {
		createdAt = object.CreatedAt
	}
	status := object.Status
	if status == "" {
		status = "processed"
	}
	return fileObjectView{
		Id:        object.GatewayId,
		Object:    "file",
		Bytes:     object.Bytes,
		CreatedAt: createdAt,
		Filename:  object.Filename,
		Purpose:   object.Purpose,
		Status:    status,
		MimeType:  object.MimeType,
	}
}

// FileCreate relays an upload to the channel that declares this purpose.
func FileCreate(c *gin.Context) {
	userId := c.GetInt("id")
	// An upload burns provider storage and bandwidth but no tokens, so the
	// allowance checks below run before anything leaves the gateway.
	settings := system_setting.GetFileRelaySetting()
	if settings == nil || !settings.Enabled {
		fileError(c, http.StatusServiceUnavailable, "file_relay_disabled", "file uploads are disabled on this gateway")
		return
	}

	if c.Request.ContentLength > maxFileRequestBodyBytes {
		fileError(c, http.StatusRequestEntityTooLarge, "request_too_large", "the upload exceeds the gateway limit")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxFileRequestBodyBytes)
	// Parse explicitly so an oversized body surfaces as 413 here instead of
	// silently turning into "purpose is required" when PostForm swallows the
	// parse error.
	if err := c.Request.ParseMultipartForm(int64(multipartMemoryBytes)); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			fileError(c, http.StatusRequestEntityTooLarge, "request_too_large", "the upload exceeds the gateway limit")
			return
		}
		fileError(c, http.StatusBadRequest, "invalid_file", "the request is not valid multipart form data")
		return
	}

	purpose := strings.TrimSpace(c.PostForm("purpose"))
	if purpose == "" {
		fileError(c, http.StatusBadRequest, "invalid_purpose", "purpose is required")
		return
	}

	header, err := c.FormFile("file")
	if err != nil {
		fileError(c, http.StatusBadRequest, "invalid_file", "a file part is required")
		return
	}
	// An empty file costs no megabytes, so charging per megabyte would let a
	// caller park unlimited objects for free. Providers reject them anyway.
	if header.Size <= 0 {
		fileError(c, http.StatusBadRequest, "empty_file", "the uploaded file is empty")
		return
	}
	if err := filerelay.CheckAllowance(userId, header.Size, settings); err != nil {
		writeAllowanceError(c, err)
		return
	}

	source, err := header.Open()
	if err != nil {
		fileError(c, http.StatusBadRequest, "invalid_file", "the uploaded file could not be read")
		return
	}
	defer source.Close()

	// The form is fully parsed by this point, so a large file lives in a spool
	// file and the relay streams from there. Reading parts in arrival order
	// would be cheaper, but purpose may follow the file and routing cannot wait.
	mimeType, err := detectUploadedMime(source)
	if err != nil {
		fileError(c, http.StatusBadRequest, "invalid_file", "the uploaded file could not be read")
		return
	}

	group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	selection, err := filerelay.SelectChannel(purpose, group)
	if err != nil {
		fileError(c, http.StatusServiceUnavailable, "no_file_channel", err.Error())
		return
	}
	channel := selection.Channel
	backend, err := filerelay.BackendForChannel(channel)
	if err != nil {
		fileError(c, http.StatusServiceUnavailable, "unsupported_channel", err.Error())
		return
	}

	tokenId := c.GetInt("token_id")
	tokenKey := c.GetString("token_key")
	quota := filerelay.UploadQuota(header.Size, settings)
	if quota > 0 {
		reserved, reserveErr := model.TryReserveTokenQuota(tokenId, tokenKey, quota, c.GetBool("token_unlimited_quota"))
		if reserveErr != nil {
			fileError(c, http.StatusInternalServerError, "quota_reserve_failed", "the upload quota could not be reserved")
			return
		}
		if !reserved {
			fileError(c, http.StatusPaymentRequired, "insufficient_quota", "not enough quota for this upload")
			return
		}
	}

	result, err := backend.Upload(c.Request.Context(), channel, &filerelay.UploadInput{
		Filename:    header.Filename,
		Purpose:     purpose,
		ContentType: mimeType,
		Size:        header.Size,
		MaxBytes:    selection.MaxUploadBytes,
		Body:        source,
	})
	if err != nil {
		refundUploadQuota(tokenId, tokenKey, quota)
		writeFileRelayError(c, err)
		return
	}

	object := &model.FileObject{
		GatewayId:         model.GenerateGatewayFileId(),
		UserId:            userId,
		TokenId:           tokenId,
		ChannelId:         channel.Id,
		UpstreamId:        result.Object.ID,
		Purpose:           purpose,
		Filename:          header.Filename,
		Bytes:             result.Object.Bytes,
		MimeType:          mimeType,
		ContentKind:       result.ContentKind,
		InlineContent:     string(result.Inline),
		Status:            result.Object.Status,
		UpstreamCreatedAt: result.Object.CreatedAt,
		// CreatedAt is when the gateway accepted the file, not the timestamp the
		// provider echoed back: the daily upload window is measured against this
		// column, and a provider clock that drifts would skew the count.
		CreatedAt: common.GetTimestamp(),
		ExpiresAt: resolveFileExpiry(c.PostForm("expires_after[seconds]")),
	}
	if object.Bytes == 0 {
		object.Bytes = header.Size
	}
	if object.MimeType == "" || object.MimeType == "application/octet-stream" {
		object.MimeType = result.MimeType
	}
	if err := model.CreateFileObject(object); err != nil {
		// The provider already holds the bytes; report failure clearly rather
		// than returning an id the gateway cannot resolve later.
		refundUploadQuota(tokenId, tokenKey, quota)
		if result.Object.ID != "" {
			if deleteErr := backend.Delete(c.Request.Context(), channel, result.Object.ID); deleteErr != nil {
				common.SysError(common.LogText("file relay: orphaned upstream file %s on channel %d: %s", result.Object.ID, channel.Id, deleteErr.Error()))
			}
		}
		fileError(c, http.StatusInternalServerError, "file_record_failed", "the file was stored upstream but could not be recorded")
		return
	}
	if quota > 0 {
		// Token quota was reserved before the upload; the wallet side is only
		// charged once the file is actually usable.
		if err := model.DecreaseUserQuota(userId, quota, false); err != nil {
			common.SysError(common.LogText("file relay: failed to charge user %d for upload %s: %s", userId, object.GatewayId, err.Error()))
		}
		model.UpdateUserUsedQuotaAndRequestCount(userId, quota)
	}

	c.JSON(http.StatusOK, fileObjectToView(object))
}

// FileList answers from the gateway's records. Provider cursors are not
// portable across channels, and a local page always contains resolvable ids.
func FileList(c *gin.Context) {
	userId := c.GetInt("id")
	limit, _ := strconv.Atoi(c.Query("limit"))
	objects, hasMore, err := model.ListFileObjects(model.FileObjectListQuery{
		UserId:  userId,
		Purpose: strings.TrimSpace(c.Query("purpose")),
		After:   strings.TrimSpace(c.Query("after")),
		Order:   strings.TrimSpace(c.Query("order")),
		Limit:   limit,
	})
	if err != nil {
		fileError(c, http.StatusInternalServerError, "file_list_failed", "the file list could not be read")
		return
	}
	data := make([]fileObjectView, 0, len(objects))
	for i := range objects {
		data = append(data, fileObjectToView(&objects[i]))
	}
	payload := gin.H{
		"object":   "list",
		"data":     data,
		"has_more": hasMore,
	}
	if len(data) > 0 {
		payload["first_id"] = data[0].Id
		payload["last_id"] = data[len(data)-1].Id
	}
	c.JSON(http.StatusOK, payload)
}

func FileRetrieve(c *gin.Context) {
	userId := c.GetInt("id")
	object, found, err := model.GetFileObjectByGatewayId(userId, c.Param("id"))
	if err != nil {
		fileError(c, http.StatusInternalServerError, "file_lookup_failed", "the file record could not be read")
		return
	}
	if !found {
		fileError(c, http.StatusNotFound, "file_not_found", "no such file")
		return
	}
	if fileExpired(object) {
		fileError(c, http.StatusNotFound, "file_not_found", "no such file")
		return
	}
	c.JSON(http.StatusOK, fileObjectToView(object))
}

// FileDelete removes the record even when the provider-side delete fails: a row
// the gateway cannot use is worse than an orphaned object upstream, and the
// orphan is reported in the log.
func FileDelete(c *gin.Context) {
	userId := c.GetInt("id")
	object, found, err := model.GetFileObjectByGatewayId(userId, c.Param("id"))
	if err != nil {
		fileError(c, http.StatusInternalServerError, "file_lookup_failed", "the file record could not be read")
		return
	}
	if !found {
		fileError(c, http.StatusNotFound, "file_not_found", "no such file")
		return
	}
	if channel, channelErr := model.GetChannelById(object.ChannelId, true); channelErr == nil && channel != nil {
		if backend, backendErr := filerelay.BackendForChannel(channel); backendErr == nil && object.UpstreamId != "" {
			if deleteErr := backend.Delete(c.Request.Context(), channel, object.UpstreamId); deleteErr != nil {
				common.SysError(common.LogText("file relay: upstream delete failed for %s on channel %d: %s", object.GatewayId, object.ChannelId, deleteErr.Error()))
			}
		}
	}
	deleted, err := model.DeleteFileObject(userId, object.GatewayId)
	if err != nil || !deleted {
		fileError(c, http.StatusInternalServerError, "file_delete_failed", "the file record could not be removed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": object.GatewayId, "object": "file", "deleted": true})
}

// FileContent streams the bytes back. When the provider answered with parsed
// text at upload time there is no upstream id to replay, and the stored text is
// served directly.
func FileContent(c *gin.Context) {
	userId := c.GetInt("id")
	object, found, err := model.GetFileObjectByGatewayId(userId, c.Param("id"))
	if err != nil {
		fileError(c, http.StatusInternalServerError, "file_lookup_failed", "the file record could not be read")
		return
	}
	if !found {
		fileError(c, http.StatusNotFound, "file_not_found", "no such file")
		return
	}

	if fileExpired(object) {
		fileError(c, http.StatusNotFound, "file_not_found", "no such file")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+sanitizeFilename(object.Filename)+`"`)

	if object.InlineContent != "" {
		c.Data(http.StatusOK, fileContentType(object), []byte(object.InlineContent))
		return
	}

	channel, err := model.GetChannelById(object.ChannelId, true)
	if err != nil || channel == nil {
		fileError(c, http.StatusGone, "channel_unavailable", "the channel that holds this file no longer exists")
		return
	}
	backend, err := filerelay.BackendForChannel(channel)
	if err != nil {
		fileError(c, http.StatusGone, "unsupported_channel", err.Error())
		return
	}
	content, err := backend.Content(c.Request.Context(), channel, object.UpstreamId)
	if err != nil {
		writeFileRelayError(c, err)
		return
	}
	defer content.Body.Close()

	// The provider knows what it is about to send. The upload MIME describes the
	// original file, which is the wrong answer whenever the provider answers with
	// an extraction: a PDF upload whose content endpoint returns text would be
	// labelled application/pdf.
	contentType := strings.TrimSpace(content.ContentType)
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = fileContentType(object)
	}
	c.Header("Content-Type", contentType)
	if content.Length >= 0 {
		c.Header("Content-Length", strconv.FormatInt(content.Length, 10))
	}
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, content.Body)
}

func fileExpired(object *model.FileObject) bool {
	return object.ExpiresAt > 0 && object.ExpiresAt <= common.GetTimestamp()
}

func fileContentType(object *model.FileObject) string {
	if object.ContentKind == model.FileContentKindText {
		return "text/markdown; charset=utf-8"
	}
	if mime := strings.TrimSpace(object.MimeType); mime != "" && mime != "application/octet-stream" {
		return mime
	}
	return "application/octet-stream"
}

// detectUploadedMime sniffs the leading bytes and rewinds, because a client
// supplied Content-Type is a claim rather than evidence.
func detectUploadedMime(source multipart.File) (string, error) {
	head := make([]byte, 512)
	n, readErr := io.ReadFull(source, head)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return "", readErr
	}
	// Failing to rewind must abort the upload: continuing would forward the file
	// starting after the sniffed bytes, silently corrupting it.
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind upload after sniffing: %w", err)
	}
	if n == 0 {
		return "application/octet-stream", nil
	}
	return http.DetectContentType(head[:n]), nil
}

// resolveFileExpiry honours expires_after[seconds] but never lets a client pin a
// file beyond the OpenAI 30-day ceiling, which is the longest any provider here
// keeps an object.
func resolveFileExpiry(raw string) int64 {
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	const ceiling = int64(30 * 24 * 3600)
	if seconds > ceiling {
		seconds = ceiling
	}
	return common.GetTimestamp() + seconds
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer("\\", "_", "/", "_", "\"", "_", "\r", "", "\n", "")
	name = replacer.Replace(name)
	if name == "" {
		return "file"
	}
	return name
}

func writeFileRelayError(c *gin.Context, err error) {
	var limitErr *filerelay.LimitError
	if errors.As(err, &limitErr) {
		fileError(c, http.StatusRequestEntityTooLarge, "file_too_large", limitErr.Error())
		return
	}
	var upstreamErr *filerelay.UpstreamError
	if errors.As(err, &upstreamErr) {
		status := upstreamErr.StatusCode
		code := "upstream_file_error"
		// An exhausted balance arrives as 429, which a client reads as "retry
		// later" and will loop on forever. The provider's error code is the only
		// reliable signal that the caller has to top up instead.
		if isQuotaExhaustedCode(upstreamErr.Code) {
			status = http.StatusPaymentRequired
			code = "insufficient_quota"
		} else if status < 400 || status >= 500 {
			// A provider 5xx is not this client's problem: the gateway is the one
			// the client talks to, so surface 502.
			status = http.StatusBadGateway
		}
		fileError(c, status, code, upstreamErr.Message)
		return
	}
	if errors.Is(err, filerelay.ErrNoPurposeChannel) {
		fileError(c, http.StatusServiceUnavailable, "no_file_channel", err.Error())
		return
	}
	fileError(c, http.StatusBadGateway, "file_relay_failed", err.Error())
}

// writeAllowanceError maps a refusal onto a status the client can act on.
func writeAllowanceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, filerelay.ErrStorageFull):
		fileError(c, http.StatusForbidden, "storage_quota_exceeded", err.Error())
	case errors.Is(err, filerelay.ErrDailyLimit):
		fileError(c, http.StatusTooManyRequests, "daily_upload_limit", err.Error())
	default:
		fileError(c, http.StatusInternalServerError, "allowance_check_failed", "the upload allowance could not be checked")
	}
}

// refundUploadQuota returns a reservation that never turned into a usable file.
func refundUploadQuota(tokenId int, tokenKey string, quota int) {
	if quota <= 0 {
		return
	}
	if err := model.IncreaseTokenQuota(tokenId, tokenKey, quota); err != nil {
		common.SysError(common.LogText("file relay: failed to refund %d quota to token %d: %s", quota, tokenId, err.Error()))
	}
}

// isQuotaExhaustedCode reports whether the provider refused the upload because
// the account behind this channel has no balance left, rather than because of
// rate limiting.
func isQuotaExhaustedCode(code string) bool {
	switch strings.ToLower(code) {
	case "exceeded_current_quota_error", "insufficient_quota", "quota_exceeded", "billing_hard_limit_reached":
		return true
	default:
		return false
	}
}

func fileError(c *gin.Context, status int, code string, message string) {
	errorType := "invalid_request_error"
	switch {
	case status == http.StatusPaymentRequired:
		errorType = "insufficient_quota"
	case status >= 500:
		errorType = "api_error"
	}
	c.JSON(status, gin.H{
		"error": types.OpenAIError{
			Message: message,
			Type:    errorType,
			Code:    code,
		},
	})
}

// APIDUO:file-relay:END
