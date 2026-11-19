package cli

import (
	"fmt"

	"github.com/nict-ocpai/oc-mirctl/internal/generator"
	"github.com/spf13/cobra"
)

var (
	mirrorRegistry      string
	generateAll         bool
	generateRunningOnly bool
	pinOperators        bool
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "ImageSetConfiguration / disconnected config を生成",
	Long: `collect で収集した情報を元に以下を生成する:
  - oc-mirror 用 ImageSetConfiguration (imageset-config.yaml)
  - disconnected install 用の追加設定 (disconnected-overrides.yaml)

デフォルトでは、collect 時点で使用中のもの (稼働中の OCP リリース、Subscription の
Operator、Pod のイメージ) と、待機中のワークロード (Pod が動いていない Deployment /
StatefulSet / DaemonSet / Job / CronJob / DeploymentConfig) が参照するイメージを対象にする。
--running-only を指定すると、待機中のワークロードのイメージを除き、動いている Pod の
イメージだけにする。
--all を指定すると、collect 時点でクラスタに残っている未使用の情報も対象に含める
(ダウングレードへの備え):
  - ClusterVersion の履歴に残る過去のリリース
  - InstallPlan に残る、過去にインストールした Operator の版
  - ノードにキャッシュされた未使用イメージ

Operator はデフォルトで「現行 CSV 〜 チャネル最新」をミラーする (minVersion のみ指定)。
--pin-operators を指定すると現行 CSV のバージョンに固定する (minVersion = maxVersion)。

mirror_data/ 下のクラスタディレクトリを自動検出`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		if generateAll {
			fmt.Println("==> --all: 未使用の過去のリリース / Operator の版 / キャッシュ済みイメージも対象に含めます")
		}

		g, err := generator.New(dir, mirrorRegistry, generator.Options{
			All:          generateAll,
			RunningOnly:  generateRunningOnly,
			PinOperators: pinOperators,
		})
		if err != nil {
			return fmt.Errorf("初期化に失敗: %w", err)
		}

		if err := g.GenerateImageSetConfig(); err != nil {
			return fmt.Errorf("ImageSetConfiguration 生成に失敗: %w", err)
		}

		if mirrorRegistry != "" {
			if err := g.GenerateDisconnectedOverrides(); err != nil {
				return fmt.Errorf("disconnected 設定生成に失敗: %w", err)
			}
		}

		fmt.Printf("\n生成完了: %s\n", dir)
		return nil
	},
}

func init() {
	generateCmd.Flags().StringVar(&mirrorRegistry, "mirror-registry", "", "Mirror registry URL (host:port) — 省略時は imageset-config.yaml のみ生成")
	generateCmd.Flags().BoolVar(&generateAll, "all", false, "collect 時点でクラスタに残っている未使用の情報 (過去のリリース / Operator の版 / キャッシュ済みイメージ) も対象にする")
	generateCmd.Flags().BoolVar(&generateRunningOnly, "running-only", false, "待機中のワークロード (Pod が動いていない Deployment / CronJob / DaemonSet 等) のイメージを除き、動いている Pod のイメージだけを対象にする")
	generateCmd.Flags().BoolVar(&pinOperators, "pin-operators", false, "Operator を現行 CSV のバージョンに固定する (minVersion = maxVersion)")
	rootCmd.AddCommand(generateCmd)
}
