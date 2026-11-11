package advancedcustom

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

var apiduoTestPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type apiduoTestPart struct {
	field       string
	filename    string
	contentType string
	data        []byte
}

func apiduoTestMultipartBody(t *testing.T, fields [][2]string, parts []apiduoTestPart) (*bytes.Buffer, string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			t.Fatalf("write field %s: %v", field[0], err)
		}
	}
	for _, part := range parts {
		header := make(textproto.MIMEHeader)
		header.Set(
			"Content-Disposition",
			fmt.Sprintf(`form-data; name=%q; filename=%q`, part.field, part.filename),
		)
		if part.contentType != "" {
			header.Set("Content-Type", part.contentType)
		}
		section, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create part %s: %v", part.field, err)
		}
		if _, err := section.Write(part.data); err != nil {
			t.Fatalf("write part %s: %v", part.field, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, writer.FormDataContentType()
}

func apiduoTestForm(t *testing.T, fields [][2]string, parts []apiduoTestPart) *multipart.Form {
	t.Helper()

	body, contentType := apiduoTestMultipartBody(t, fields, parts)
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}

	form, err := multipart.NewReader(body, params["boundary"]).ReadForm(1 << 22)
	if err != nil {
		t.Fatalf("read form: %v", err)
	}
	t.Cleanup(func() {
		_ = form.RemoveAll()
	})
	return form
}

func apiduoTestDecode(t *testing.T, form *multipart.Form) map[string]any {
	t.Helper()

	body, err := foldEditsMultipart(form)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return decoded
}

func TestShouldFoldEditsToJSON(t *testing.T) {
	cases := []struct {
		upstream string
		want     bool
	}{
		{"/api/v3/images/generations", true},
		{"/v1/images/generations", true},
		{"https://ark.example.com/api/v3/images/generations", true},
		{"/v1/images/edits", false},
		{"/api/v3/images/edits/", false},
		{"https://x.example.com/v1/images/edits", false},
		{"", false},
		{"   ", false},
	}

	for _, tc := range cases {
		if got := shouldFoldEditsToJSON(tc.upstream); got != tc.want {
			t.Errorf("shouldFoldEditsToJSON(%q) = %v, want %v", tc.upstream, got, tc.want)
		}
	}
}

func TestFoldEditsKeepsTextFieldsAndEncodesImage(t *testing.T) {
	form := apiduoTestForm(t,
		[][2]string{{"prompt", "换成蓝底"}, {"model", "doubao-seedream-5-0"}, {"n", "2"}, {"size", "2048x2048"}},
		[]apiduoTestPart{{field: "image", filename: "a.png", contentType: "image/png", data: apiduoTestPNG}},
	)

	body := apiduoTestDecode(t, form)

	if body["prompt"] != "换成蓝底" || body["model"] != "doubao-seedream-5-0" || body["size"] != "2048x2048" {
		t.Fatalf("text fields not forwarded: %v", body)
	}
	if body["n"] != float64(2) {
		t.Fatalf("n should be an int 2, got %#v", body["n"])
	}
	image, ok := body["image"].(string)
	if !ok {
		t.Fatalf("image should be a single string, got %#v", body["image"])
	}
	if !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Fatalf("image should be a data URI, got %s", image)
	}
}

func TestFoldEditsMultipleImagesBecomeArray(t *testing.T) {
	form := apiduoTestForm(t, [][2]string{{"prompt", "p"}}, []apiduoTestPart{
		{field: "image[]", filename: "a.png", data: apiduoTestPNG},
		{field: "image[]", filename: "b.png", data: apiduoTestPNG},
	})

	body := apiduoTestDecode(t, form)

	images, ok := body["image"].([]any)
	if !ok || len(images) != 2 {
		t.Fatalf("two uploads should become an array of 2, got %#v", body["image"])
	}
	for _, item := range images {
		if ref, ok := item.(string); !ok || !strings.HasPrefix(ref, "data:image/png;base64,") {
			t.Fatalf("each item should be a data URI, got %#v", item)
		}
	}
}

func TestFoldEditsDropsEmptyParts(t *testing.T) {
	form := apiduoTestForm(t,
		[][2]string{{"prompt", "p"}, {"image", ""}},
		[]apiduoTestPart{{field: "image", filename: "empty.png", data: nil}},
	)

	body := apiduoTestDecode(t, form)

	if _, exists := body["image"]; exists {
		t.Fatalf("empty upload means text-to-image, image key must be absent: %v", body)
	}
}

func TestFoldEditsEncodesMask(t *testing.T) {
	form := apiduoTestForm(t, [][2]string{{"prompt", "p"}}, []apiduoTestPart{
		{field: "image", filename: "a.png", data: apiduoTestPNG},
		{field: "mask", filename: "m.png", data: apiduoTestPNG},
	})

	body := apiduoTestDecode(t, form)

	if mask, ok := body["mask"].(string); !ok || !strings.HasPrefix(mask, "data:image/png;base64,") {
		t.Fatalf("mask should be a data URI string, got %#v", body["mask"])
	}
}

