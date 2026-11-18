package cli

import (
	"fmt"

	"github.com/nict-ocpai/oc-mirctl/internal/collector"
	"github.com/nict-ocpai/oc-mirctl/internal/generator"
	"github.com/spf13/cobra"
)

var (
	mirrorRegistry string
	generateAll    bool
	pinOperators   bool
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "ImageSetConfiguration / disconnected config を生成",
	Long: `collect で収集した情報を元に以下を生成する:
  - oc-mirror 用 ImageSetConfiguration (imageset-config.yaml)
  - disconnected install 用の追加設定 (disconnected-overrides.yaml)

デフォルトでは直近の collect 結果 (current-data.json) のみを対象にする。
アップグレードを繰り返したクラスタでも、現在稼働中のイメージだけがミラー対象になる。
--all を指定すると、過去の collect 結果との和集合 (collected-data.json) を対象にする。

Operator はデフォルトで「現行 CSV 〜 チャネル最新」をミラーする (minVersion のみ指定)。
--pin-operators を指定すると現行 CSV のバージョンに固定する (minVersion = maxVersion)。

mirror_data/ 下のクラスタディレクトリを自動検出`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		// データファイルの存在確認 (current-data.json 未作成時に分かりやすいエラーを出す)
		if _, err := collector.LoadData(dir, generateAll); err != nil {
			return err
		}
		dataFile := collector.DataFile(generateAll)
		fmt.Printf("==> 入力: %s\n", dataFile)

		g, err := generator.New(dir, mirrorRegistry, generator.Options{
			DataFile:     dataFile,
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
	generateCmd.Flags().BoolVar(&generateAll, "all", false, "過去の collect 結果との和集合 (collected-data.json) を対象にする")
	generateCmd.Flags().BoolVar(&pinOperators, "pin-operators", false, "Operator を現行 CSV のバージョンに固定する (minVersion = maxVersion)")
	rootCmd.AddCommand(generateCmd)
}
