//go:build darwin || linux

package tests

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the real installer with local download fixtures and an allowlisted PATH.
// No test can reach the network, invoke real sudo, or replace a user's binary.
func TestInstallVerification(t *testing.T) {
	cases := []installCase{
		{name: "curl_sha256sum_latest"},
		{name: "wget_sha256sum_pinned", downloader: "wget", version: "v9.8.7"},
		{name: "curl_shasum_pinned", hashTool: "shasum", version: "v9.8.7"},
		{name: "prefer_curl_and_sha256sum", downloader: "both", hashTool: "both"},
		{name: "stdin_install", stdin: true},
		{name: "linux_arm64", arch: "aarch64"},
		{name: "linux_amd64_alias", arch: "amd64"},
		{name: "darwin_amd64", os: "Darwin", hashTool: "shasum"},
		{name: "darwin_arm64", os: "Darwin", arch: "arm64", hashTool: "shasum"},
		{name: "uppercase_crlf_checksum", checksum: "uppercase_crlf"},
		{name: "unrelated_checksum_entries", checksum: "unrelated"},
		{name: "no_downloader", downloader: "none", wantError: "need curl or wget"},
		{name: "no_hash_tool", hashTool: "none", wantError: "need sha256sum or shasum"},
		{name: "archive_download_failure", downloadFailure: "archive", wantError: "failed to download rote_"},
		{name: "checksum_download_failure", downloadFailure: "checksums", wantError: "failed to download checksums.txt"},
		{name: "wget_checksum_download_failure", downloader: "wget", downloadFailure: "checksums", wantError: "failed to download checksums.txt"},
		{name: "stdin_checksum_download_failure", stdin: true, downloadFailure: "checksums", wantError: "failed to download checksums.txt"},
		{name: "checksum_mismatch", checksum: "mismatch", wantError: "checksum mismatch"},
		{name: "empty_checksum_file", checksum: "empty", wantError: "exactly one valid SHA-256"},
		{name: "missing_checksum_entry", checksum: "missing", wantError: "exactly one valid SHA-256"},
		{name: "short_checksum", checksum: "short", wantError: "exactly one valid SHA-256"},
		{name: "long_checksum", checksum: "long", wantError: "exactly one valid SHA-256"},
		{name: "nonhex_checksum", checksum: "nonhex", wantError: "exactly one valid SHA-256"},
		{name: "duplicate_checksum", checksum: "duplicate", wantError: "exactly one valid SHA-256"},
		{name: "conflicting_checksums", checksum: "conflicting", wantError: "exactly one valid SHA-256"},
		{name: "extra_checksum_fields", checksum: "extra_fields", wantError: "exactly one valid SHA-256"},
		{name: "sha256sum_failure", hashFailure: "failure", wantError: "failed to calculate SHA-256"},
		{name: "sha256sum_output_then_failure", hashFailure: "output_then_failure", wantError: "failed to calculate SHA-256"},
		{name: "primary_hash_failure_with_fallback_available", hashTool: "both", hashFailure: "failure", wantError: "failed to calculate SHA-256"},
		{name: "shasum_failure", hashTool: "shasum", hashFailure: "failure", wantError: "failed to calculate SHA-256"},
		{name: "shasum_output_then_failure", hashTool: "shasum", hashFailure: "output_then_failure", wantError: "failed to calculate SHA-256"},
		{name: "empty_hash_output", hashFailure: "empty", wantError: "checksum mismatch"},
		{name: "malformed_hash_output", hashFailure: "malformed", wantError: "checksum mismatch"},
	}
	for _, shell := range []string{"/bin/sh", "/bin/bash"} {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					for _, existing := range []bool{false, true} {
						t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
							testInstall(t, shell, tc, existing)
						})
					}
				})
			}
		})
	}
}

type installCase struct {
	stdin                                         bool
	name, downloader, hashTool, version, os, arch string
	downloadFailure, checksum, hashFailure        string
	wantError                                     string
}

