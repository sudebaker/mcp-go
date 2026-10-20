package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression test for t_b55e3c26: a Python tool answered a tools/call with an
// error whose `details` were an array. The whole response failed to
// unmarshal and the client saw
// "failed to parse subprocess response: json: cannot unmarshal array into Go
// struct field SubprocessError.error.details of type string" instead of the
// tool's real code + message. The exact wire payload from that incident must
// decode with code and message intact.
func TestSubprocessResponse_ParsesErrorWithArrayDetails(t *testing.T) {
	stdout := `{"success":false,"request_id":"req-1","error":{"code":"NO_FILES","message":"resource not found","details":[{"file":"res://XXXX","error":"resource not found"}]}}`

	var resp SubprocessResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.RequestID != "req-1" {
		t.Errorf("expected request_id 'req-1', got %q", resp.RequestID)
	}
	if resp.Success {
		t.Error("expected success to be false")
	}
	if resp.Error == nil {
		t.Fatal("expected error to be set")
	}
	if resp.Error.Code != "NO_FILES" {
		t.Errorf("expected code 'NO_FILES', got %q", resp.Error.Code)
	}
	if resp.Error.Message != "resource not found" {
		t.Errorf("expected the tool's message, got %q", resp.Error.Message)
	}
	if strings.Contains(resp.Error.Message, "failed to parse") {
		t.Errorf("tool message replaced by parse error: %q", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Details, "res://XXXX") {
		t.Errorf("expected normalized details to keep file info, got %q", resp.Error.Details)
	}
}

func TestSubprocessError_DetailsShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"string", `{"code":"C","message":"m","details":"plain"}`, "plain"},
		{"array", `{"code":"C","message":"m","details":[1,{"file":"f"}]}`, `[1,{"file":"f"}]`},
		{"object", `{"code":"C","message":"m","details":{"field":"x"}}`, `{"field":"x"}`},
		{"null", `{"code":"C","message":"m","details":null}`, ""},
		{"missing", `{"code":"C","message":"m"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e SubprocessError
			if err := json.Unmarshal([]byte(tc.raw), &e); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if e.Details != tc.want {
				t.Errorf("Details = %q, want %q", e.Details, tc.want)
			}
		})
	}
}

// The documented contract keeps details a string on the way out: marshaling a
// SubprocessError emits details as a JSON string, and round-tripping a
// normalized error preserves code, message and details.
func TestSubprocessError_MarshalDetailsRemainsString(t *testing.T) {
	e := SubprocessError{Code: "NO_FILES", Message: "no files", Details: "gone.yaml: not found"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"details":"gone.yaml: not found"`) {
		t.Errorf("expected details to marshal as a JSON string, got %s", b)
	}
	var back SubprocessError
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Details != e.Details || back.Code != e.Code || back.Message != e.Message {
		t.Errorf("round trip changed the error: %s", b)
	}
}
