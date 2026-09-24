package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

const baseDataDir = "mirror_data"

var (
	kubeconfig string
)

var rootCmd = &cobra.Command{
	Use:     "oc-mirctl",
	Short:   "OpenShift disconnected mirror tooling",
	Long:    "稼働中の OpenShift クラスタから mirror に必要な情報を収集し、disconnected インストール用の設定を生成する",
	Version: fmt.Sprintf("%s (commit=%s, built=%s)", version, commit, buildDate),
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: $KUBECONFIG)")
}
