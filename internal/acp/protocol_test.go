package acp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"antigravity-gateway/internal/acp"
)

func TestIsAuthRequiredError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rpc auth message", &acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "Authentication required"}, true},
		{"rpc login message", &acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "Please sign in to continue"}, true},
		{"rpc credential message", &acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "Invalid credentials"}, true},
		{"wrapped rpc auth", fmt.Errorf("session/new: %w", &acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "User is unauthenticated"}), true},
		{"generic -32000", &acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "Internal server error"}, false},
		{"generic -32000 quota", &acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "model quota exhausted"}, false},
		{"proxy auth rpc", &acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "Proxy Authentication Required"}, false},
		{"proxy auth plain", errors.New("HTTP 407 Proxy Authentication Required"), false},
		{"plain authentication required", errors.New("upstream: authentication required"), true},
		{"plain not logged in", errors.New("You are not logged in"), true},
		{"plain please log in", errors.New("Please log in again"), true},
		{"plain unauthenticated", errors.New("UNAUTHENTICATED: token expired"), true},
		{"other code with auth phrase", &acp.RPCError{Code: -32603, Message: "Authentication required"}, true},
		{"unrelated", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		if got := acp.IsAuthRequiredError(tc.err); got != tc.want {
			t.Errorf("%s: IsAuthRequiredError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestIsResourceNotFoundError(t *testing.T) {
	if !acp.IsResourceNotFoundError(&acp.RPCError{Code: acp.ErrCodeResourceNotFound, Message: "gone"}) {
		t.Fatalf("expected -32002 to be resource-not-found")
	}
	if !acp.IsResourceNotFoundError(errors.New("Resource not found: session expired")) {
		t.Fatalf("expected message match to be resource-not-found")
	}
	if acp.IsResourceNotFoundError(&acp.RPCError{Code: acp.ErrCodeAuthRequired, Message: "boom"}) || acp.IsResourceNotFoundError(nil) {
		t.Fatalf("expected unrelated errors not to be resource-not-found")
	}
}

func TestContentBlockUnmarshalShapes(t *testing.T) {
	cases := map[string]acp.ContentBlock{
		`"bare string"`:                             {Type: "text", Text: "bare string"},
		`{"type":"text","text":"obj"}`:              {Type: "text", Text: "obj"},
		`[{"type":"text","text":"a"},{"text":"b"}]`: {Type: "text", Text: "a\nb"},
		`null`: {},
	}
	for raw, want := range cases {
		var got acp.ContentBlock
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if got != want {
			t.Fatalf("unmarshal %s: got %+v, want %+v", raw, got, want)
		}
	}

	var up acp.SessionUpdateParams
	if err := json.Unmarshal([]byte(`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":"hello"}}`), &up); err != nil {
		t.Fatalf("unmarshal session update: %v", err)
	}
	if up.Update.Content.Text != "hello" {
		t.Fatalf("expected bare-string content to be preserved, got %+v", up.Update.Content)
	}
}

func TestExtractModelsFromSessionResultOrderIndependent(t *testing.T) {
	variants := []acp.ModelInfo{
		{ModelID: "foo-high", Name: "Foo (High)"},
		{ModelID: "foo", Name: "Foo"},
		{ModelID: "bar-low", Name: "Bar (Low)"},
		{ModelID: "bar-High", Name: "Bar (High)"},
	}
	reversed := make([]acp.ModelInfo, len(variants))
	for i, m := range variants {
		reversed[len(variants)-1-i] = m
	}

	extract := func(models []acp.ModelInfo) map[string]acp.ModelInfo {
		out := acp.ExtractModelsFromSessionResult(&acp.NewSessionResult{
			Models: &acp.SessionModelsState{AvailableModels: models},
		})
		byID := make(map[string]acp.ModelInfo, len(out))
		for _, m := range out {
			byID[m.ModelID] = m
		}
		return byID
	}
	a, b := extract(variants), extract(reversed)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("expected identical results regardless of order:\n%+v\n%+v", a, b)
	}
	if len(a) != 2 {
		t.Fatalf("expected exactly foo and bar, got %+v", a)
	}
	if foo := a["foo"]; foo.Name != "Foo" || !reflect.DeepEqual(foo.ThinkingLevels, []string{"high"}) {
		t.Fatalf("unexpected foo entry: %+v", foo)
	}
	if bar := a["bar"]; !reflect.DeepEqual(bar.ThinkingLevels, []string{"low", "high"}) {
		t.Fatalf("expected case-insensitive canonical ordering for bar, got %+v", bar)
	}
}

func TestThinkingLevelCanonicalOrderIncludesExtraHigh(t *testing.T) {
	out := acp.ExtractModelsFromSessionResult(&acp.NewSessionResult{
		ConfigOptions: []acp.ConfigOption{
			{ID: "model", Options: []acp.ConfigOptionValue{{Value: "m1"}}},
			{ID: "thinking_level", CurrentValue: "High", Options: []acp.ConfigOptionValue{
				{Value: "Max"}, {Value: "extra_high"}, {Value: "High"}, {Value: "custom"}, {Value: "LOW"},
			}},
		},
	})
	if len(out) != 1 {
		t.Fatalf("expected one model, got %+v", out)
	}
	want := []string{"LOW", "High", "extra_high", "Max", "custom"}
	if !reflect.DeepEqual(out[0].ThinkingLevels, want) {
		t.Fatalf("expected %v, got %v", want, out[0].ThinkingLevels)
	}
}
