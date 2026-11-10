package acp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func MCPBridgeHandler(sm *SessionManager, wiretap WiretapCallback) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet {
			// Per MCP Streamable HTTP spec, return 405 Method Not Allowed when
			// a standalone GET SSE stream is not offered at this endpoint.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		bridgeID := strings.TrimPrefix(r.URL.Path, "/mcp/sessions/")
		bridgeID = strings.Trim(bridgeID, "/")
		sess, ok := sm.GetSessionByBridgeID(bridgeID)
		if !ok {
			http.Error(w, "session bridge not found", http.StatusNotFound)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 16*1024*1024)
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if wiretap != nil {
			wiretap("mcp:"+bridgeID, "IN", string(rawBody))
		}

		var req mcpRPCRequest
		if err := json.Unmarshal(rawBody, &req); err != nil {
			http.Error(w, "invalid json-rpc", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "initialize":
			protoVer := "2024-11-05"
			var initParams struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(req.Params, &initParams) == nil && strings.TrimSpace(initParams.ProtocolVersion) != "" {
				protoVer = strings.TrimSpace(initParams.ProtocolVersion)
			}
			writeMCPResponse(w, wiretap, bridgeID, req.ID, map[string]any{
				"protocolVersion": protoVer,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "antigravity-client-tools-bridge",
					"version": "1.6.0",
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			tools := sess.GetTools()
			if tools == nil {
				tools = []ToolDefinition{}
			}
			writeMCPResponse(w, wiretap, bridgeID, req.ID, map[string]any{
				"tools": tools,
			})
		case "tools/call":
			var p mcpToolCallParams
			_ = json.Unmarshal(req.Params, &p)
			toolName := strings.TrimSpace(p.Name)
			toolName = strings.TrimPrefix(toolName, "mcp__client_tools__")
			toolName = strings.TrimPrefix(toolName, "client_tools:")
			argsJSON := string(p.Arguments)
			if strings.TrimSpace(argsJSON) == "" {
				argsJSON = "{}"
			}
			parked, err := sm.ParkToolCall(bridgeID, toolName, argsJSON)
			if err != nil {
				writeMCPError(w, req.ID, -32603, err.Error())
				return
			}

			select {
			case res := <-parked.ResultCh:
				writeMCPResponse(w, wiretap, bridgeID, req.ID, map[string]any{
					"content": []map[string]any{
						{
							"type": "text",
							"text": res.Content,
						},
					},
					"isError": res.IsError,
				})
			case <-r.Context().Done():
				sm.CancelParkedToolCall(parked.CallID)
				writeMCPError(w, req.ID, -32001, "request cancelled while waiting for client tool_result")
			}
		default:
			if req.ID == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			writeMCPResponse(w, wiretap, bridgeID, req.ID, map[string]any{})
		}
	})
}

func writeMCPResponse(w http.ResponseWriter, wiretap WiretapCallback, bridgeID string, id any, result any) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	raw, _ := json.Marshal(payload)
	if wiretap != nil {
		wiretap("mcp:"+bridgeID, "OUT", string(raw))
	}
	_, _ = w.Write(raw)
}

func writeMCPError(w http.ResponseWriter, id any, code int, message string) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	raw, _ := json.Marshal(payload)
	_, _ = w.Write(raw)
}
