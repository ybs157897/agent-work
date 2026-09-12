package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledgelib"
)

// maxRequirementBytes bounds how much external text one task may carry. A
// requirement is an instruction, not a corpus: anything larger is truncated
// with an explicit note rather than silently dropped or buffered forever.
const maxRequirementBytes = 256 * 1024

// requirementTextKeys are the payload fields an external system may use to
// deliver the requirement body, in the order they are preferred.
var requirementTextKeys = []string{
	"text", "requirement_text", "content", "body", "statement", "description", "summary",
}

// requirementMetaKeys are scalar payload fields worth showing the agent
// verbatim. They are declared metadata, never evidence of implementation.
var requirementMetaKeys = []string{
	"acceptance", "acceptance_criteria", "owner", "priority", "status", "tags",
	"source_url", "effective_at", "reason", "note", "notes",
}

// requirementFromEvent materializes the declared requirement text that opened
// a task. Both the content_ref and the payload body are handed to the agent:
// without them the librarian can only guess what was asked for, which is the
// failure this function exists to prevent.
func requirementFromEvent(event *domain.KnowledgeLibraryEvent, staging string) *knowledgelib.Requirement {
	if event == nil {
		return nil
	}
	payload := decodeJSONObject(event.PayloadJSON)
	req := &knowledgelib.Requirement{
		RequirementID: firstString(payload, "requirement_id", "req_id", "requirement", "id"),
		Title:         firstString(payload, "title", "name", "summary"),
		Version:       firstString(payload, "version", "requirement_version", "revision"),
		Source:        strings.TrimSpace(event.Source),
		ContentRef:    strings.TrimSpace(event.ContentRef),
		Extra:         map[string]string{},
	}
	if req.RequirementID == "" {
		req.RequirementID = strings.TrimSpace(subjectString(event.SubjectJSON, "requirement_id"))
	}
	for _, key := range requirementMetaKeys {
		if v := firstString(payload, key); v != "" {
			req.Extra[key] = v
		}
	}
	if len(req.Extra) == 0 {
		req.Extra = nil
	}

	var text string
	if v := firstString(payload, requirementTextKeys...); v != "" {
		text = v
	}
	// A referenced document is the authority when both are present, because a
	// payload body is often only a summary of it.
	if req.ContentRef != "" {
		raw, err := readContentRef(req.ContentRef)
		switch {
		case err != nil:
			req.Unresolved = err.Error()
		case strings.TrimSpace(raw) == "":
			req.Unresolved = fmt.Sprintf("引用 %s 的内容为空", req.ContentRef)
		default:
			text = raw
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		if req.Unresolved == "" {
			req.Unresolved = "事件既没有可直接读取的 content_ref，也没有正文 payload"
		}
		return req
	}
	if len(text) > maxRequirementBytes {
		text = text[:maxRequirementBytes] + "\n\n（原文超过 256 KiB，已截断）"
	}
	sum := sha256.Sum256([]byte(text))
	req.Digest = hex.EncodeToString(sum[:])
	req.Text = text
	if strings.TrimSpace(staging) != "" {
		req.Path = filepath.Join(staging, knowledgelib.RequirementFileName)
	}
	return req
}

// readContentRef reads a referenced requirement document. Only local files are
// supported: a remote scheme is reported as unresolved instead of being
// fetched silently, so the agent is never handed text from an unrecorded
// source.
func readContentRef(ref string) (string, error) {
	path := strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(path, "file://"):
		path = strings.TrimPrefix(path, "file://")
	case strings.Contains(path, "://"):
		scheme := path[:strings.Index(path, "://")]
		return "", fmt.Errorf("content_ref 使用 %s 协议，本版本只能读取本地文件", scheme)
	}
	path = filepath.FromSlash(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("content_ref %s 不可读：%v", ref, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("content_ref %s 是目录，不是需求文档", ref)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("content_ref %s 不可读：%v", ref, err)
	}
	return string(raw), nil
}

func decodeJSONObject(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func subjectString(subjectJSON, key string) string {
	if v, ok := decodeJSONObject(subjectJSON)[key].(string); ok {
		return v
	}
	return ""
}

// firstString returns the first non-empty scalar for the given keys, rendered
// the way an operator would expect to read it.
func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := object[key]
		if !ok || v == nil {
			continue
		}
		switch typed := v.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case bool:
			return fmt.Sprintf("%t", typed)
		case float64:
			return strings.TrimSpace(strings.TrimSuffix(fmt.Sprintf("%f", typed), ".000000"))
		case []any:
			parts := make([]string, 0, len(typed))
			for _, item := range typed {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "；")
			}
		}
	}
	return ""
}
