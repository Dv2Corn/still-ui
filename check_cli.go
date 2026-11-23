package main

// "x-ui check rotation" (or "still-ui check rotation") runs the subscription rotation
// self-check against the subscription server of the running panel.

import (
	"context"
	"fmt"
	"io"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const checkUsage = `usage: x-ui check rotation
  rotation  create a temporary client, re-issue its subscription id the way the client
            form's Save does, and check over HTTP(S) that the running subscription server
            serves the new link and answers 404 for the old one. The client is always deleted.
`

func runCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "rotation" {
		fmt.Fprint(stderr, checkUsage)
		return 2
	}
	logger.InitLogger(logging.WARNING)
	return withPanelDB(stderr, func() int {
		check, err := service.NewSubRotationCheck(&service.SettingService{}, &service.ClientService{}, &service.InboundService{})
		if err != nil {
			fmt.Fprintln(stderr, "failed:", err)
			return 1
		}
		// The CLI has no Xray of its own: the client lives only in the database, which the
		// subscription server reads, so nothing is left to restart once it is deleted.
		report := check.Run(context.Background())
		printSubRotationReport(stdout, report)
		if !report.Passed {
			return 1
		}
		return 0
	})
}

func printSubRotationReport(w io.Writer, r service.SubRotationReport) {
	for _, step := range r.Steps {
		mark := "OK  "
		if !step.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "  %s  %s: %s\n", mark, step.Name, step.Detail)
	}
	if r.Passed {
		fmt.Fprintln(w, "subscription rotation check passed")
	} else {
		fmt.Fprintln(w, "subscription rotation check FAILED")
	}
}
