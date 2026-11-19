// Package update は GitHub Releases から最新のビルド済みバイナリを取得し、
// 実行中の oc-mirctl を置き換える (konono/panecom の update をベースにしている)。
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	repoOwner     = "konono"
	repoName      = "oc-mirctl"
	binaryName    = "oc-mirctl"
	checksumsName = "checksums.txt"
	defaultAPIURL = "https://api.github.com"
)

// ErrNoRelease は Release がまだ 1 つも公開されていないことを表す
var ErrNoRelease = errors.New("公開されている Release がまだありません")

// HTTPClient は http.Client の Do だけを使うためのインターフェース (テスト用)
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Options は update コマンドの挙動を制御する
type Options struct {
	// CheckOnly が true の場合、更新の有無を表示するだけで置き換えない
	CheckOnly bool
	// Force が true の場合、現在のバージョンと比較できない (開発ビルド等) 場合や
	// 最新と同じ版でも、最新の Release で置き換える
	Force bool
}

// Updater は更新処理の依存 (HTTP、実行環境) をまとめたもの
type Updater struct {
	HTTPClient     HTTPClient
	CurrentVersion string
	GOOS           string
	GOARCH         string
	Out            io.Writer
	ExecPath       string // 置き換え対象 (空なら実行中のバイナリ)
	APIURL         string // GitHub API のベース URL (テストで差し替える)
}

// Run は実行環境の既定値で更新処理を行う
func Run(currentVersion string, opts Options) error {
	u := &Updater{
		HTTPClient:     &http.Client{Timeout: 5 * time.Minute},
		CurrentVersion: currentVersion,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Out:            os.Stderr,
	}
	return u.Execute(opts)
}

// Execute は最新の Release を確認し、必要なら実行中のバイナリを置き換える
func (u *Updater) Execute(opts Options) error {
	u.printf("==> 最新の Release を確認...\n")
	release, err := u.fetchLatestRelease()
	if err != nil {
		return err
	}
	latest := release.TagName

	cmp, cmpErr := compareVersions(latest, u.CurrentVersion)
	switch {
	case cmpErr != nil:
		// 開発ビルド (make build の git describe、"dev" 等) はリリースと比較できない
		u.printf("    現在: %s (リリース版ではないため比較できません)\n    最新: %s\n", u.CurrentVersion, latest)
		if opts.CheckOnly {
			return nil
		}
		if !opts.Force {
			return fmt.Errorf("現在のバージョン %q はリリース版ではないため自動更新しません。"+
				"最新の Release (%s) で置き換える場合は --force を指定してください", u.CurrentVersion, latest)
		}
	case cmp <= 0:
		u.printf("    現在: %s / 最新: %s → 最新です\n", u.CurrentVersion, latest)
		if opts.CheckOnly || !opts.Force {
			return nil
		}
	default:
		u.printf("    現在: %s / 最新: %s → 更新があります\n", u.CurrentVersion, latest)
		if opts.CheckOnly {
			u.printf("    更新するには oc-mirctl update を実行してください\n")
			return nil
		}
	}

	archiveName := fmt.Sprintf("%s_%s_%s.tar.gz", binaryName, u.GOOS, u.GOARCH)
	archiveURL, err := findAsset(release, archiveName)
	if err != nil {
		return err
	}
	checksumsURL, err := findAsset(release, checksumsName)
	if err != nil {
		return err
	}

	targetPath, err := u.executablePath()
	if err != nil {
		return fmt.Errorf("実行中のバイナリのパスを特定できません: %w", err)
	}

	u.printf("==> %s をダウンロード...\n", archiveName)
	archive, err := u.download(archiveURL)
	if err != nil {
		return fmt.Errorf("%s のダウンロードに失敗: %w", archiveName, err)
	}
	checksums, err := u.download(checksumsURL)
	if err != nil {
		return fmt.Errorf("%s のダウンロードに失敗: %w", checksumsName, err)
	}
	if err := verifyChecksum(archive, archiveName, checksums); err != nil {
		return err
	}
	u.printf("    チェックサム (SHA256) を検証しました\n")

	binary, err := extractBinary(archive, binaryName)
	if err != nil {
		return err
	}
	if err := replaceBinary(targetPath, binary); err != nil {
		return err
	}
	u.printf("==> %s を %s に更新しました (oc-mirctl --version で確認できます)\n", targetPath, latest)
	return nil
}

func (u *Updater) printf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(u.Out, format, args...)
}

// ReleaseInfo は GitHub API の Release のうち必要な項目
type ReleaseInfo struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset は Release に添付されたファイル
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (u *Updater) fetchLatestRelease() (*ReleaseInfo, error) {
	base := u.APIURL
	if base == "" {
		base = defaultAPIURL
	}
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, repoOwner, repoName)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("最新の Release の取得に失敗: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNoRelease
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("最新の Release の取得に失敗: GitHub API が %d を返しました: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var release ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("Release 情報の解析に失敗: %w", err)
	}
	if release.TagName == "" {
		return nil, errors.New("Release 情報にタグがありません")
	}
	return &release, nil
}

func findAsset(release *ReleaseInfo, name string) (string, error) {
	for _, a := range release.Assets {
		if a.Name == name {
			return a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("Release %s に %s がありません (この OS / アーキテクチャ向けのバイナリが無い可能性があります)", release.TagName, name)
}

func (u *Updater) download(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// verifyChecksum は checksums.txt ("<sha256>  <ファイル名>" 形式) と照合する
func verifyChecksum(data []byte, name string, checksums []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, fields[0]) {
			return fmt.Errorf("%s のチェックサムが一致しません (期待値 %s、実際 %s)。更新を中止しました", name, fields[0], got)
		}
		return nil
	}
	return fmt.Errorf("%s に %s のチェックサムがありません。更新を中止しました", checksumsName, name)
}

func extractBinary(archive []byte, name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("アーカイブの展開に失敗: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("アーカイブの読み込みに失敗: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == name {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("アーカイブに %s がありません", name)
}

func (u *Updater) executablePath() (string, error) {
	if u.ExecPath != "" {
		return u.ExecPath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// replaceBinary は同じディレクトリに一時ファイルを書いてから rename で置き換える
// (書き込み途中で失敗しても既存のバイナリは壊れない)
func replaceBinary(targetPath string, binary []byte) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, binaryName+".update.*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%s に書き込む権限がありません。権限のあるユーザーで実行してください (例: sudo oc-mirctl update): %w", dir, err)
		}
		return fmt.Errorf("一時ファイルの作成に失敗: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(binary); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("一時ファイルへの書き込みに失敗: %w", err)
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("実行権限の設定に失敗: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("一時ファイルのクローズに失敗: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%s を置き換える権限がありません。権限のあるユーザーで実行してください (例: sudo oc-mirctl update): %w", targetPath, err)
		}
		return fmt.Errorf("バイナリの置き換えに失敗: %w", err)
	}
	tmpPath = ""
	return nil
}