func TestFoldEditsNormalisesInvalidN(t *testing.T) {
	invalid := apiduoTestDecode(t, apiduoTestForm(t,
		[][2]string{{"prompt", "p"}, {"n", "abc"}}, nil))
	if invalid["n"] != float64(1) {
		t.Fatalf("invalid n must fall back to 1, got %#v", invalid["n"])
	}

	absent := apiduoTestDecode(t, apiduoTestForm(t, [][2]string{{"prompt", "p"}}, nil))
	if _, exists := absent["n"]; exists {
		t.Fatalf("absent n must stay absent, got %#v", absent["n"])
	}
}

func TestFoldEditsRejectsTextImageReference(t *testing.T) {
	// edits 只接受文件流：URL / data URI / 裸 base64 这类文本引用没有文件本体，必须 400。
	for _, ref := range []string{
		"https://cdn.example.com/a.png",
		"data:image/png;base64,iVBORw0KGgo=",
		"iVBORw0KGgo=",
	} {
		form := apiduoTestForm(t, [][2]string{{"prompt", "p"}, {"image", ref}}, nil)
		if _, err := foldEditsMultipart(form); err == nil {
			t.Fatalf("text reference %q must be rejected by /v1/images/edits", ref)
		} else if !strings.Contains(err.Error(), "file part") {
			t.Fatalf("error should point at the file-part requirement, got %v", err)
		}
	}
}

func TestFoldEditsRejectsTextMaskReference(t *testing.T) {
	form := apiduoTestForm(t,
		[][2]string{{"prompt", "p"}, {"mask", "https://cdn.example.com/m.png"}},
		[]apiduoTestPart{{field: "image", filename: "a.png", data: apiduoTestPNG}},
	)

	if _, err := foldEditsMultipart(form); err == nil {
		t.Fatal("mask must be a file part as well")
	}
}

func TestFoldEditsRejectsNonImageUpload(t *testing.T) {
	form := apiduoTestForm(t, [][2]string{{"prompt", "p"}}, []apiduoTestPart{
		{field: "image", filename: "note.txt", contentType: "text/plain", data: []byte("not an image")},
	})

	if _, err := foldEditsMultipart(form); err == nil {
		t.Fatal("a non-image upload must fail loudly instead of being forwarded")
	}
}

func TestFoldEditsFallsBackToDeclaredMime(t *testing.T) {
	// 字节嗅探不出图片（无 magic number），但客户端声明了 image/*，按声明收下。
	form := apiduoTestForm(t, [][2]string{{"prompt", "p"}}, []apiduoTestPart{
		{field: "image", filename: "x.heic", contentType: "image/heic", data: []byte{0x00, 0x01, 0x02, 0x03}},
	})

	body := apiduoTestDecode(t, form)

	image, ok := body["image"].(string)
	if !ok || !strings.HasPrefix(image, "data:image/heic;base64,") {
		t.Fatalf("declared image mime should be used as fallback, got %#v", body["image"])
	}
}