func testInstall(t *testing.T, shell string, tc installCase, existing bool) {
	t.Helper()
	if tc.os == "" {
		tc.os = "Linux"
	}
	if tc.arch == "" {
		tc.arch = "x86_64"
	}
	if tc.downloader == "" {
		tc.downloader = "curl"
	}
	if tc.hashTool == "" {
		tc.hashTool = "sha256sum"
	}
	arch := tc.arch
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "aarch64":
		arch = "arm64"
	}
	asset := fmt.Sprintf("rote_%s_%s.tar.gz", tc.os, arch)
	baseURL := "https://github.com/zhh2001/rote/releases/latest/download"
	if tc.version != "" {
		baseURL = "https://github.com/zhh2001/rote/releases/download/" + tc.version
	}

	// Spaces exercise quoting in PATH, download paths, and the install target.
	dir := filepath.Join(t.TempDir(), "installer sandbox")
	bin, target, scratch := filepath.Join(dir, "tools"), filepath.Join(dir, "install"), filepath.Join(dir, "downloads")
	for _, path := range []string{bin, target, scratch} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	events := filepath.Join(dir, "events")
	writeInstallFile(t, events, "", 0o600)
	original := "existing installation must survive\n"
	installed := filepath.Join(target, "rote")
	if existing {
		writeInstallFile(t, installed, original, 0o755)
	}
	payload := "#!/bin/sh\nprintf 'executed\\n' >> \"$INSTALL_TEST_EVENTS\"\nprintf 'rote 9.8.7\\n'\n"
	archive := installArchive(t, payload)
	if err := os.WriteFile(filepath.Join(dir, "archive"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(archive))
	entry := func(hash string) string { return hash + "  " + asset + "\n" }
	manifest := entry(digest)
	switch tc.checksum {
	case "uppercase_crlf":
		manifest = strings.ReplaceAll(entry(strings.ToUpper(digest)), "\n", "\r\n")
	case "unrelated":
		manifest = entry(digest) + digest + "  another-archive.tar.gz\n"
	case "mismatch":
		manifest = entry(strings.Repeat("0", 64))
	case "empty":
		manifest = ""
	case "missing":
		manifest = digest + "  " + asset + ".other\n"
	case "short":
		manifest = entry(digest[:63])
	case "long":
		manifest = entry(digest + "0")
	case "nonhex":
		manifest = entry(strings.Repeat("z", 64))
	case "duplicate":
		manifest += manifest
	case "conflicting":
		manifest += entry(strings.Repeat("0", 64))
	case "extra_fields":
		manifest = strings.TrimSpace(manifest) + " extra\n"
	}
	writeInstallFile(t, filepath.Join(dir, "checksums"), manifest, 0o600)
	for _, tool := range []string{"awk", "chmod", "cp", "gzip", "mkdir", "mktemp", "mv", "rm"} {
		if err := os.Symlink(installTool(t, tool), filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	writeInstallTool(t, bin, "uname", "case \"$1\" in\n-s) printf '%s\\n' "+shellQuote(tc.os)+" ;;\n-m) printf '%s\\n' "+shellQuote(tc.arch)+" ;;\n*) exit 90 ;;\nesac\n")
	writeInstallTool(t, bin, "sudo", "printf 'sudo\\n' >> \"$INSTALL_TEST_EVENTS\"\nexit 90\n")
	writeInstallTool(t, bin, "tar", "printf 'extracted\\n' >> \"$INSTALL_TEST_EVENTS\"\nexec "+shellQuote(installTool(t, "tar"))+" \"$@\"\n")
	for _, tool := range []string{"curl", "wget"} {
		if tc.downloader == tool || tc.downloader == "both" {
			writeInstallTool(t, bin, tool, installDownloadStub)
		}
	}

	// Use real hashing, including when a host only provides one of the two tools.
	// The wrappers normalize arguments while recording the installer's selection.
	for _, tool := range []string{"sha256sum", "shasum"} {
		if tc.hashTool != tool && tc.hashTool != "both" {
			continue
		}
		args := "[ \"$#\" = 1 ]\nfile=$1\n"
		if tool == "shasum" {
			args = "[ \"$#\" = 3 ] && [ \"$1\" = -a ] && [ \"$2\" = 256 ] || exit 90\nfile=$3\n"
		}
		realTool := tool
		realHash, err := exec.LookPath(realTool)
		if err != nil {
			realTool = "shasum"
			if tool == "shasum" {
				realTool = "sha256sum"
			}
			realHash = installTool(t, realTool)
		}
		hashCommand := shellQuote(realHash)
		if realTool == "shasum" {
			hashCommand += " -a 256"
		}
		hashCommand += " \"$file\""
		body := hashCommand + "\n"
		switch tc.hashFailure {
		case "failure":
			body = "exit 1\n"
		case "output_then_failure":
			body += "exit 1\n"
		case "empty":
			body = "exit 0\n"
		case "malformed":
			body = "printf 'not-a-digest\\n'\n"
		}
		writeInstallTool(t, bin, tool, args+"printf 'hash-"+tool+"\\n' >> \"$INSTALL_TEST_EVENTS\"\n"+body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, shell, script)
	if tc.stdin {
		source, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.CommandContext(ctx, shell)
		cmd.Stdin = bytes.NewReader(source)
	}
	// Do not inherit user settings, shell startup hooks, or download credentials.
	cmd.Env = []string{
		"PATH=" + bin, "HOME=" + dir, "TMPDIR=" + scratch, "LC_ALL=C",
		"INSTALL_DIR=" + target, "ROTE_VERSION=" + tc.version,
		"INSTALL_TEST_DIR=" + dir, "INSTALL_TEST_EVENTS=" + events,
		"INSTALL_TEST_URL=" + baseURL, "INSTALL_TEST_ASSET=" + asset,
		"INSTALL_TEST_DOWNLOAD_FAILURE=" + tc.downloadFailure,
	}
	output, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("installer timed out: %v\n%s", ctx.Err(), output)
	}
	activity, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(activity), "sudo") {
		t.Fatalf("installer attempted elevation: %s", activity)
	}
	data, readErr := os.ReadFile(installed)
	if tc.wantError != "" {
		if runErr == nil || !strings.Contains(string(output), tc.wantError) {
			t.Fatalf("want failure containing %q, got %v\n%s", tc.wantError, runErr, output)
		}
		if strings.Contains(string(activity), "extracted") || strings.Contains(string(activity), "executed") {
			t.Fatalf("unverified archive was extracted or executed: %s", activity)
		}
		if existing {
			if readErr != nil || string(data) != original {
				t.Fatalf("failed verification changed existing installation: %q, %v", data, readErr)
			}
		} else if !os.IsNotExist(readErr) {
			t.Fatalf("failed verification created an installation: %q, %v", data, readErr)
		}
	} else {
		if runErr != nil {
			t.Fatalf("install failed: %v\n%s", runErr, output)
		}
		if readErr != nil || string(data) != payload {
			t.Fatalf("wrong installed binary: %q, %v", data, readErr)
		}
		if !strings.Contains(string(output), "checksum verified") || !strings.Contains(string(output), "rote 9.8.7") {
			t.Fatalf("missing verification/version confirmation: %s", output)
		}
		downloader, hashTool := tc.downloader, tc.hashTool
		if downloader == "both" {
			downloader = "curl"
		}
		if hashTool == "both" {
			hashTool = "sha256sum"
		}
		wantActivity := downloader + "-archive\n" + downloader + "-checksums\nhash-" + hashTool + "\nextracted\nexecuted\n"
		if string(activity) != wantActivity {
			t.Fatalf("unexpected operation order: %q, want %q", activity, wantActivity)
		}
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("installer did not clean temporary downloads: %v, %v", entries, err)
	}
}

const installDownloadStub = `tool=${0##*/}
case "$tool" in
curl) [ "$#" = 4 ] && [ "$1" = -fsSL ] && [ "$3" = -o ] || exit 90; url=$2; destination=$4 ;;
wget) [ "$#" = 3 ] && [ "$1" = -qO ] || exit 90; destination=$2; url=$3 ;;
*) exit 90 ;;
esac
case "$url" in
"$INSTALL_TEST_URL/$INSTALL_TEST_ASSET") kind=archive ;;
"$INSTALL_TEST_URL/checksums.txt") kind=checksums ;;
*) printf 'unexpected URL: %s\n' "$url" >&2; exit 90 ;;
esac
printf '%s-%s\n' "$tool" "$kind" >> "$INSTALL_TEST_EVENTS"
cp "$INSTALL_TEST_DIR/$kind" "$destination"
# Even a failed transfer can leave a complete-looking file on disk.
if [ "$INSTALL_TEST_DOWNLOAD_FAILURE" = "$kind" ]; then exit 22; fi
`

func installArchive(t *testing.T, payload string) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "rote", Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func installTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func writeInstallTool(t *testing.T, dir, name, body string) {
	t.Helper()
	writeInstallFile(t, filepath.Join(dir, name), "#!/bin/sh\nset -eu\n"+body, 0o755)
}

func writeInstallFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}
