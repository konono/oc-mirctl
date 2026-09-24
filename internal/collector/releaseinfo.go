package collector

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GetReleaseImageSize は oc adm release info --size を使ってリリースイメージの
// 全コンポーネントの SIZE MB を合計する
func GetReleaseImageSize(releaseImage, authFile string) (int64, error) {
	if _, err := exec.LookPath("oc"); err != nil {
		return 0, fmt.Errorf("oc コマンドが見つかりません")
	}

	args := []string{"adm", "release", "info", releaseImage, "--size"}
	if authFile != "" {
		args = append(args, "-a", authFile)
	}

	cmd := exec.Command("oc", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return 0, fmt.Errorf("oc adm release info --size failed: %s", string(exitErr.Stderr))
		}
		return 0, err
	}

	// "Images:" セクション以降の行をパースして SIZE MB 列を合計する
	// フォーマット: "  <name>  <age>  <layers>  <size_mb>  <unique_mb>  <base>"
	var totalMB float64
	var count int
	inImages := false

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Images:") {
			inImages = true
			continue
		}
		if !inImages {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(line, "  NAME") {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			break
		}

		fields := strings.Fields(trimmed)
		// fields: name, age, layers, size_mb, unique_mb, base
		if len(fields) < 5 {
			continue
		}
		sizeMB, err := strconv.ParseFloat(fields[len(fields)-3], 64)
		if err != nil {
			continue
		}
		totalMB += sizeMB
		count++
	}

	if count == 0 {
		return 0, fmt.Errorf("リリースイメージのサイズ情報が取得できませんでした")
	}

	totalBytes := int64(totalMB * 1024 * 1024)
	return totalBytes, nil
}
