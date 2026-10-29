// Package acceptance provides a milestone-oriented behavioral acceptance
// runner. Scenarios use production Core contracts while profiles select which
// observable behaviors a milestone requires.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

type Participant struct {
	Kind     string `json:"kind"`
	Mode     string `json:"mode"`
	Revision string `json:"revision,omitempty"`
	Boundary string `json:"boundary"`
}

type Scenario struct {
	ID           string
	Description  string
	Participants []Participant
	Run          func(context.Context) error
}

type Profile struct {
	Name      string
	Scenarios []Scenario
}

type ScenarioResult struct {
	ID           string        `json:"id"`
	Description  string        `json:"description"`
	Status       Status        `json:"status"`
	Duration     time.Duration `json:"duration_ns"`
	Failure      string        `json:"failure_reason,omitempty"`
	Participants []Participant `json:"participants"`
}

type Report struct {
	Version           int               `json:"version"`
	Profile           string            `json:"profile"`
	CoreRevision      string            `json:"core_revision"`
	ExternalRevisions map[string]string `json:"external_component_revisions"`
	StartedAt         time.Time         `json:"started_at"`
	Duration          time.Duration     `json:"duration_ns"`
	Status            Status            `json:"status"`
	Scenarios         []ScenarioResult  `json:"scenarios"`
}

type Runner struct {
	profiles map[string]Profile
	now      func() time.Time
}

var ErrUnknownProfile = errors.New("acceptance: unknown profile")

type skipError struct{ reason string }

func (e skipError) Error() string { return e.reason }

// Skip marks a scenario visibly skipped. Required profile scenarios are not
// converted to passes by the runner.
func Skip(reason string) error { return skipError{reason: strings.TrimSpace(reason)} }

func NewRunner(profiles ...Profile) (*Runner, error) {
	registered := make(map[string]Profile, len(profiles))
	for _, profile := range profiles {
		if strings.TrimSpace(profile.Name) == "" {
			return nil, errors.New("acceptance: profile name is required")
		}
		if _, exists := registered[profile.Name]; exists {
			return nil, fmt.Errorf("acceptance: duplicate profile %q", profile.Name)
		}
		seen := make(map[string]struct{}, len(profile.Scenarios))
		for index, scenario := range profile.Scenarios {
			if strings.TrimSpace(scenario.ID) == "" || scenario.Run == nil {
				return nil, fmt.Errorf("acceptance: profile %q has invalid scenario %d", profile.Name, index)
			}
			if _, duplicate := seen[scenario.ID]; duplicate {
				return nil, fmt.Errorf("acceptance: profile %q duplicates scenario %q", profile.Name, scenario.ID)
			}
			seen[scenario.ID] = struct{}{}
		}
		registered[profile.Name] = profile
	}
	return &Runner{profiles: registered, now: time.Now}, nil
}

func (r *Runner) Run(ctx context.Context, profileName, revision string, external map[string]string, console io.Writer) (Report, error) {
	profile, ok := r.profiles[profileName]
	if !ok {
		return Report{}, fmt.Errorf("%w %q", ErrUnknownProfile, profileName)
	}
	if strings.TrimSpace(revision) == "" {
		revision = "unknown"
	}
	started := r.now().UTC()
	report := Report{Version: 1, Profile: profile.Name, CoreRevision: revision, ExternalRevisions: copyStrings(external), StartedAt: started, Status: StatusPass}
	for _, scenario := range profile.Scenarios {
		result := ScenarioResult{ID: scenario.ID, Description: scenario.Description, Status: StatusPass, Participants: append([]Participant(nil), scenario.Participants...)}
		if console != nil {
			fmt.Fprintf(console, "[ RUN  ] %s\n", scenario.ID)
		}
		scenarioStarted := r.now()
		err := runScenario(ctx, scenario.Run)
		result.Duration = r.now().Sub(scenarioStarted)
		if err != nil {
			var skipped skipError
			if errors.As(err, &skipped) {
				result.Status, result.Failure = StatusSkip, skipped.Error()
			} else {
				result.Status, result.Failure = StatusFail, err.Error()
			}
			report.Status = StatusFail
		}
		if console != nil {
			fmt.Fprintf(console, "[ %-5s] %s (%s)", strings.ToUpper(string(result.Status)), scenario.ID, result.Duration.Round(time.Millisecond))
			if result.Failure != "" {
				fmt.Fprintf(console, ": %s", result.Failure)
			}
			fmt.Fprintln(console)
		}
		report.Scenarios = append(report.Scenarios, result)
	}
	report.Duration = r.now().Sub(started)
	return report, nil
}

func runScenario(ctx context.Context, run func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("scenario panic: %v", recovered)
		}
	}()
	return run(ctx)
}

func (r Report) WriteJSON(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func (r Report) Successful() bool { return r.Status == StatusPass }

func CoreRevision() string {
	if value := strings.TrimSpace(os.Getenv("ACCEPTANCE_CORE_REVISION")); value != "" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}
	return "unknown"
}

func ProfileNames(profiles ...Profile) []string {
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.Name)
	}
	sort.Strings(names)
	return names
}

func copyStrings(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
