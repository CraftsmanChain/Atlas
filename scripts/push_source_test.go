package scripts_test

import (
	"os"
	"os/exec"
	"testing"
)

func TestIndependentSourcePushBehavior(t *testing.T) {
	for _, path := range []string{"deploy_remote_source.sh", "push_source_remotes.py", "test_push_source_remotes.py"} {
		if _, err := os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("python3", "test_push_source_remotes.py", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dual push behavior: %v\n%s", err, out)
	}
}