// edits 入口只接受文件流：JSON 载体没有文件本体，直接 400。
func TestPrepareImageProtocolEditsRejectsJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/images/edits",
		strings.NewReader(`{"model":"m","prompt":"p","image":"https://cdn.example.com/a.png"}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	adaptor := &Adaptor{}
	plan, handled, err := adaptor.prepareImageProtocol(c, apiduoImageTestInfo())
	if !handled {
		t.Fatal("edits request must be handled by the image protocol")
	}
	if err == nil || !strings.Contains(err.Error(), "multipart/form-data") {
		t.Fatalf("JSON body must be rejected with a file-stream hint, got plan=%v err=%v", plan, err)
	}
}

func TestPrepareImageProtocolEditsRejectsTextReference(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, contentType := apiduoTestMultipartBody(t,
		[][2]string{{"prompt", "p"}, {"image", "https://cdn.example.com/a.png"}}, nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	c.Request.Header.Set("Content-Type", contentType)

	adaptor := &Adaptor{}
	if _, handled, err := adaptor.prepareImageProtocol(c, apiduoImageTestInfo()); !handled {
		t.Fatal("edits request must be handled by the image protocol")
	} else if err == nil || !strings.Contains(err.Error(), "file part") {
		t.Fatalf("URL text reference must be rejected, got %v", err)
	}
}

func TestPrepareImageProtocolEditsAcceptsFileUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, contentType := apiduoTestMultipartBody(t,
		[][2]string{{"prompt", "p"}, {"model", "gpt-image-2"}},
		[]apiduoTestPart{{field: "image", filename: "a.png", contentType: "image/png", data: apiduoTestPNG}})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	c.Request.Header.Set("Content-Type", contentType)

	adaptor := &Adaptor{}
	plan, handled, err := adaptor.prepareImageProtocol(c, apiduoImageTestInfo())
	if !handled || err != nil {
		t.Fatalf("a file upload must be accepted, handled=%v err=%v", handled, err)
	}
	if plan.UpstreamPath != "/v1/images/edits" {
		t.Fatalf("image edit must target the derived edits endpoint, got %q", plan.UpstreamPath)
	}
	if !strings.HasPrefix(plan.ContentType, "multipart/form-data") {
		t.Fatalf("upstream must receive multipart, got %q", plan.ContentType)
	}
	if !bytes.Contains(plan.Body, []byte(`name="image"; filename="image_1.png"`)) {
		t.Fatalf("uploaded file must be forwarded as a file part: %s", plan.Body)
	}
}

func apiduoImageTestInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesEdits,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ImageProtocol: &dto.ImageProtocolConfig{
					Enabled:     true,
					Upstream:    dto.ImageUpstreamOpenAI,
					PrimaryPath: "/v1/images/generations",
				},
			},
		},
	}
}

func TestGuardFileStreamEdits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adaptorInfo := func(protocol *dto.ImageProtocolConfig) *relaycommon.RelayInfo {
		info := apiduoImageTestInfo()
		info.ChannelOtherSettings.ImageProtocol = protocol
		return info
	}
	newContext := func(body []byte, contentType string) *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", contentType)
		return c
	}
	enabled := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamOpenAI}
	errorStatus := func(t *testing.T, err error) int {
		t.Helper()
		var apiErr *types.NewAPIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("guard must classify the rejection as an API error, got %T: %v", err, err)
		}
		return apiErr.StatusCode
	}

	t.Run("JSON body is 400", func(t *testing.T) {
		c := newContext([]byte(`{"prompt":"p","image":"https://x/a.png"}`), "application/json")
		err := guardFileStreamEdits(c, adaptorInfo(enabled))
		if status := errorStatus(t, err); status != http.StatusBadRequest {
			t.Fatalf("JSON body must be 400, got %d", status)
		}
	})

	t.Run("text URL in form is 400", func(t *testing.T) {
		body, contentType := apiduoTestMultipartBody(t,
			[][2]string{{"prompt", "p"}, {"image", "https://x/a.png"}}, nil)
		err := guardFileStreamEdits(newContext(body.Bytes(), contentType), adaptorInfo(enabled))
		if status := errorStatus(t, err); status != http.StatusBadRequest {
			t.Fatalf("text reference must be 400, got %d", status)
		} else if !strings.Contains(err.Error(), "file part") {
			t.Fatalf("message should name the file-part rule, got %v", err)
		}
	})

	t.Run("file upload passes", func(t *testing.T) {
		body, contentType := apiduoTestMultipartBody(t, [][2]string{{"prompt", "p"}},
			[]apiduoTestPart{{field: "image", filename: "a.png", contentType: "image/png", data: apiduoTestPNG}})
		if err := guardFileStreamEdits(newContext(body.Bytes(), contentType), adaptorInfo(enabled)); err != nil {
			t.Fatalf("file upload must pass, got %v", err)
		}
	})

	t.Run("channel without image protocol is still file-only", func(t *testing.T) {
		// 未启用 image_protocol 的 advancedcustom 渠道同样只收文件流：
		// 否则 multipart 文本会变成可重试的 500，urlencoded 文本会把 URL 原样转发给上游。
		body, contentType := apiduoTestMultipartBody(t,
			[][2]string{{"image", "https://x/a.png"}}, nil)
		err := guardFileStreamEdits(newContext(body.Bytes(), contentType), adaptorInfo(nil))
		if status := errorStatus(t, err); status != http.StatusBadRequest {
			t.Fatalf("text reference must be 400 without image_protocol too, got %d", status)
		}
	})
}

// image[0] / image[] 都是数组写法：文件不能被静默丢掉。
func TestFoldEditsAcceptsIndexedImageField(t *testing.T) {
	form := apiduoTestForm(t, [][2]string{{"prompt", "p"}}, []apiduoTestPart{
		{field: "image[0]", filename: "a.png", contentType: "image/png", data: apiduoTestPNG},
	})

	body := apiduoTestDecode(t, form)

	images, ok := body["image"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("image[0] upload must survive folding as a 1-element array, got %#v", body["image"])
	}
	if ref, _ := images[0].(string); !strings.HasPrefix(ref, "data:image/png;base64,") {
		t.Fatalf("image[0] must be encoded as a data URI, got %#v", images[0])
	}
}

func TestFoldEditsRejectsIndexedTextImageField(t *testing.T) {
	form := apiduoTestForm(t,
		[][2]string{{"prompt", "p"}, {"image[0]", "https://cdn.example.com/a.png"}}, nil)
	if _, err := foldEditsMultipart(form); err == nil {
		t.Fatal("image[0] text reference must also be rejected")
	}
}
