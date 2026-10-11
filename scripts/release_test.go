package scripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func shell(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	// Register shell source files with Go's test cache as well as executing them.
	for _, path := range []string{"deployment_target.sh", "bootstrap_remote.sh", "prepare_native_deployment.sh", "deploy_remote_source.sh", "remote_build_release.sh"} {
		if _, err := os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("bash", args...)
	command.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestDeploymentTargetSelection(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
		fail bool
	}{
		{"default", nil, "root@10.80.192.232|http://10.80.192.232:7077|configs/clusters/xiaozhen-zhiyuan.yaml|systemd", false},
		{"legacy", []string{"ATLAS_DEPLOY_TARGET=legacy-zhiyuan"}, "root@10.111.201.1|http://10.111.201.1:7077|configs/config.yaml|docker", false},
		{"custom", []string{"REMOTE_SSH=atlas@192.0.2.1", "REMOTE_URL=http://192.0.2.1:7077"}, "atlas@192.0.2.1|http://192.0.2.1:7077|configs/clusters/xiaozhen-zhiyuan.yaml", false},
		{"wrong-cluster-check", []string{"REMOTE_SSH=atlas@192.0.2.1"}, "requires REMOTE_URL", true},
		{"unknown", []string{"ATLAS_DEPLOY_TARGET=invalid"}, "Unknown", true},
		{"unknown-runtime", []string{"ATLAS_DEPLOY_RUNTIME=invalid"}, "must be systemd or docker", true},
		{"unsafe-root", []string{"REMOTE_ROOT=/ops/../etc"}, "safe absolute", true},
		{"unsafe-template", []string{"ATLAS_CONFIG_TEMPLATE=configs/../other.yaml"}, "repository YAML", true},
		{"secret-url", []string{"REMOTE_URL=http://user:example@192.0.2.1"}, "credential-free", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			output, err := shell(t, test.env, "-ec", `source ./deployment_target.sh; printf '%s|%s|%s|%s' "$remote_ssh" "$remote_url" "$config_template" "$deployment_runtime"`)
			if (err != nil) != test.fail || !strings.Contains(output, test.want) {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
}

func TestBootstrapRequiresDevelopmentStageBeforeAnySSH(t *testing.T) {
	output, err := shell(t, []string{"ATLAS_BOOTSTRAP=1", "ATLAS_DEPLOYMENT_STAGE=production"}, "./deploy_remote_source.sh")
	if err == nil || !strings.Contains(output, "First deployment requires development") {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestBootstrapPreflightNeverMutatesHost(t *testing.T) {
	for _, test := range []struct{ name, state, listeners string }{
		{"existing-unit", "loaded", ""},
		{"busy-port", "not-found", "LISTEN 0 128 127.0.0.1:15432 0.0.0.0:*"},
		{"new-host", "not-found", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, path := range []string{"/etc/atlas/atlas.env", "/etc/atlas/postgres.env", "/ops/atlas/atlas-server", "/ops/atlas/configs/config.yaml", "/ops/atlas/postgres-data"} {
				if _, err := os.Lstat(path); err == nil {
					t.Skip("host has a real Atlas installation; do not assume blank-host preconditions")
				}
			}
			mocks := t.TempDir()
			logPath := filepath.Join(mocks, "operations.log")
			writeFixture(t, filepath.Join(mocks, "id"), "#!/bin/sh\nprintf '0\\n'\n", 0o755)
			writeFixture(t, filepath.Join(mocks, "systemctl"), `#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$ATLAS_TEST_LOG"
if [ "$1" = show ]; then printf '%s\n' "$ATLAS_TEST_LOAD_STATE"; fi
`, 0o755)
			writeFixture(t, filepath.Join(mocks, "docker"), `#!/bin/sh
printf 'docker %s\n' "$*" >> "$ATLAS_TEST_LOG"
if [ "$1" = container ]; then exit 1; fi
`, 0o755)
			writeFixture(t, filepath.Join(mocks, "ss"), "#!/bin/sh\nprintf '%s\\n' \"$ATLAS_TEST_LISTENERS\"\n", 0o755)
			writeFixture(t, filepath.Join(mocks, "flock"), "#!/bin/sh\nexit 0\n", 0o755)
			output, err := shell(t, []string{
				"PATH=" + mocks + ":" + os.Getenv("PATH"), "ATLAS_TEST_LOG=" + logPath,
				"ATLAS_TEST_LOAD_STATE=" + test.state, "ATLAS_TEST_LISTENERS=" + test.listeners,
				"ATLAS_DEPLOY_RUNTIME=docker",
			}, "./bootstrap_remote.sh", "check", "/ops/atlas")
			if (err == nil) != (test.name == "new-host") {
				t.Fatalf("output=%q err=%v", output, err)
			}
			operations, _ := os.ReadFile(logPath)
			for _, forbidden := range []string{"systemctl start", "systemctl restart", "systemctl enable", "docker create", "docker pull"} {
				if strings.Contains(string(operations), forbidden) {
					t.Fatalf("preflight mutated host: %s", operations)
				}
			}
		})
	}
}

func writeFixture(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func writeArchive(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(gz)
	for name, data := range files {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteReleaseSwitchAndRollback(t *testing.T) {
	for _, previous := range []bool{false, true} {
		for _, mismatch := range []bool{false, true} {
			name := "first"
			if previous {
				name = "existing"
			}
			if mismatch {
				name += "-wrong-commit"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				mocks := filepath.Join(root, "commands")
				logPath := filepath.Join(root, "operations.log")
				writeFixture(t, filepath.Join(root, "configs/config.yaml"), "existing config", 0o600)
				writeFixture(t, filepath.Join(root, "postgres-data/retained"), "database", 0o600)
				if previous {
					writeFixture(t, filepath.Join(root, "atlas-server"), "old binary", 0o755)
					writeFixture(t, filepath.Join(root, "web/dist/index.html"), "old web", 0o644)
				}
				writeFixture(t, filepath.Join(root, "incoming/.placeholder"), "", 0o600)
				writeArchive(t, filepath.Join(root, "incoming/test.source.tar.gz"), map[string]string{"placeholder": "source"})
				writeArchive(t, filepath.Join(root, "incoming/test.web.tar.gz"), map[string]string{"dist/index.html": "new web"})
				writeFixture(t, filepath.Join(mocks, "docker"), `#!/usr/bin/env bash
set -eu
printf 'docker %s\n' "$*" >> "$ATLAS_TEST_LOG"
if [[ "$ATLAS_TEST_MIRROR_FAIL" == 1 && "$1" != run && "$*" == *m.daocloud.io/* ]]; then exit 1; fi
if [[ "$1" == run ]]; then
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == --volume && "$2" == *:/out ]]; then
      output="${2%:/out}"
      printf 'new binary' > "$output/atlas-server"
      printf 'migration binary' > "$output/atlas-db-migrate"
      chmod 755 "$output/atlas-server" "$output/atlas-db-migrate"
    fi
    shift
  done
fi
`, 0o755)
				writeFixture(t, filepath.Join(mocks, "systemctl"), `#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >> "$ATLAS_TEST_LOG"
if [[ "$1" == show ]]; then printf 'loaded\n'; fi
`, 0o755)
				writeFixture(t, filepath.Join(mocks, "curl"), `#!/usr/bin/env bash
if [[ "$*" == *api/v1/status* ]]; then
  printf '{"commit":"%s","version":"v0.120.59"}\n' "$ATLAS_TEST_STATUS_COMMIT"
else
  printf 'Atlas Server is healthy\n'
fi
`, 0o755)
				writeFixture(t, filepath.Join(root, "toolchains/go1.24.10/bin/go"), `#!/usr/bin/env bash
while [[ $# -gt 0 ]]; do
  if [[ "$1" == -o ]]; then
    if [[ "$2" == */atlas-server ]]; then printf 'new binary' > "$2"; else printf 'migration binary' > "$2"; fi
    chmod 755 "$2"
    exit 0
  fi
  shift
done
exit 1
`, 0o755)
				writeFixture(t, filepath.Join(mocks, "systemd-run"), `#!/usr/bin/env bash
printf 'systemd-run %s\n' "$*" >> "$ATLAS_TEST_LOG"
while [[ $# -gt 0 && "$1" != env ]]; do shift; done
exec "$@"
`, 0o755)
				for _, command := range []string{"flock", "sleep"} {
					writeFixture(t, filepath.Join(mocks, command), "#!/bin/sh\nexit 0\n", 0o755)
				}
				statusCommit := "123abcd"
				if mismatch {
					statusCommit = "badcafe"
				}
				mirrorFailure := "0"
				runtime := "systemd"
				if previous {
					runtime = "docker"
				}
				if previous && !mismatch {
					mirrorFailure = "1"
				}
				output, err := shell(t, []string{
					"PATH=" + mocks + ":" + os.Getenv("PATH"), "ATLAS_REMOTE_ROOT=" + root,
					"ATLAS_TEST_LOG=" + logPath, "ATLAS_TEST_STATUS_COMMIT=" + statusCommit,
					"ATLAS_TEST_MIRROR_FAIL=" + mirrorFailure,
					"ATLAS_DEPLOY_RUNTIME=" + runtime,
				}, "./remote_build_release.sh", "test", "v0.120.59", "123abcd")
				if (err != nil) != mismatch {
					t.Fatalf("output=%s err=%v", output, err)
				}
				binary, _ := os.ReadFile(filepath.Join(root, "atlas-server"))
				web, _ := os.ReadFile(filepath.Join(root, "web/dist/index.html"))
				wantBinary, wantWeb := "new binary", "new web"
				if previous && mismatch {
					wantBinary, wantWeb = "old binary", "old web"
				}
				if string(binary) != wantBinary || string(web) != wantWeb {
					t.Fatalf("binary=%q web=%q output=%s", binary, web, output)
				}
				operations, _ := os.ReadFile(logPath)
				if !previous && (strings.Contains(string(operations), "docker ") || !strings.Contains(string(operations), "systemd-run")) {
					t.Fatal("new native deployment must build without invoking Docker")
				}
				if mirrorFailure == "1" && (!strings.Contains(output, "identical pinned digest") || !strings.Contains(string(operations), "docker.io/library/golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac")) {
					t.Fatal("mirror fallback must keep the original pinned digest")
				}
				if !previous && mismatch && !strings.Contains(string(operations), "systemctl stop atlas.service") {
					t.Fatal("failed first installation must stop only the newly created Atlas service")
				}
				if previous && !strings.Contains(string(operations), "systemctl restart atlas.service") {
					t.Fatal("existing release must restart its Atlas service")
				}
				for _, path := range []string{"incoming/test.source.tar.gz", "incoming/test.web.tar.gz", "postgres-data/retained"} {
					if _, err := os.Stat(filepath.Join(root, path)); err != nil {
						t.Fatalf("release/database evidence removed: %s", path)
					}
				}
			})
		}
	}
}
