package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type rawManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Layers        []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
	Config struct {
		Size int64 `json:"size"`
	} `json:"config"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Size     int64  `json:"size"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

// normalizeImageRef は tag+digest 形式 (image:tag@sha256:...) をダイジェストのみに変換する
func normalizeImageRef(image string) string {
	atIdx := strings.Index(image, "@sha256:")
	if atIdx < 0 {
		return image
	}
	repo := image[:atIdx]
	digest := image[atIdx:]
	if colonIdx := strings.LastIndex(repo, ":"); colonIdx > 0 {
		afterColon := repo[colonIdx+1:]
		if !strings.Contains(afterColon, "/") {
			repo = repo[:colonIdx]
		}
	}
	return repo + digest
}

func skopeoInspectRaw(image, authFile string) ([]byte, error) {
	ref := normalizeImageRef(image)
	args := []string{"inspect", "--raw", "docker://" + ref}
	if authFile != "" {
		// 相対パスを絶対パスに変換
		abs, err := filepath.Abs(authFile)
		if err == nil {
			authFile = abs
		}
		args = append(args, "--authfile", authFile)
	}
	cmd := exec.Command("skopeo", args...)
	out, err := cmd.Output()
	if err != nil {
		// stderr を取得してデバッグ
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%w: %s", err, string(exitErr.Stderr))
		}
	}
	return out, err
}

func FetchImageSize(image string, authFile string) (int64, error) {
	normalized := normalizeImageRef(image)
	out, err := skopeoInspectRaw(image, authFile)
	if err != nil {
		return -1, fmt.Errorf("skopeo inspect failed for %s: %w", normalized, err)
	}

	var manifest rawManifest
	if err := json.Unmarshal(out, &manifest); err != nil {
		return -1, err
	}

	if len(manifest.Layers) > 0 {
		var total int64
		for _, l := range manifest.Layers {
			total += l.Size
		}
		total += manifest.Config.Size
		if total > 0 {
			return total, nil
		}
	}

	if len(manifest.Manifests) > 0 {
		repo := extractRepo(normalized)
		for _, m := range manifest.Manifests {
			if m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
				ref := repo + "@" + m.Digest
				return FetchImageSize(ref, authFile)
			}
		}
		ref := repo + "@" + manifest.Manifests[0].Digest
		return FetchImageSize(ref, authFile)
	}

	return -1, fmt.Errorf("no layer size found for %s", normalized)
}

func extractRepo(image string) string {
	if idx := strings.LastIndex(image, "@"); idx > 0 {
		return image[:idx]
	}
	if idx := strings.LastIndex(image, ":"); idx > 0 {
		afterColon := image[idx+1:]
		if !strings.Contains(afterColon, "/") {
			return image[:idx]
		}
	}
	return image
}

// isDigestRef はダイジェスト参照 (@sha256:...) かどうかを判定する
// ダイジェスト参照は不変なのでキャッシュ可能。タグ参照は更新される可能性があるためキャッシュしない
func isDigestRef(image string) bool {
	return strings.Contains(image, "@sha256:")
}

type sizeCache struct {
	Sizes map[string]int64 `json:"sizes"`
	path  string
}

func loadSizeCache(dir string) *sizeCache {
	path := filepath.Join(dir, ".image-size-cache.json")
	c := &sizeCache{Sizes: map[string]int64{}, path: path}
	raw, err := os.ReadFile(path)
	if err == nil {
		json.Unmarshal(raw, c)
	}
	return c
}

func (c *sizeCache) save() {
	data, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(c.path, data, 0644)
}

// FetchSizes は複数のイメージサイズを並行で取得する
// ダイジェスト参照 (@sha256:...) の成功結果のみキャッシュする
// タグ参照と失敗結果はキャッシュしない
func FetchSizes(images []string, concurrency int, authFile string) map[string]int64 {
	// キャッシュをロード (output-dir から取得)
	cacheDir := ""
	if authFile != "" {
		cacheDir = filepath.Dir(authFile)
	}
	cache := loadSizeCache(cacheDir)

	result := map[string]int64{}
	var mu sync.Mutex
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	total := len(images)
	done := 0
	cacheHits := 0

	for _, img := range images {
		// ダイジェスト参照で、キャッシュに成功結果があればスキップ
		if isDigestRef(img) {
			if size, ok := cache.Sizes[img]; ok && size > 0 {
				mu.Lock()
				result[img] = size
				done++
				cacheHits++
				mu.Unlock()
				continue
			}
		}

		wg.Add(1)
		go func(image string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			size, err := FetchImageSize(image, authFile)
			mu.Lock()
			defer mu.Unlock()
			done++
			if err != nil {
				result[image] = -1
			} else {
				result[image] = size
				// ダイジェスト参照の成功結果のみキャッシュ
				if isDigestRef(image) && size > 0 {
					cache.Sizes[image] = size
				}
			}
			if done%20 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "    %d/%d 完了 (cache hit: %d)\n", done, total, cacheHits)
			}
		}(img)
	}

	wg.Wait()

	if cacheHits < total {
		cache.save()
	}

	if cacheHits > 0 {
		fmt.Fprintf(os.Stderr, "    cache hit: %d/%d\n", cacheHits, total)
	}

	return result
}

func FormatSize(bytes int64) string {
	if bytes < 0 {
		return "  unknown"
	}
	if bytes == 0 {
		return "    0 B"
	}

	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%5.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%5.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%5.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%5d B", bytes)
	}
}
