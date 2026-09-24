package cli

import (
	"github.com/nict-ocpai/oc-mirctl/internal/mirror"
	"github.com/spf13/cobra"
)

var (
	dumpScript  bool
	destSkipTLS bool
)

var runMirrorCmd = &cobra.Command{
	Use:   "mirror",
	Short: "oc-mirror を実行してイメージをミラーリング",
	Long: `デフォルト: oc-mirror を直接実行する
--dump-script: Mirror VM で実行するシェルスクリプトを stdout に出力する

スクリプトは Mirror VM 上で実行される前提で、destination を $(hostname -f):8443 に
自動解決する (oc-mirror v2 の graph-image ホスト名問題の回避)。

mirror_data/ 下のクラスタディレクトリを自動検出`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		m, err := mirror.New(dir, destSkipTLS)
		if err != nil {
			return err
		}

		if dumpScript {
			return m.DumpScript()
		}
		return m.Execute()
	},
}

func init() {
	runMirrorCmd.Flags().BoolVar(&dumpScript, "dump-script", false, "Mirror VM で実行するスクリプトを stdout に出力")
	runMirrorCmd.Flags().BoolVar(&destSkipTLS, "dest-skip-tls", true, "Mirror registry の TLS 検証をスキップ")
	rootCmd.AddCommand(runMirrorCmd)
}
