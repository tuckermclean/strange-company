package vikunja

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// MaxAttachmentBytes caps what this client will upload.
//
// Well under the 20 MB a default Vikunja accepts, because the point of the
// cap is not the server's limit -- it is that a runaway harness can emit far
// more than anyone will read, and an upload rejected for size is a silent hole
// in the audit trail. Truncating with a visible marker is worse than the whole
// file and much better than nothing.
const MaxAttachmentBytes = 2 << 20 // 2 MiB

// truncationNotice is appended to any file this client shortens, so a reader
// is never left believing they have the whole thing.
const truncationNotice = "\n\n--- truncated by the control plane at %d bytes; the artifact record holds the true size ---\n"

// ErrAttachmentsDisabled reports that this Vikunja has task attachments turned
// off.
//
// Like comments, the routes are registered only when the feature is enabled
// (pkg/routes/routes.go), so on an install without it the endpoint is simply
// absent. A deployment choice, not a failure.
var ErrAttachmentsDisabled = errors.New("vikunja: task attachments are disabled on this instance")

// Attachment is one file on a task.
type Attachment struct {
	ID   int64 `json:"id"`
	File struct {
		Name string `json:"name"`
		Mime string `json:"mime"`
		Size uint64 `json:"size"`
	} `json:"file"`
}

// ListAttachments returns the files already on a task.
//
// This is the whole of the idempotence story: artifacts are immutable, so a
// name that is already here is already done. Without it the reconciler
// re-uploads every artifact on every tick and grows the operator's storage
// without bound.
func (c *Client) ListAttachments(ctx context.Context, taskID int64) ([]*Attachment, error) {
	var out []*Attachment
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/tasks/%d/attachments", taskID), nil, &out)

	var reqErr *RequestError
	if errors.As(err, &reqErr) && reqErr.Status == http.StatusNotFound {
		return nil, ErrAttachmentsDisabled
	}
	return out, err
}

// UploadAttachment puts one file on a task.
//
// VERIFIED against Vikunja v0.24.6 (pkg/routes/api/v1/task_attachment.go): the
// route is PUT on the collection, the body is multipart/form-data, and the
// field name is "files" -- plural, because the handler iterates
// form.File["files"] and accepts several per request.
func (c *Client) UploadAttachment(ctx context.Context, taskID int64, name string, content []byte) error {
	if len(content) > MaxAttachmentBytes {
		content = append(
			append([]byte{}, content[:MaxAttachmentBytes]...),
			[]byte(fmt.Sprintf(truncationNotice, MaxAttachmentBytes))...,
		)
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("files", name)
	if err != nil {
		return fmt.Errorf("vikunja: building the upload for %q: %w", name, err)
	}
	if _, err := part.Write(content); err != nil {
		return fmt.Errorf("vikunja: writing the upload for %q: %w", name, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("vikunja: closing the upload for %q: %w", name, err)
	}

	return c.upload(ctx, fmt.Sprintf("/api/v1/tasks/%d/attachments", taskID), w.FormDataContentType(), body.Bytes(), name)
}

// uploadResult is what the upload endpoint returns.
//
// The shape is the point. Vikunja answers 200 with a PER-FILE errors array
// rather than failing the request, so a caller that checks only the status
// code records a file it does not have. This is structurally the same trap as
// the Hermes gateway reporting a backend outage as a successful completion,
// and it is handled here rather than rediscovered later.
type uploadResult struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Success []*Attachment `json:"success"`
}

func (c *Client) upload(ctx context.Context, path, contentType string, body []byte, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vikunja: build upload request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vikunja: PUT %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))

	if resp.StatusCode == http.StatusNotFound {
		return ErrAttachmentsDisabled
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &RequestError{Method: http.MethodPut, Path: path, Status: resp.StatusCode, Body: string(respBody)}
	}

	var result uploadResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("vikunja: decoding the upload response for %q: %w", name, err)
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("vikunja: uploading %q: %s", name, result.Errors[0].Message)
	}
	if len(result.Success) == 0 {
		// 200, no error, no file. Believing this would record an artifact
		// as delivered when nothing was stored.
		return fmt.Errorf("vikunja: uploading %q: the server reported neither success nor an error", name)
	}
	return nil
}
