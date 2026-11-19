package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeRelease struct {
	tag          string
	archive      []byte
	checksumLine string // 空なら正しいチェックサム
	noAsset      bool
	status       int // releases/latest の HTTP ステータス (0 = 200)
}

func newServer(t *testing.T, r fakeRelease) *httptest.Server {
	t.Helper()
	archiveName := fmt.Sprintf("oc-mirctl_%s_%s.tar.gz", "linux", "amd64")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/repos/konono/oc-mirctl/releases/latest":
			if r.status != 0 {
				w.WriteHeader(r.status)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			assets := []Asset{{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/dl/checksums.txt"}}
			if !r.noAsset {
				assets = append(assets, Asset{Name: archiveName, BrowserDownloadURL: srv.URL + "/dl/" + archiveName})
			}
			_ = json.NewEncoder(w).Encode(ReleaseInfo{TagName: r.tag, Assets: assets})
		case "/dl/" + archiveName:
			_, _ = w.Write(r.archive)
		case "/dl/checksums.txt":
			line := r.checksumLine
			if line == "" {
				sum := sha256.Sum256(r.archive)
				line = hex.EncodeToString(sum[:]) + "  " + archiveName
			}
			_, _ = w.Write([]byte("0000  oc-mirctl_darwin_arm64.tar.gz\n" + line + "\n"))
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T, current string, r fakeRelease) (*Updater, string, *bytes.Buffer) {
	t.Helper()
	srv := newServer(t, r)
	exe := filepath.Join(t.TempDir(), "oc-mirctl")
	if err := os.WriteFile(exe, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &Updater{
		HTTPClient:     srv.Client(),
		CurrentVersion: current,
		GOOS:           "linux",
		GOARCH:         "amd64",
		Out:            out,
		ExecPath:       exe,
		APIURL:         srv.URL,
	}, exe, out
}

func content(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExecute(t *testing.T) {
	newBin := []byte("new-binary")
	archive := tarGz(t, map[string][]byte{"oc-mirctl": newBin, "README.md": []byte("readme")})

	cases := []struct {
		name     string
		current  string
		release  fakeRelease
		opts     Options
		wantErr  string
		wantFile string
	}{
		{"更新あり → 置き換える", "v0.1.0", fakeRelease{tag: "v0.2.0", archive: archive}, Options{}, "", "new-binary"},
		{"疑似バージョン (go install) → 置き換える", "v0.0.0-20261119073401-c56b2626fbe1", fakeRelease{tag: "v0.1.0", archive: archive}, Options{}, "", "new-binary"},
		{"最新 → 何もしない", "v0.2.0", fakeRelease{tag: "v0.2.0", archive: archive}, Options{}, "", "old"},
		{"最新でも --force → 置き換える", "v0.2.0", fakeRelease{tag: "v0.2.0", archive: archive}, Options{Force: true}, "", "new-binary"},
		{"--check → 置き換えない", "v0.1.0", fakeRelease{tag: "v0.2.0", archive: archive}, Options{CheckOnly: true}, "", "old"},
		{"開発ビルド → エラー (置き換えない)", "c56b262-dirty", fakeRelease{tag: "v0.2.0", archive: archive}, Options{}, "--force", "old"},
		{"開発ビルド + --check → エラーにしない", "dev", fakeRelease{tag: "v0.2.0", archive: archive}, Options{CheckOnly: true}, "", "old"},
		{"開発ビルド + --force → 置き換える", "dev", fakeRelease{tag: "v0.2.0", archive: archive}, Options{Force: true}, "", "new-binary"},
		// タグ後の make build (git describe) は古い Release で上書きしない (rev 指摘)
		{"タグ後の開発ビルド (git describe) → エラー", "v0.1.0-1-gabcdef1", fakeRelease{tag: "v0.1.0", archive: archive}, Options{}, "--force", "old"},
		{"タグ後の開発ビルド (dirty) → エラー", "v0.1.0-1-gabcdef1-dirty", fakeRelease{tag: "v0.1.0", archive: archive}, Options{}, "--force", "old"},
		{"タグ上の dirty ビルド → エラー", "v0.1.0-dirty", fakeRelease{tag: "v0.2.0", archive: archive}, Options{}, "--force", "old"},
		{"変更ありの go build (+dirty) → エラー", "v0.0.0-20261119073401-c56b2626fbe1+dirty", fakeRelease{tag: "v0.1.0", archive: archive}, Options{}, "--force", "old"},
		{"通常のプレリリース → 比較して更新", "v0.2.0-rc.1", fakeRelease{tag: "v0.2.0", archive: archive}, Options{}, "", "new-binary"},
		{"チェックサム不一致 → 中止", "v0.1.0", fakeRelease{tag: "v0.2.0", archive: archive, checksumLine: strings.Repeat("ab", 32) + "  oc-mirctl_linux_amd64.tar.gz"}, Options{}, "チェックサムが一致しません", "old"},
		{"チェックサムの行が無い → 中止", "v0.1.0", fakeRelease{tag: "v0.2.0", archive: archive, checksumLine: "abcd  other.tar.gz"}, Options{}, "チェックサムがありません", "old"},
		{"対象プラットフォームのアセットが無い", "v0.1.0", fakeRelease{tag: "v0.2.0", archive: archive, noAsset: true}, Options{}, "oc-mirctl_linux_amd64.tar.gz がありません", "old"},
		{"アーカイブにバイナリが無い", "v0.1.0", fakeRelease{tag: "v0.2.0", archive: tarGz(t, map[string][]byte{"README.md": []byte("x")})}, Options{}, "アーカイブに oc-mirctl がありません", "old"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, exe, out := setup(t, c.current, c.release)
			err := u.Execute(c.opts)
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v\n%s", err, out)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, c.wantErr)
			}
			if got := content(t, exe); got != c.wantFile {
				t.Errorf("binary content = %q, want %q", got, c.wantFile)
			}
			if c.wantFile == "new-binary" {
				if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0755 {
					t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
				}
			}
			// 一時ファイルが残っていない
			entries, _ := os.ReadDir(filepath.Dir(exe))
			if len(entries) != 1 {
				t.Errorf("leftover files: %v", entries)
			}
		})
	}
}

func TestExecuteNoRelease(t *testing.T) {
	u, _, _ := setup(t, "v0.1.0", fakeRelease{status: http.StatusNotFound})
	if err := u.Execute(Options{}); !errors.Is(err, ErrNoRelease) {
		t.Errorf("error = %v, want ErrNoRelease", err)
	}
}

func TestReplaceBinaryPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission test requires a non-root unix user")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "oc-mirctl")
	if err := os.WriteFile(exe, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })

	err := replaceBinary(exe, []byte("new"))
	if err == nil || !strings.Contains(err.Error(), "権限がありません") {
		t.Fatalf("error = %v, want permission hint", err)
	}
	if got := content(t, exe); got != "old" {
		t.Errorf("binary must be unchanged, got %q", got)
	}
}
