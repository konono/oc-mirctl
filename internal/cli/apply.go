package cli

import (
	"fmt"

	"github.com/nict-ocpai/oc-mirctl/internal/applier"
	"github.com/spf13/cobra"
)

var (
	resultsDir string
	dryRun     bool
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "oc-mirror の結果 (IDMS/CatalogSource) をクラスタに適用",
	Long: `oc-mirror が生成した IDMS/ITMS/CatalogSource をクラスタに適用する。
既存の IDMS エントリがある場合はマージし、上書きしない。

--dry-run: 適用内容を表示するだけで実際には適用しない`,
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := applier.New(kubeconfig, resultsDir, dryRun)
		if err != nil {
			return fmt.Errorf("初期化に失敗: %w", err)
		}
		return a.Apply()
	},
}

func init() {
	applyCmd.Flags().StringVar(&resultsDir, "results-dir", "", "oc-mirror の results ディレクトリ (例: mirror-data/oc-mirror-workspace/results-*)")
	applyCmd.Flags().BoolVar(&dryRun, "dry-run", false, "適用内容を表示するだけで実際には適用しない")
	_ = applyCmd.MarkFlagRequired("results-dir")
	rootCmd.AddCommand(applyCmd)
}
