package apt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

type upgradeExecFunc func(context.Context, string) (*ssh.ExecResult, error)

func (f upgradeExecFunc) ExecSudo(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	return f(ctx, cmd)
}

func TestSecurityUpgradeInstallsAndPropagatesFailure(t *testing.T) {
	for _, exitCode := range []int{0, 9} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			bin, temporary := t.TempDir(), t.TempDir()
			for _, name := range []string{"mktemp", "rm", "cat", "grep"} {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			scripts := map[string]string{
				"apt-config": "#!/bin/sh\nprintf '%s\\n' 'Unattended-Upgrade::Allowed-Origins:: \"Ubuntu:noble-updates\";' 'Unattended-Upgrade::Origins-Pattern:: \"origin=*\";'\n",
				"apt-get":    "#!/bin/sh\necho 'unexpected general upgrade' >&2\nexit 88\n",
				"unattended-upgrade": `#!/bin/sh
case "$*" in *--dry-run*) echo 'Packages that will be upgraded: openssl'; exit 0;; esac
[ "$*" = '--verbose' ] || exit 81
[ -n "$APT_CONFIG" ] || exit 82
cat "$APT_CONFIG" > "$CONFIG_COPY"
if [ "$UPGRADE_EXIT" != 0 ]; then echo 'fixture install failure' >&2; exit "$UPGRADE_EXIT"; fi
: > "$UPGRADE_MARKER"
echo 'Setting up openssl (1.2.3)'
`,
			}
			for name, content := range scripts {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			marker, snapshot := filepath.Join(bin, "installed"), filepath.Join(bin, "config")
			client := upgradeExecFunc(func(ctx context.Context, command string) (*ssh.ExecResult, error) {
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
				cmd.Env = []string{"PATH=" + bin, "TMPDIR=" + temporary, "UPGRADE_EXIT=" + strconv.Itoa(exitCode), "UPGRADE_MARKER=" + marker, "CONFIG_COPY=" + snapshot}
				output, err := cmd.CombinedOutput()
				result := &ssh.ExecResult{Stdout: string(output)}
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					result.ExitCode = exit.ExitCode()
					return result, nil
				}
				return result, err
			})
			result, err := NewManager().Upgrade(context.Background(), client, true)
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != (exitCode == 0) {
				t.Fatalf("incorrect result: %+v", result)
			}
			_, installedErr := os.Stat(marker)
			if exitCode == 0 && installedErr != nil {
				t.Fatal("upgrade claimed success without installing packages")
			}
			if exitCode != 0 && (installedErr == nil || !strings.Contains(result.Error, "9")) {
				t.Fatalf("installation failure masked: %+v", result)
			}
			config, err := os.ReadFile(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{
				`#clear Unattended-Upgrade::Allowed-Origins;`, `#clear Unattended-Upgrade::Origins-Pattern;`,
				`Dir::Etc::parts "-";`, `Dir::Etc::main "/dev/null";`,
				`"Ubuntu:${distro_codename}-security";`, `"UbuntuESMApps:${distro_codename}-apps-security";`,
				`"UbuntuESM:${distro_codename}-infra-security";`,
				`Unattended-Upgrade::InstallOnShutdown "false";`,
			} {
				if !strings.Contains(string(config), required) {
					t.Errorf("missing security policy: %s", required)
				}
			}
			files, err := os.ReadDir(temporary)
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary configuration not cleaned: %v %v", files, err)
			}
		})
	}
}

func TestFullUpgradeAndTransportFailure(t *testing.T) {
	client := ssh.NewMockClient()
	result, err := NewManager().Upgrade(context.Background(), client, false)
	if err != nil || !result.Success || len(client.ExecLog) != 1 || client.ExecLog[0] != "sudo DEBIAN_FRONTEND=noninteractive apt-get upgrade -y" {
		t.Fatalf("full upgrade changed: %+v %v %v", result, err, client.ExecLog)
	}
	transportError := errors.New("connection lost")
	result, err = NewManager().Upgrade(context.Background(), upgradeExecFunc(func(context.Context, string) (*ssh.ExecResult, error) { return nil, transportError }), true)
	if !errors.Is(err, transportError) || result.Success {
		t.Fatalf("transport failure masked: %+v %v", result, err)
	}
}
