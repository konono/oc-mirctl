package cli

import (
	"errors"
	"fmt"

	"github.com/konono/oc-mirctl/internal/update"
	"github.com/spf13/cobra"
)

var (
	updateCheckOnly bool
	updateForce     bool
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "oc-mirctl を最新の Release に更新",
	Long: `GitHub Releases (https://github.com/konono/oc-mirctl/releases) から、
実行環境の OS / アーキテクチャ向けの最新のビルド済みバイナリを取得し、
実行中の oc-mirctl を置き換える。ダウンロードしたアーカイブは checksums.txt の
SHA256 で検証してから置き換える。

  --check  更新の有無を表示するだけで置き換えない
  --force  開発ビルド (make build 等) で現在のバージョンを比較できない場合や、
           最新と同じ版でも、最新の Release で置き換える

インストール先に書き込む権限が必要 (例: /usr/local/bin なら sudo oc-mirctl update)。`,
	Args: cobra.NoArgs,
	// 失敗の多くはネットワークや Release の状態によるもので、使い方の誤りではないため Usage を出さない
	SilenceUsage: true,
	// エラーは Execute (root.go) が表示するので、cobra の "Error: ..." と二重にしない
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		err := update.Run(currentVersion(), update.Options{CheckOnly: updateCheckOnly, Force: updateForce})
		if errors.Is(err, update.ErrNoRelease) {
			return fmt.Errorf("%w (https://github.com/konono/oc-mirctl/releases)", err)
		}
		return err
	},
}

func init() {
	updateCmd.Flags().BoolVar(&updateCheckOnly, "check", false, "更新の有無を表示するだけで置き換えない")
	updateCmd.Flags().BoolVar(&updateForce, "force", false, "現在のバージョンを比較できない場合や最新と同じ版でも、最新の Release で置き換える")
	rootCmd.AddCommand(updateCmd)
}
