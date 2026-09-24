package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nict-ocpai/oc-mirctl/internal/collector"
	"github.com/spf13/cobra"
)

var collectCmd = &cobra.Command{
	Use:   "collect",
	Short: "稼働中クラスタから mirror に必要な情報を収集",
	Long: `稼働中の OpenShift クラスタに接続し、以下の情報を収集する:
  - OCP リリースイメージ情報
  - インストール済み Operator (Subscription/CSV) と relatedImages
  - CatalogSource 情報
  - 全 Pod で使用中のコンテナイメージ

出力先: ./mirror_data/<cluster-name>/
既存の collected-data.json がある場合、差分があればバックアップを取ってマージする`,
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

		var finalResult *collector.Result
		oldResult, err := collector.LoadPrevious(dir)
		if err == nil {
			// マージ
			finalResult = collector.Merge(oldResult, newResult)

			// 差分チェック: マージ結果が前回と同じならバックアップ不要
			if hasDiff(oldResult, finalResult) {
				backupDir := filepath.Join(dir, "backups",
					time.Now().UTC().Format("20060102-150405"))
				if err := os.MkdirAll(backupDir, 0755); err != nil {
					return fmt.Errorf("バックアップディレクトリの作成に失敗: %w", err)
				}

				oldPath := filepath.Join(dir, "collected-data.json")
				oldData, _ := os.ReadFile(oldPath)
				backupPath := filepath.Join(backupDir, "collected-data.json")
				if err := os.WriteFile(backupPath, oldData, 0644); err != nil {
					return fmt.Errorf("バックアップの保存に失敗: %w", err)
				}
				fmt.Printf("==> バックアップ: %s\n", backupPath)
				fmt.Println("==> 前回のデータとマージ (差分あり)")

				oldOps := len(oldResult.Operators)
				newOps := len(newResult.Operators)
				mergedOps := len(finalResult.Operators)
				if mergedOps != oldOps {
					fmt.Printf("    Operators: %d → %d\n", oldOps, mergedOps)
				}
				if len(finalResult.PodImages) != len(oldResult.PodImages) {
					fmt.Printf("    Pod Images: %d → %d\n", len(oldResult.PodImages), len(finalResult.PodImages))
				}
				if len(finalResult.CustomImages) != len(oldResult.CustomImages) {
					fmt.Printf("    Custom Images: %d → %d\n", len(oldResult.CustomImages), len(finalResult.CustomImages))
				}
				_ = newOps
			} else {
				fmt.Println("==> 前回のデータと差分なし (バックアップ不要)")
			}
		} else {
			fmt.Println("==> 新規作成")
			finalResult = newResult
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
		fmt.Printf("  Cluster:        %s\n", finalResult.ClusterName)
		fmt.Printf("  OCP Version:    %s\n", finalResult.OCPVersion)
		fmt.Printf("  Operators:      %d\n", len(finalResult.Operators))
		fmt.Printf("  Pod Images:     %d\n", len(finalResult.PodImages))
		fmt.Printf("  Custom Images:  %d\n", len(finalResult.CustomImages))
		return nil
	},
}

// hasDiff は old と merged の間に実質的な差分があるか判定する
// (collectedAt や lastSeen のタイムスタンプ差分は無視)
func hasDiff(old, merged *collector.Result) bool {
	if old.OCPVersion != merged.OCPVersion {
		return true
	}
	if old.OCPChannel != merged.OCPChannel {
		return true
	}
	if len(old.Operators) != len(merged.Operators) {
		return true
	}
	if len(old.PodImages) != len(merged.PodImages) {
		return true
	}
	if len(old.CustomImages) != len(merged.CustomImages) {
		return true
	}
	if len(old.CatalogSources) != len(merged.CatalogSources) {
		return true
	}

	// Operator の中身を比較 (channel, relatedImages の数)
	oldOps := map[string]string{}
	oldRelated := map[string]int{}
	for _, op := range old.Operators {
		oldOps[op.Key()] = op.Channel
		oldRelated[op.Key()] = len(op.RelatedImages)
	}
	for _, op := range merged.Operators {
		if oldOps[op.Key()] != op.Channel {
			return true
		}
		if oldRelated[op.Key()] != len(op.RelatedImages) {
			return true
		}
	}

	// CatalogSource の image を比較
	oldCats := map[string]string{}
	for _, cs := range old.CatalogSources {
		oldCats[cs.Key()] = cs.Image
	}
	for _, cs := range merged.CatalogSources {
		if oldCats[cs.Key()] != cs.Image {
			return true
		}
	}

	// JSON で比較するのが確実だが、コスト重視で上記のヒューリスティクスを使用
	// 最終手段: PodImages の中身比較
	oldJSON, _ := json.Marshal(old.PodImages)
	mergedJSON, _ := json.Marshal(merged.PodImages)
	if string(oldJSON) != string(mergedJSON) {
		return true
	}

	return false
}

func init() {
	rootCmd.AddCommand(collectCmd)
}
