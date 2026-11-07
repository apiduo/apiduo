package advancedcustom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
)

var apiduoTestPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type apiduoTestPart struct {
	field       string
	filename    string
	contentType string
	data        []byte
}

func apiduoTestForm(t *testing.T, fields [][2]string, parts []apiduoTestPart) *multipart.Form {
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

	form, err := multipart.NewReader(&buf, writer.Boundary()).ReadForm(1 << 22)
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

func TestFoldEditsKeepsTextReference(t *testing.T) {
	form := apiduoTestForm(t,
		[][2]string{{"prompt", "p"}, {"image", " https://cdn.example.com/a.png "}},
		nil,
	)

	body := apiduoTestDecode(t, form)

	if body["image"] != "https://cdn.example.com/a.png" {
		t.Fatalf("text reference should be trimmed and kept, got %#v", body["image"])
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
