package cli

import (
	"fmt"

	"github.com/nict-ocpai/oc-mirctl/internal/generator"
	"github.com/spf13/cobra"
)

var (
	mirrorRegistry string
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "ImageSetConfiguration / disconnected config を生成",
	Long: `collect で収集した情報を元に以下を生成する:
  - oc-mirror 用 ImageSetConfiguration (imageset-config.yaml)
  - disconnected install 用の追加設定 (disconnected-overrides.yaml)

mirror_data/ 下のクラスタディレクトリを自動検出`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		g, err := generator.New(dir, mirrorRegistry)
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
	rootCmd.AddCommand(generateCmd)
}
