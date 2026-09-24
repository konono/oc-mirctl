package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// resolveOutputDir は ./mirror_data/ 下のクラスタディレクトリを自動検出する
func resolveOutputDir() (string, error) {
	base := filepath.Join(".", baseDataDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("%s/ が見つかりません。先に collect を実行してください", baseDataDir)
	}

	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dataFile := filepath.Join(base, e.Name(), "collected-data.json")
		if _, err := os.Stat(dataFile); err == nil {
			dirs = append(dirs, e.Name())
		}
	}

	if len(dirs) == 0 {
		return "", fmt.Errorf("%s/ に collected-data.json を含むディレクトリが見つかりません", baseDataDir)
	}

	sort.Strings(dirs)

	if len(dirs) == 1 {
		dir := filepath.Join(base, dirs[0])
		fmt.Fprintf(os.Stderr, "==> %s を使用\n", dir)
		return dir, nil
	}

	msg := fmt.Sprintf("%s/ に複数のクラスタディレクトリがあります:\n", baseDataDir)
	for _, d := range dirs {
		msg += fmt.Sprintf("  %s\n", filepath.Join(base, d))
	}
	return "", fmt.Errorf(msg)
}
