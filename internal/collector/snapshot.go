package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// AllDataFile は collect の度に和集合マージされる累積データ (--all モード用)
	AllDataFile = "collected-data.json"
	// CurrentDataFile は直近の collect 結果のみを保持するスナップショット (デフォルトモード用)
	CurrentDataFile = "current-data.json"
)

// DataFile はモードに応じたデータファイル名を返す
func DataFile(all bool) string {
	if all {
		return AllDataFile
	}
	return CurrentDataFile
}

// CurrentSnapshot は今回の collect 結果 (マージ前) から current-data.json 用の
// スナップショットを作る。imageSizes は累積データ側で取得済みのサイズから
// スナップショットに含まれるイメージ分だけを引き継ぐ。
func CurrentSnapshot(latest *Result, sizes map[string]int64) *Result {
	snap := *latest
	snap.pullSecret = nil
	snap.ImageSizes = nil
	if len(sizes) > 0 {
		snap.ImageSizes = map[string]int64{}
		for _, img := range snap.AllImages() {
			if s, ok := sizes[img]; ok {
				snap.ImageSizes[img] = s
			}
		}
	}
	return &snap
}

// SaveCurrent は current-data.json を保存する (pull-secret は保存しない)
func (r *Result) SaveCurrent(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, CurrentDataFile)
	fmt.Printf("==> %s に保存 (最新状態のスナップショット)\n", path)
	return os.WriteFile(path, data, 0644)
}

// LoadData はモードに応じたデータファイルを読み込む。
// current-data.json が無い場合 (本機能導入前に collect したデータ) は
// 累積データへ黙ってフォールバックせずエラーにする。
func LoadData(dir string, all bool) (*Result, error) {
	path := filepath.Join(dir, DataFile(all))
	raw, err := os.ReadFile(path)
	if err != nil {
		if !all && os.IsNotExist(err) {
			return nil, fmt.Errorf("%s が見つかりません。collect を再実行するか、累積データを使う場合は --all を指定してください", path)
		}
		return nil, fmt.Errorf("%s の読み込みに失敗: %w", path, err)
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%s のパースに失敗: %w", path, err)
	}
	return &result, nil
}
