package cli

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version / commit / buildDate は make build の -ldflags で埋め込まれる。
// go install で入れた場合は埋め込まれないので、Go のビルド情報から補う (versionString)。
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
	Version: versionString(),
}

func versionString() string {
	info, _ := debug.ReadBuildInfo()
	v, c, d := resolveVersion(version, commit, buildDate, info)
	return fmt.Sprintf("%s (commit=%s, built=%s)", v, c, d)
}

// resolveVersion は ldflags で埋め込まれていない値 (既定値のまま) をビルド情報から補う。
//   - go install <module>@<version>: Main.Version にモジュールのバージョン
//     (タグ、または v0.0.0-<時刻>-<コミット> 形式の疑似バージョン) が入る
//   - リポジトリ内での go build: vcs.revision / vcs.time にコミットと時刻が入る
func resolveVersion(v, c, d string, info *debug.BuildInfo) (string, string, string) {
	if info == nil {
		return v, c, d
	}
	if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	modified := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if c == "unknown" && s.Value != "" {
				c = s.Value
				if len(c) > 12 {
					c = c[:12]
				}
			}
		case "vcs.time":
			if d == "unknown" && s.Value != "" {
				d = s.Value
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if modified && c != "unknown" {
		c += "-dirty"
	}
	return v, c, d
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
