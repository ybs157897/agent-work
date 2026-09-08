package lspproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadOnlyMessageAllowsNavigationAndDocumentSync(t *testing.T) {
	for _, method := range []string{
		"initialize",
		"initialized",
		"textDocument/didOpen",
		"textDocument/didChange",
		"textDocument/didClose",
		"textDocument/didSave",
		"textDocument/definition",
		"textDocument/references",
		"textDocument/hover",
		"textDocument/completion",
		"textDocument/documentSymbol",
		"workspace/symbol",
		"$/cancelRequest",
	} {
		allowed, forbidden := readOnlyMessage([]byte(`{"jsonrpc":"2.0","method":"` + method + `"}`))
		if !allowed || forbidden != nil {
			t.Fatalf("method %s was rejected: allowed=%v forbidden=%q", method, allowed, forbidden)
		}
	}
}

func TestReadOnlyMessageForbidsMutationAndUnknownRequests(t *testing.T) {
	for _, method := range []string{"textDocument/rename", "textDocument/codeAction", "workspace/executeCommand", "workspace/didChangeConfiguration", "unknown/method"} {
		allowed, forbidden := readOnlyMessage([]byte(`{"jsonrpc":"2.0","id":7,"method":"` + method + `"}`))
		if allowed || len(forbidden) == 0 {
			t.Fatalf("method %s was allowed: allowed=%v forbidden=%q", method, allowed, forbidden)
		}
		var response struct {
			ID    int `json:"id"`
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(forbidden, &response); err != nil {
			t.Fatal(err)
		}
		if response.ID != 7 || response.Error.Code != -32001 || !strings.Contains(string(forbidden), "read-only") {
			t.Fatalf("forbidden response=%s", forbidden)
		}
	}

	allowed, forbidden := readOnlyMessage([]byte(`{"jsonrpc":"2.0","method":"textDocument/rename"}`))
	if allowed || forbidden != nil {
		t.Fatalf("notification should be dropped without a response: allowed=%v forbidden=%q", allowed, forbidden)
	}
}

func TestReadOnlyMessageDoesNotFailOpenForMalformedOrBatchFrames(t *testing.T) {
	for _, raw := range []string{
		"not-json",
		"[]",
		"[{}]",
		"null",
		`{"jsonrpc":"2.0","method":42}`,
		`{"jsonrpc":"2.0","id":true,"result":null}`,
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-1,"message":"both"}}`,
	} {
		allowed, forbidden := readOnlyMessage([]byte(raw))
		if allowed || forbidden != nil {
			t.Fatalf("invalid frame passed read-only boundary: raw=%s allowed=%v forbidden=%q", raw, allowed, forbidden)
		}
	}

	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"result":null}`,
		`{"jsonrpc":"2.0","id":"request","error":{"code":-32600,"message":"bad request"}}`,
	} {
		allowed, forbidden := readOnlyMessage([]byte(raw))
		if !allowed || forbidden != nil {
			t.Fatalf("valid response was rejected: raw=%s allowed=%v forbidden=%q", raw, allowed, forbidden)
		}
	}
}
