package apply

import (
	"context"
	"fmt"
	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/preflight"
	"github.com/nixfleet/nixfleet/internal/ssh"
)

type RemoteChecks struct{}

func (RemoteChecks) Preflight(ctx context.Context, client *ssh.Client, host *inventory.Host) error {
	result, err := preflight.NewChecker().RunAll(ctx, client, inventory.NormalizeBase(host.Base))
	if err != nil {
		return err
	}
	if !result.Passed {
		return fmt.Errorf("%s", result.Summary)
	}
	return nil
}
func (RemoteChecks) Health(ctx context.Context, client *ssh.Client, storePath string) error {
	result, err := client.ExecSudo(ctx, ssh.ShellQuote(storePath+"/bin/nixfleet-verify-health"))
	if err != nil {
		return err
	}
	if result == nil || result.ExitCode != 0 {
		return fmt.Errorf("configured health checks failed (or closure lacks the checker)")
	}
	return nil
}
