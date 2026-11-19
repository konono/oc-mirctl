package collector

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// GetPlatformImages は oc adm release info から platform リリースに含まれる
// 全コンポーネントイメージを取得する
func GetPlatformImages(releaseImage, authFile string) ([]string, error) {
	if _, err := exec.LookPath("oc"); err != nil {
		return nil, fmt.Errorf("oc コマンドが見つかりません")
	}

	args := []string{"adm", "release", "info", releaseImage, "-o", "jsonpath={.references.spec.tags[*].from.name}"}
	if authFile != "" {
		args = append(args, "-a", authFile)
	}

	cmd := exec.Command("oc", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("oc adm release info failed: %s", string(exitErr.Stderr))
		}
		return nil, err
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, fmt.Errorf("platform イメージが取得できませんでした")
	}

	images := strings.Fields(raw)
	return images, nil
}

// GetReleaseImageSize は oc adm release info --size でリリース (platform) の容量を返す。
//
// arch はクラスタのアーキテクチャ (例: amd64)。リリースイメージが multi-arch の場合、
// oc は実行環境 (例: macOS arm64) の OS/Arch でイメージを探して失敗するため、
// --filter-by-os=linux/<arch> を指定する。空なら指定しない。
func GetReleaseImageSize(releaseImage, authFile, arch string) (int64, error) {
	if _, err := exec.LookPath("oc"); err != nil {
		return 0, fmt.Errorf("oc コマンドが見つかりません")
	}

	args := []string{"adm", "release", "info", releaseImage, "--size"}
	if arch != "" {
		args = append(args, "--filter-by-os=linux/"+arch)
	}
	if authFile != "" {
		args = append(args, "-a", authFile)
	}

	cmd := exec.Command("oc", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return 0, fmt.Errorf("oc adm release info --size failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return 0, err
	}
	return parseReleaseSize(string(out))
}

// releaseTotalRe は oc adm release info --size の末尾の合計行にマッチする
// (例: "  21.99GB across 333 layers, 4 different base images")
var releaseTotalRe = regexp.MustCompile(`^\s*([0-9.]+)\s*([kKMGT]?B)\s+across\s+\d+\s+layers`)

// parseReleaseSize は oc adm release info --size の出力から容量 (バイト) を求める。
//
// 末尾の合計行は、コンポーネント間で共有されるレイヤーを重複なく数えた値なので
// これを使う (単位は 1000 単位)。合計行が無い場合のみ、各コンポーネントの
// SIZE MB 列を足す (共有レイヤーを重複して数えるため実際より大きくなる)。
func parseReleaseSize(out string) (int64, error) {
	var totalMB float64
	var count int
	inImages := false

	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if m := releaseTotalRe.FindStringSubmatch(line); m != nil {
			v, err := strconv.ParseFloat(m[1], 64)
			if err == nil {
				return int64(v * float64(decimalUnit(m[2]))), nil
			}
		}
		if strings.HasPrefix(line, "Images:") {
			inImages = true
			continue
		}
		if !inImages {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(line, "  NAME") || !strings.HasPrefix(line, "  ") {
			continue
		}
		// fields: name, age, layers, size_mb, unique_mb, base
		fields := strings.Fields(trimmed)
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
	return int64(totalMB * 1024 * 1024), nil
}

func decimalUnit(unit string) int64 {
	switch strings.ToUpper(unit) {
	case "KB":
		return 1000
	case "MB":
		return 1000 * 1000
	case "GB":
		return 1000 * 1000 * 1000
	case "TB":
		return 1000 * 1000 * 1000 * 1000
	}
	return 1
}
