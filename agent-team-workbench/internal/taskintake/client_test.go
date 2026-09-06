package taskintake

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func completeMessages() []Message {
	return []Message{{Role: "user", Content: "我想把登录流程做得更稳定"}}
}

func completeModel(baseURL, api string) Model {
	return Model{Ref: "test-model", API: api, Model: "test-model", BaseURL: baseURL, APIKey: "secret-key"}
}

func TestAnalyzeOpenAICompletionsUsesTextOnlyRequestAndParsesDraft(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-key" {
			t.Fatalf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages, ok := body["messages"].([]any)
		format, formatOK := body["response_format"].(map[string]any)
		jsonSchema, schemaOK := format["json_schema"].(map[string]any)
		schema, schemaBodyOK := jsonSchema["schema"].(map[string]any)
		if body["model"] != "test-model" || body["stream"] != false || !ok || len(messages) != 2 ||
			!formatOK || format["type"] != "json_schema" || !schemaOK || jsonSchema["name"] != "task_intake" ||
			jsonSchema["strict"] != true || !schemaBodyOK || schema["additionalProperties"] != false {
			t.Fatalf("request = %+v", body)
		}
		firstMessage := messages[0].(map[string]any)
		secondMessage := messages[1].(map[string]any)
		if firstMessage["role"] != "system" || secondMessage["content"] != completeMessages()[0].Content {
			t.Fatalf("messages = %+v", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		content := `{"reply":"我还需要确认范围。","questions":[],"draft":{"title":"稳定登录","description":"补齐登录失败处理","acceptance_criteria":["失败时显示可理解原因"]}}`
		response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
		_, _ = w.Write(response)
	}))
	defer server.Close()

	result, err := NewClient(server.Client()).Analyze(context.Background(), completeModel(server.URL, "openai-completions"), completeMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reply != "我还需要确认范围。" || result.Draft == nil || result.Draft.Title != "稳定登录" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAnalyzeOpenAIResponsesUsesResponsesEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		input, ok := body["input"].([]any)
		textSpec, textOK := body["text"].(map[string]any)
		format, formatOK := textSpec["format"].(map[string]any)
		if body["model"] != "test-model" || body["stream"] != false || !ok || len(input) != 2 ||
			!textOK || !formatOK || format["type"] != "json_schema" || format["name"] != "task_intake" || format["strict"] != true {
			t.Fatalf("request = %+v", body)
		}
		firstMessage := input[0].(map[string]any)
		if firstMessage["role"] != "system" {
			t.Fatalf("input = %+v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		content := `{"reply":"已整理。","questions":[],"draft":null}`
		response, _ := json.Marshal(map[string]any{"output_text": string(content)})
		_, _ = w.Write(response)
	}))
	defer server.Close()

	result, err := NewClient(server.Client()).Analyze(context.Background(), completeModel(server.URL, "openai-responses"), completeMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reply != "已整理。" || result.Draft != nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseResultWithIncompleteDraftWithholdsPublishableDraft(t *testing.T) {
	result, err := parseResult(`{"reply":"还需要确认验收方式。","questions":[],"draft":{"title":"任务","description":"范围","acceptance_criteria":[]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Draft != nil {
		t.Fatalf("incomplete draft must be withheld: %+v", result.Draft)
	}
}

func TestParseResultQuestionsWithholdDraftAndValidateShape(t *testing.T) {
	result, err := parseResult(`{"reply":"请先选择范围。","questions":[{"id":"scope","title":"范围","options":["前端","后端","暂不确定"],"multiple":false}],"draft":{"title":"任务","description":"描述","acceptance_criteria":["完成"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Questions) != 1 || result.Questions[0].ID != "scope" || result.Questions[0].Multiple || result.Draft != nil {
		t.Fatalf("questions=%+v draft=%+v", result.Questions, result.Draft)
	}

	invalid := []string{
		`{"reply":"q","questions":[{"id":"","title":"范围","options":["前端","后端"],"multiple":false}],"draft":null}`,
		`{"reply":"q","questions":[{"id":"scope","title":"范围","options":["前端"],"multiple":false}],"draft":null}`,
		`{"reply":"q","questions":[{"id":"scope","title":"范围","options":["前端","前端"],"multiple":false}],"draft":null}`,
		`{"reply":"q","questions":[{"id":"scope","title":"范围","options":["前端","后端"],"multiple":"no"}],"draft":null}`,
	}
	for _, content := range invalid {
		if _, err := parseResult(content); err == nil {
			t.Fatalf("invalid question shape should be rejected: %s", content)
		}
	}
	longReply := strings.Repeat("x", MaxQuestionReplyRunes+1)
	if _, err := parseResult(`{"reply":"` + longReply + `","questions":[{"id":"scope","title":"范围","options":["前端","后端"],"multiple":false}],"draft":null}`); err == nil {
		t.Fatal("question guidance reply must stay short enough for next-turn serialization")
	}
}

func TestValidateQuestionsBoundsAndUniqueIDs(t *testing.T) {
	if err := ValidateQuestions([]Question{{ID: "same", Title: "a", Options: []string{"1", "2"}}, {ID: "same", Title: "b", Options: []string{"1", "2"}}}); err == nil {
		t.Fatal("duplicate question ids must be rejected")
	}
	if err := ValidateQuestions([]Question{{ID: "q", Title: "a", Options: []string{"1", "2", "3", "4", "5", "6"}}}); err == nil {
		t.Fatal("too many options must be rejected")
	}
	if err := ValidateQuestions([]Question{{ID: "q", Title: "a", Options: []string{"1"}}}); err == nil {
		t.Fatal("too few options must be rejected")
	}
}

func TestParseResultProjectsUnknownRootPresentationFields(t *testing.T) {
	result, err := parseResult(`{"reply":"ok","questions":[],"reminder":"确认后发布","extra":{"ignored":true},"draft":null}`)
	if err != nil || result.Reply != "ok" || result.Draft != nil {
		t.Fatalf("known fields should be projected: result=%+v err=%v", result, err)
	}
	if _, err := parseResult(`{"reply":"ok","questions":[],"draft":{"title":"t","description":"d","acceptance_criteria":["a"],"publish":true}}`); err == nil {
		t.Fatal("unknown draft fields must remain strict")
	}
}

func TestParseResultRejectsNonJSONAndMultipleValues(t *testing.T) {
	for _, content := range []string{
		"plain text",
		`{"reply":"ok","questions":[],"draft":null} {"reply":"second"}`,
	} {
		if _, err := parseResult(content); err == nil {
			t.Fatalf("content should be rejected: %q", content)
		}
	}
}

func TestParseResultAcceptsProviderMarkdownWrapperButKeepsStrictObject(t *testing.T) {
	valid := `{"reply":"已整理。","questions":[],"draft":null}`
	for _, content := range []string{
		"```json\n" + valid + "\n```",
		"```JSON\n" + valid + "\n```",
		"```json " + valid + " ```",
		"```\n" + valid + "\n```",
	} {
		result, err := parseResult(content)
		if err != nil || result.Reply != "已整理。" {
			t.Fatalf("wrapped response should parse: content=%q result=%+v err=%v", content, result, err)
		}
	}
	for _, content := range []string{
		"下面是结果：\n" + valid,
		"```text\n" + valid + "\n```",
		"```json\n" + valid,
	} {
		if _, err := parseResult(content); err == nil {
			t.Fatalf("ambiguous wrapper should be rejected: %q", content)
		}
	}
}

func TestValidateInputRejectsToolAndOversizedMessages(t *testing.T) {
	if err := ValidateInput([]Message{{Role: "system", Content: "no"}}, nil); err == nil {
		t.Fatal("system message must be rejected")
	}
	if err := ValidateInput([]Message{{Role: "assistant", Content: "not latest user"}}, nil); err == nil {
		t.Fatal("latest assistant message must be rejected")
	}
	if err := ValidateInput([]Message{{Role: "user", Content: strings.Repeat("x", MaxMessageRunes+1)}}, nil); err == nil {
		t.Fatal("oversized message must be rejected")
	}
}

func TestAnalyzeProviderFailureDoesNotExposeResponseBody(t *testing.T) {
	secret := "provider-secret-that-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, secret, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := NewClient(server.Client()).Analyze(context.Background(), completeModel(server.URL, "openai-completions"), completeMessages(), nil)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("provider body leaked or error missing: %v", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeRequestFailed || typed.Retryable {
		t.Fatalf("unexpected error = %#v", err)
	}
}

func TestAnalyzeTimeoutIsClassifiedRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.SetTimeout(10 * time.Millisecond)
	_, err := client.Analyze(context.Background(), completeModel(server.URL, "openai-completions"), completeMessages(), nil)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeTimeout || !typed.Retryable {
		t.Fatalf("unexpected timeout error = %#v", err)
	}
}

func TestAnalyzeBodyReadTimeoutIsClassifiedRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"`))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.SetTimeout(10 * time.Millisecond)
	_, err := client.Analyze(context.Background(), completeModel(server.URL, "openai-responses"), completeMessages(), nil)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeTimeout || !typed.Retryable {
		t.Fatalf("unexpected body-read timeout error = %#v", err)
	}
}
