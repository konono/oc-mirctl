package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/konono/oc-mirctl/internal/collector"
	"github.com/spf13/cobra"
)

var collectCmd = &cobra.Command{
	Use:   "collect",
	Short: "稼働中クラスタから mirror に必要な情報を収集",
	Long: `稼働中の OpenShift クラスタに接続し、以下の情報を収集する:
  - OCP リリースイメージ情報
  - インストール済み Operator (Subscription/CSV) と relatedImages
  - CatalogSource 情報
  - 動いている Pod のコンテナイメージ (完了済みの Pod は除く)

待機中のワークロード (Pod が動いていない Deployment / StatefulSet / DaemonSet /
Job / CronJob / DeploymentConfig) が参照するイメージも収集する
(list / generate のデフォルトで対象。--running-only で除外)。

さらに、現在は使われていないがクラスタに残っている情報も収集する
(list / generate の --all で対象になる。ダウングレードへの備え):
  - ClusterVersion の履歴に残る過去のリリース
  - InstallPlan に残る、過去にインストールした Operator の版
  - ノードにキャッシュされた未使用イメージ

出力先: ./mirror_data/<cluster-name>/collected-data.json
毎回その時点の情報で上書きする。前回のデータと差分があれば backups/ に保存する`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := collector.New(kubeconfig)
		if err != nil {
			return fmt.Errorf("クラスタ接続に失敗: %w", err)
		}

		clusterName, _, err := c.GetClusterName()
		if err != nil {
			return fmt.Errorf("クラスタ名の取得に失敗: %w", err)
		}
		dir := filepath.Join(".", baseDataDir, clusterName)

		newResult, err := c.Collect()
		if err != nil {
			return fmt.Errorf("情報収集に失敗: %w", err)
		}

		// collect は毎回その時点のクラスタ情報で上書きする (過去の collect との累積はしない)。
		// 前回のデータと差分があればバックアップを残す。
		finalResult := newResult
		oldResult, err := collector.LoadPrevious(dir)
		if err == nil {
			backupPath, err := backupIfChanged(dir, oldResult, newResult, time.Now())
			if err != nil {
				return err
			}
			if backupPath != "" {
				fmt.Printf("==> 前回のデータと差分あり。バックアップ: %s\n", backupPath)
			} else {
				fmt.Println("==> 前回のデータと差分なし")
			}

			// 前回の pull-secret.json の認証情報は保持する
			// (今回 imagePullSecrets から取れなかった private registry の認証が失われないように)
			oldPSPath := filepath.Join(dir, "pull-secret.json")
			if oldPS, err := os.ReadFile(oldPSPath); err == nil {
				finalResult.MergePreviousPullSecret(oldPS)
				fmt.Println("==> 前回の pull-secret.json から認証情報をマージ")
			}
		} else {
			fmt.Println("==> 新規作成")
		}

		if err := finalResult.Save(dir); err != nil {
			return fmt.Errorf("出力保存に失敗: %w", err)
		}

		authFile := filepath.Join(dir, "pull-secret.json")
		if _, err := os.Stat(authFile); err != nil {
			authFile = ""
		}
		allImages := finalResult.AllImages()
		if len(allImages) > 0 {
			fmt.Fprintf(os.Stderr, "==> %d イメージのサイズを取得中...\n", len(allImages))
			finalResult.ImageSizes = collector.FetchSizes(allImages, 50, authFile)
			data, err := json.MarshalIndent(finalResult, "", "  ")
			if err != nil {
				return fmt.Errorf("サイズ情報のシリアライズに失敗: %w", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "collected-data.json"), data, 0644); err != nil {
				return fmt.Errorf("サイズ情報の保存に失敗: %w", err)
			}
		}

		fmt.Printf("\n収集完了: %s\n", dir)
		fmt.Printf("  Cluster:          %s\n", finalResult.ClusterName)
		fmt.Printf("  OCP Version:      %s\n", finalResult.OCPVersion)
		fmt.Printf("  Operators:        %d\n", len(finalResult.Operators))
		fmt.Printf("  Pod Images:       %d\n", len(finalResult.PodImages))
		fmt.Printf("  Custom Images:    %d\n", len(finalResult.CustomImages))
		fmt.Printf("  Uncovered Images: %d\n", len(finalResult.UncoveredImages))
		pastOps := 0
		for _, op := range finalResult.Operators {
			pastOps += len(op.PastVersions)
		}
		fmt.Printf("  Idle Images:      %d (待機中のワークロード。--running-only で除外)\n", len(finalResult.IdleImages))
		fmt.Printf("  未使用 (--all で対象): 過去のリリース %d / 過去の Operator の版 %d / 未使用イメージ %d\n",
			len(finalResult.PastReleases), pastOps, len(finalResult.UnusedImages))
		if len(finalResult.UncoveredImages) > 0 {
			fmt.Println("\n⚠ 以下のイメージは platform/operator でカバーされていません。")
			fmt.Println("  generate で additionalImages に自動追加されます:")
			for _, img := range finalResult.UncoveredImages {
				fmt.Printf("    - %s\n", img)
			}
		}
		return nil
	},
}

