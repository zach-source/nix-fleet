package main

import (
	"fmt"

	"github.com/nixfleet/nixfleet/internal/apply"
)

// applyProgress prints the per-phase lines `nixfleet apply` prints while a host
// deploys. The result carries the built closure, so "Built:" is reported as the
// copy starts.
func applyProgress(phase apply.Phase, result *apply.DeployResult) {
	switch phase {
	case apply.PhaseCopy:
		fmt.Printf("  Built: %s\n", result.Closure)
		fmt.Printf("  Copying closure...\n")
	case apply.PhaseActivate:
		fmt.Printf("  Activating...\n")
	}
}
