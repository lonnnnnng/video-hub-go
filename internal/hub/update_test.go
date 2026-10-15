package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func releaseFixture() githubRelease {
	r := githubRelease{Tag: "v99.0.0", PublishedAt: "2026-10-15T14:33:58Z"}
	for _, name := range []string{"moviehub-linux-" + runtime.GOARCH, "SHA256SUMS"} {
		r.Assets = append(r.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}{name, "https://github.com/" + releaseRepo + "/releases/download/" + r.Tag + "/" + name})
	}
	return r
}
func TestInstallReleaseVerificationAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "checksum", "rollback", "rollback-restart-failed"} {
		t.Run(scenario, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "moviehub")
			old := []byte("old program")
			next := []byte("\x7fELFnew program")
			if err := os.WriteFile(binary, old, 0755); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(next)
			checksum := hex.EncodeToString(hash[:])
			if scenario == "checksum" {
				checksum = strings.Repeat("0", 64)
			}
			client := releaseClient()
			client.Transport = hlsTransport(func(req *http.Request) (*http.Response, error) {
				body := string(next)
				if strings.HasSuffix(req.URL.Path, "SHA256SUMS") {
					body = checksum + "  moviehub-linux-" + runtime.GOARCH + "\n"
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			record := updateRecord{Version: "v99.0.0", Release: releaseFixture(), Target: updateTarget{Binary: binary}}
			restarts := 0
			restart := func() error {
				restarts++
				if scenario == "rollback-restart-failed" && restarts == 2 {
					return fmt.Errorf("failed")
				}
				return nil
			}
			health := func(version string) bool { return scenario == "success" || version == Version }
			err := installRelease(context.Background(), client, record, restart, health, func(string) {})
			got, _ := os.ReadFile(binary)
			if scenario == "success" {
				if err != nil || string(got) != string(next) || restarts != 1 {
					t.Fatalf("success: %v, %s, %d", err, got, restarts)
				}
			} else {
				if err == nil || string(got) != string(old) {
					t.Fatalf("old binary not preserved: %v %s", err, got)
				}
				if scenario == "checksum" && restarts != 0 {
					t.Fatal("checksum mismatch restarted service")
				}
				if scenario == "rollback" && !strings.Contains(err.Error(), "已回滚") {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestReleaseBoundaries(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{{"v0.10.0", "v0.9.9", true}, {"v0.3.0", "v0.3.0", false}, {"v0.2.1", "v0.3.0", false}, {"v1.0.0", "dev", false}, {"v1.0.0-rc1", "v0.3.0", false}} {
		if got := newerRelease(tc.latest, tc.current); got != tc.want {
			t.Errorf("%+v => %v", tc, got)
		}
	}
	r := releaseFixture()
	r.Assets[0].URL = "https://example.com/program"
	if _, err := releaseAsset(r, r.Assets[0].Name); err == nil {
		t.Fatal("untrusted asset accepted")
	}
	r = releaseFixture()
	r.Assets = append(r.Assets, r.Assets[0])
	if _, err := releaseAsset(r, r.Assets[0].Name); err == nil {
		t.Fatal("duplicate asset accepted")
	}
}
