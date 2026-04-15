package api

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

func TestReadBundleUpload_TooLarge(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/apps/app/deploy", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	file, err := readBundleUpload(rec, req, 8)
	if file != nil {
		file.Close()
		t.Fatal("expected no bundle file for oversized upload")
	}
	if err != errBundleTooLarge {
		t.Fatalf("expected errBundleTooLarge, got %v", err)
	}
}