// backupIfChanged は、前回のデータ (dir/collected-data.json) と今回の結果に実質的な
// 差分がある場合に、上書きされる前の前回データを dir/backups/<時刻>/ に保存する。
// collect は毎回上書きするため、差分の見落としは前回データの消失につながる。
// バックアップを作成した場合はそのパスを、差分が無い場合は "" を返す。
func backupIfChanged(dir string, oldResult, newResult *collector.Result, now time.Time) (string, error) {
	if !hasDiff(oldResult, newResult) {
		return "", nil
	}
	backupDir := filepath.Join(dir, "backups", now.UTC().Format("20060102-150405"))
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("バックアップディレクトリの作成に失敗: %w", err)
	}
	oldData, err := os.ReadFile(filepath.Join(dir, "collected-data.json"))
	if err != nil {
		return "", fmt.Errorf("前回の collected-data.json の読み込みに失敗: %w", err)
	}
	backupPath := filepath.Join(backupDir, "collected-data.json")
	if err := os.WriteFile(backupPath, oldData, 0644); err != nil {
		return "", fmt.Errorf("バックアップの保存に失敗: %w", err)
	}
	return backupPath, nil
}

// hasDiff は old と merged の間に実質的な差分があるか判定する。
//
// collect は毎回 collected-data.json を上書きするため、差分の見落としは前回データの
// 消失につながる。項目を個別に比べると比較漏れが起きやすいので、保存される全項目を
// 正規化して比較する。除外するのは取得タイミングで変わるだけの項目
// (collectedAt / lastSeen / installedAt / イメージサイズ) と、並び順の違いのみ。
func hasDiff(old, merged *collector.Result) bool {
	a, errA := normalizedJSON(old)
	b, errB := normalizedJSON(merged)
	if errA != nil || errB != nil {
		return true // 比較できない場合は安全側 (バックアップを作る)
	}
	return a != b
}

// normalizedJSON は比較用に Result を正規化して JSON 文字列にする (元の値は変更しない)
func normalizedJSON(r *collector.Result) (string, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	var c collector.Result
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}

	// 取得タイミングで変わるだけの項目
	c.CollectedAt = ""
	c.ImageSizes = nil
	c.PlatformImageSize = 0
	for i := range c.Operators {
		op := &c.Operators[i]
		op.LastSeen = ""
		for j := range op.PastVersions {
			op.PastVersions[j].InstalledAt = ""
		}
		sort.Slice(op.RelatedImages, func(x, y int) bool {
			if op.RelatedImages[x].Image != op.RelatedImages[y].Image {
				return op.RelatedImages[x].Image < op.RelatedImages[y].Image
			}
			return op.RelatedImages[x].Name < op.RelatedImages[y].Name
		})
	}

	// 並び順の違いは差分にしない
	sort.Slice(c.Operators, func(x, y int) bool { return c.Operators[x].Key() < c.Operators[y].Key() })
	sort.Slice(c.CatalogSources, func(x, y int) bool { return c.CatalogSources[x].Key() < c.CatalogSources[y].Key() })
	sort.Slice(c.IdleImages, func(x, y int) bool { return c.IdleImages[x].Image < c.IdleImages[y].Image })
	for i := range c.IdleImages {
		sort.Strings(c.IdleImages[i].Workloads)
	}
	for _, list := range [][]string{c.PodImages, c.CustomImages, c.UncoveredImages, c.UnusedImages, c.PrivateRegistries, c.PrivateImages} {
		sort.Strings(list)
	}

	out, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func init() {
	rootCmd.AddCommand(collectCmd)
}
