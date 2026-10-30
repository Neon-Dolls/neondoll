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
	external := flag.Bool("external", false, "include required external participant scenarios")
	shellBodySource := flag.String("shellbody-source", os.Getenv("SHELLBODY_SOURCE"), "pinned neondoll-shellbody checkout")
	shellBodyBinary := flag.String("shellbody-binary", os.Getenv("SHELLBODY_BINARY"), "prebuilt neondoll-shellbody executable")
	shellBodyRevision := flag.String("shellbody-revision", acceptance.ShellBodyRevision, "required neondoll-shellbody revision")
	flag.Parse()

	var driver *acceptance.ShellBodyDriver
	profile := acceptance.Core5Profile()
	if *external {
		driver = acceptance.NewShellBodyDriver(acceptance.ShellBodyDriverConfig{SourceDir: *shellBodySource, Binary: *shellBodyBinary, Revision: *shellBodyRevision})
		defer driver.Close()
		profile = acceptance.Core5ExternalProfile(driver)
	}
	profiles := []acceptance.Profile{profile}
	runner, err := acceptance.NewRunner(profiles...)
	if err != nil {
		fatal(err)
	}
	externalRevisions := parseRevisions(os.Getenv("ACCEPTANCE_EXTERNAL_REVISIONS"))
	if driver != nil {
		externalRevisions["neondoll-shellbody"] = driver.Revision()
	}
	report, runErr := runner.Run(context.Background(), *profileName, acceptance.CoreRevision(), externalRevisions, os.Stdout)
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
