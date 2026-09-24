package body

import (
	"encoding/json"
	"fmt"
	"runtime"
)

// RuntimeInfo is the structured, benign payload returned by the
// runtime.info / read operation of the Local Body. It describes the
// immediate Core execution environment only.
//
// Deliberately NOT included: environment variables, credentials, network
// configuration, filesystem contents, host identifiers, or any other
// ambient/sensitive data. Those belong to later capabilities with their
// own authority evaluation, never to a generic runtime read.
type RuntimeInfo struct {
	// OS is the operating system the Core runs on (runtime.GOOS).
	OS string `json:"os"`

	// Architecture is the machine architecture (runtime.GOARCH).
	Architecture string `json:"architecture"`

	// GoVersion is the Go runtime/version string (runtime.Version()).
	GoVersion string `json:"go_version"`
}

// collectRuntimeInfo gathers the benign runtime facts for runtime.info/read.
func collectRuntimeInfo() RuntimeInfo {
	return RuntimeInfo{
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		GoVersion:    runtime.Version(),
	}
}

// executeRuntimeInfoRead implements the runtime.info / read capability of
// the Local Body capability surface. It is reached only through the generic
// capability invocation path (Guard.Execute → Body.Execute) — never through
// a special Core helper, so the same authority boundary applies that every
// future capability will use.
func executeRuntimeInfoRead() (*ExecutionResult, error) {
	info := collectRuntimeInfo()
	out, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal runtime info: %w", err)
	}
	return &ExecutionResult{
		Status: StatusSuccess,
		Output: string(out),
	}, nil
}

// executeRuntimeInfoList implements the runtime.info / list capability of
// the Local Body capability surface.
//
// This is an explicitly deterministic M6 conformance fixture: it returns a
// hard-coded response and does NOT reflect the actual capability registry.
// A future capability (e.g. capabilities.registry / list) will provide
// truthful capability discovery when the architecture requires it.
func executeRuntimeInfoList() (*ExecutionResult, error) {
	result := map[string]any{
		"fixture": true,
		"message": "M6 conformance — deterministic response",
		"capabilities": []map[string]any{
			{"id": "runtime.info", "operations": []string{"read", "list"}},
		},
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal capability list: %w", err)
	}
	return &ExecutionResult{
		Status: StatusSuccess,
		Output: string(out),
	}, nil
}
