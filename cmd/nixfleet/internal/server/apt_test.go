package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixfleet/nixfleet/internal/apt"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
)

func TestUpgradeRecordsOnlySuccess(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		client := ssh.NewMockClient()
		client.DefaultResult = &ssh.ExecResult{ExitCode: exitCode}
		client.RegisterCommandOutput("sudo if [ -e "+state.StatePath, `{"current_generation":42,"last_os_update":"2025-01-01T00:00:00Z"}`, 0)
		client.RegisterCommandOutput("sudo (", "", 0)
		s := &Server{aptMgr: apt.NewManager(), stateMgr: state.NewManager()}
		result, err := s.upgradeApt(context.Background(), client, true)
		if err != nil {
			t.Fatal(err)
		}
		if result.Success != (exitCode == 0) {
			t.Fatalf("incorrect result: %+v", result)
		}
		writes := 0
		for _, cmd := range client.ExecLog {
			if strings.Contains(cmd, "<< 'NIXFLEET_STATE'") {
				writes++
				var recorded state.HostState
				body := strings.SplitN(strings.SplitN(cmd, "<< 'NIXFLEET_STATE'\n", 2)[1], "\nNIXFLEET_STATE", 2)[0]
				if err := json.Unmarshal([]byte(body), &recorded); err != nil {
					t.Fatal(err)
				}
				if recorded.CurrentGeneration != 42 || !recorded.LastOSUpdate.Equal(result.EndTime) || recorded.LastOSUpdate.Before(time.Now().Add(-time.Minute)) {
					t.Fatalf("incorrect state: %+v", recorded)
				}
			}
		}
		if exitCode != 0 && len(client.ExecLog) != 1 {
			t.Fatalf("failed upgrade touched state: %v", client.ExecLog)
		}
		if exitCode == 0 && writes != 1 {
			t.Fatalf("successful upgrade wrote state %d times", writes)
		}
	}
}
