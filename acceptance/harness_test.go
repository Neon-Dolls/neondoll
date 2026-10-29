package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRunnerReportsEveryScenarioInDeclaredOrder(t *testing.T) {
	var order []string
	resource := filepath.Join(t.TempDir(), "resource")
	profile := Profile{Name: "test", Scenarios: []Scenario{
		{ID: "pass", Description: "passes", Run: func(context.Context) error { order = append(order, "pass"); return nil }},
		{ID: "fail", Description: "fails", Run: func(context.Context) error {
			order = append(order, "fail")
			if err := os.WriteFile(resource, []byte("open"), 0o600); err != nil {
				return err
			}
			defer os.Remove(resource)
			return errors.New("observable failure")
		}},
		{ID: "skip", Description: "skips", Run: func(context.Context) error { order = append(order, "skip"); return Skip("not available") }},
		{ID: "after", Description: "still runs", Run: func(context.Context) error { order = append(order, "after"); return nil }},
	}}
	runner, err := NewRunner(profile)
	if err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	report, err := runner.Run(context.Background(), "test", "revision", map[string]string{"gateway": "abc"}, &console)
	if err != nil {
		t.Fatal(err)
	}
	if report.Successful() || report.Status != StatusFail {
		t.Fatalf("report status=%s", report.Status)
	}
	if !reflect.DeepEqual(order, []string{"pass", "fail", "skip", "after"}) {
		t.Fatalf("order=%v", order)
	}
	if len(report.Scenarios) != 4 || report.Scenarios[1].Status != StatusFail || report.Scenarios[2].Status != StatusSkip || report.Scenarios[2].Failure != "not available" {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Stat(resource); !os.IsNotExist(err) {
		t.Fatalf("scenario resource was not cleaned up: %v", err)
	}
	if !bytes.Contains(console.Bytes(), []byte("observable failure")) {
		t.Fatalf("console=%s", console.String())
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := report.WriteJSON(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Profile != "test" || decoded.CoreRevision != "revision" || decoded.ExternalRevisions["gateway"] != "abc" || len(decoded.Scenarios) != 4 {
		t.Fatalf("decoded=%#v", decoded)
	}
}

func TestUnknownProfileFailsClearly(t *testing.T) {
	runner, err := NewRunner(Core5Profile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(context.Background(), "missing", "revision", nil, nil); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("error=%v", err)
	}
}

func TestCore5ProfileExecutesRequiredVerticalSlice(t *testing.T) {
	runner, err := NewRunner(Core5Profile())
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), "core5", "test-revision", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Successful() {
		t.Fatalf("report=%#v", report)
	}
	if got := []string{report.Scenarios[0].ID, report.Scenarios[1].ID, report.Scenarios[2].ID}; !reflect.DeepEqual(got, []string{ScenarioConversation, ScenarioAuthorizedExecution, ScenarioAutonomousCommunication}) {
		t.Fatalf("scenario order=%v", got)
	}
	for _, result := range report.Scenarios {
		if result.Status != StatusPass || len(result.Participants) == 0 {
			t.Fatalf("result=%#v", result)
		}
	}
}
