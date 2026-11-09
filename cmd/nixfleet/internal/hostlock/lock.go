// Package hostlock coordinates cooperating host-side writers.
package hostlock

import (
	_ "embed"
	"fmt"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"time"
)

const MutationPath = "/var/lib/nixfleet/mutation.lock"

//go:embed lock.sh
var Script string

func Wrap(command, path string, timeout time.Duration) string {
	if timeout <= 0 {
		return command
	}
	seconds := int64((timeout + time.Second - 1) / time.Second)
	return fmt.Sprintf("(\nset -eu\n%s\nnixfleet_lock_enter %s %d\n%s\n)", Script, ssh.ShellQuote(path), seconds, command)
}
