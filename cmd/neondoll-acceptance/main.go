package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Neon-Dolls/neondoll/acceptance"
)

func main() {
	profileName := flag.String("profile", "core5", "acceptance profile")
	reportPath := flag.String("report", "acceptance-report.json", "JSON report path")
	flag.Parse()

	profiles := []acceptance.Profile{acceptance.Core5Profile()}
	runner, err := acceptance.NewRunner(profiles...)
	if err != nil {
		fatal(err)
	}
	external := parseRevisions(os.Getenv("ACCEPTANCE_EXTERNAL_REVISIONS"))
	report, runErr := runner.Run(context.Background(), *profileName, acceptance.CoreRevision(), external, os.Stdout)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "acceptance: %v; available profiles: %s\n", runErr, strings.Join(acceptance.ProfileNames(profiles...), ", "))
		os.Exit(2)
	}
	if err = report.WriteJSON(*reportPath); err != nil {
		fatal(fmt.Errorf("write report: %w", err))
	}
	fmt.Printf("report: %s\n", *reportPath)
	if !report.Successful() {
		os.Exit(1)
	}
}

func parseRevisions(value string) map[string]string {
	result := make(map[string]string)
	for _, item := range strings.Split(value, ",") {
		name, revision, ok := strings.Cut(item, "=")
		if ok && strings.TrimSpace(name) != "" && strings.TrimSpace(revision) != "" {
			result[strings.TrimSpace(name)] = strings.TrimSpace(revision)
		}
	}
	return result
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "acceptance:", err)
	os.Exit(2)
}
