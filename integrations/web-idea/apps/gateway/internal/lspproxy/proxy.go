package lspproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/ybs/web-idea/apps/gateway/internal/jdtls"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // CORS/auth handled upstream
}

// Handle bridges a browser WebSocket (JSON-RPC text frames) to jdtls stdio.
func Handle(w http.ResponseWriter, r *http.Request, sess *jdtls.Session, workspaceID, root string, readOnly bool) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("lspproxy upgrade: %v", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(MaxFrameBytes)

	var writeMu sync.Mutex
	errCh := make(chan error, 2)
	handlerDone := make(chan struct{})
	defer close(handlerDone)

	// A workspace DELETE stops the session and closes Done after the process
	// exits. Closing the WebSocket here also unblocks the client reader when a
	// browser connection is still open during that shutdown.
	go func() {
		select {
		case <-sess.Done():
			_ = conn.Close()
		case <-r.Context().Done():
			_ = conn.Close()
		case <-handlerDone:
		}
	}()

	// jdtls stdout → client
	go func() {
		br := bufio.NewReader(sess.Stdout)
		for {
			body, err := ReadFrame(br)
			if err != nil {
				errCh <- err
				return
			}
			out, err := RewriteJSON(body, workspaceID, root, "toClient")
			if err != nil {
				log.Printf("lspproxy rewrite→client: %v", err)
				errCh <- err
				return
			}
			writeMu.Lock()
			err = conn.WriteMessage(websocket.TextMessage, out)
			writeMu.Unlock()
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	// client → jdtls stdin
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if len(msg) > MaxFrameBytes {
				errCh <- ErrFrameTooLarge
				return
			}
			if readOnly {
				allowed, forbidden := readOnlyMessage(msg)
				if !allowed {
					if forbidden != nil {
						writeMu.Lock()
						err = conn.WriteMessage(websocket.TextMessage, forbidden)
						writeMu.Unlock()
						if err != nil {
							errCh <- err
							return
						}
					}
					continue
				}
			}
			out, err := RewriteJSON(msg, workspaceID, root, "toServer")
			if err != nil {
				log.Printf("lspproxy rewrite→server: %v", err)
				continue
			}
			if err := WriteFrame(sess.Stdin, out); err != nil {
				errCh <- err
				return
			}
		}
	}()

	<-errCh
}

var readOnlyMethods = map[string]struct{}{
	"initialize":                       {},
	"initialized":                      {},
	"shutdown":                         {},
	"exit":                             {},
	"textDocument/didOpen":             {},
	"textDocument/didChange":           {},
	"textDocument/didClose":            {},
	"textDocument/didSave":             {},
	"textDocument/definition":          {},
	"textDocument/declaration":         {},
	"textDocument/typeDefinition":      {},
	"textDocument/implementation":      {},
	"textDocument/references":          {},
	"textDocument/hover":               {},
	"textDocument/completion":          {},
	"textDocument/signatureHelp":       {},
	"textDocument/documentSymbol":      {},
	"textDocument/semanticTokens/full": {},
	"workspace/symbol":                 {},
	"$/cancelRequest":                  {},
	"window/workDoneProgress/cancel":   {},
}

func readOnlyMessage(raw []byte) (bool, []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '[' {
		return false, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return false, nil
	}
	var version string
	if err := json.Unmarshal(object["jsonrpc"], &version); err != nil || version != "2.0" {
		return false, nil
	}
	methodRaw, hasMethod := object["method"]
	if hasMethod {
		var method string
		if err := json.Unmarshal(methodRaw, &method); err != nil || method == "" {
			return false, nil
		}
		if _, hasResult := object["result"]; hasResult {
			return false, nil
		}
		if _, hasError := object["error"]; hasError {
			return false, nil
		}
		if _, ok := readOnlyMethods[method]; ok {
			return true, nil
		}
		id, hasID := object["id"]
		if !hasID || !validRPCID(id) {
			return false, nil
		}
		return false, forbiddenResponse(id)
	}

	id, hasID := object["id"]
	result, hasResult := object["result"]
	errValue, hasError := object["error"]
	if !hasID || !validRPCID(id) || hasResult == hasError {
		return false, nil
	}
	if hasResult && len(bytes.TrimSpace(result)) == 0 {
		return false, nil
	}
	if hasError && !validRPCError(errValue) {
		return false, nil
	}
	return true, nil
}

func validRPCID(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

func validRPCError(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return false
	}
	var code int
	var message string
	return json.Unmarshal(object["code"], &code) == nil && json.Unmarshal(object["message"], &message) == nil
}

func forbiddenResponse(id json.RawMessage) []byte {
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    -32001,
			"message": "read-only workspace forbids this LSP method",
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil
	}
	return encoded
}
