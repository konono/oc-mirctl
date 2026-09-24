package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nict-ocpai/oc-mirctl/internal/collector"
	"github.com/spf13/cobra"
)

var (
	excludeReason string
	excludeName   string
	excludeMatch  string
)

var excludeCmd = &cobra.Command{
	Use:   "exclude <operator|image> [name]",
	Short: "Operator またはイメージを mirror 対象から除外",
	Long: `指定した Operator またはイメージを除外リストに追加する。
generate 時に ImageSetConfiguration から除外される。

Operator の除外:
  oc-mirctl exclude operator odf-operator --reason "顧客環境ではストレージ別途"

イメージの除外 (フル参照):
  oc-mirctl exclude image "vllm/vllm-openai:v0.28.0" --reason "ラボ検証用"

イメージの除外 (CSV の name で指定):
  oc-mirctl exclude image --name "odh_workbench_jupyter_pytorch_rocm_py312_image" --reason "ROCm不要"

イメージの除外 (パターンマッチで一括):
  oc-mirctl exclude image --match "*rocm*" --reason "ROCm不要"
  oc-mirctl exclude image --match "*gaudi*" --reason "Gaudi不要"`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		kind := args[0]
		if kind != "operator" && kind != "image" {
			return fmt.Errorf("第1引数は 'operator' または 'image' を指定してください")
		}

		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		excl, err := collector.LoadExclusions(dir)
		if err != nil {
			return err
		}

		data, err := collector.LoadPrevious(dir)
		if err != nil {
			return fmt.Errorf("collected-data.json の読み込みに失敗: %w", err)
		}

		switch kind {
		case "operator":
			if len(args) < 2 {
				return fmt.Errorf("operator 名を指定してください")
			}
			name := args[1]
			catalog := ""
			for _, op := range data.Operators {
				if op.PackageName == name {
					catalog = op.CatalogSource
					break
				}
			}
			if excl.ExcludeOperator(name, catalog, excludeReason) {
				fmt.Printf("除外: operator '%s'\n", name)
			} else {
				fmt.Printf("operator '%s' は既に除外リストに含まれています\n", name)
			}

		case "image":
			if excludeMatch != "" {
				if err := excludeByMatch(data, excl, excludeMatch); err != nil {
					return err
				}
			} else if excludeName != "" {
				if err := excludeByName(data, excl, excludeName); err != nil {
					return err
				}
			} else {
				if len(args) < 2 {
					return fmt.Errorf("イメージ参照、--name、または --match を指定してください")
				}
				imageRef := args[1]
				if excl.ExcludeImage(imageRef, excludeReason) {
					fmt.Printf("除外: image '%s'\n", imageRef)
				} else {
					fmt.Printf("image '%s' は既に除外リストに含まれています\n", imageRef)
				}
			}
		}

		if excludeReason != "" {
			fmt.Printf("  reason: %s\n", excludeReason)
		}

		return excl.Save(dir)
	},
}

func excludeByName(data *collector.Result, excl *collector.Exclusions, name string) error {
	count := 0
	for _, op := range data.Operators {
		for _, ri := range op.RelatedImages {
			if ri.Name == name {
				if excl.ExcludeImage(ri.Image, excludeReason) {
					repo, tag, digest := splitImageRef(ri.Image)
					ref := repo
					if tag != "" {
						ref += ":" + tag
					}
					if digest != "" {
						ref += " (" + digest + ")"
					}
					fmt.Printf("除外: %s → %s\n", name, ref)
					count++
				}
			}
		}
	}
	if count == 0 {
		return fmt.Errorf("name '%s' に一致するイメージが見つかりません", name)
	}
	return nil
}

func excludeByMatch(data *collector.Result, excl *collector.Exclusions, pattern string) error {
	count := 0
	alreadyExcluded := 0

	// relatedImages の name と image の両方でマッチ
	for _, op := range data.Operators {
		for _, ri := range op.RelatedImages {
			nameMatch, _ := filepath.Match(pattern, ri.Name)
			imageMatch, _ := filepath.Match(pattern, ri.Image)
			baseName := ri.Image
			if idx := strings.LastIndex(baseName, "/"); idx >= 0 {
				baseName = baseName[idx+1:]
			}
			baseMatch, _ := filepath.Match(pattern, baseName)

			if nameMatch || imageMatch || baseMatch {
				if excl.ExcludeImage(ri.Image, excludeReason) {
					repo, tag, _ := splitImageRef(ri.Image)
					label := ri.Name
					if label == "" {
						label = repo
					}
					ref := repo
					if tag != "" {
						ref += ":" + tag
					}
					fmt.Printf("除外: %s (%s)\n", label, ref)
					count++
				} else {
					alreadyExcluded++
				}
			}
		}
	}

	// customImages でもマッチ
	for _, img := range data.CustomImages {
		imageMatch, _ := filepath.Match(pattern, img)
		baseName := img
		if idx := strings.LastIndex(baseName, "/"); idx >= 0 {
			baseName = baseName[idx+1:]
		}
		baseMatch, _ := filepath.Match(pattern, baseName)

		if imageMatch || baseMatch {
			if excl.ExcludeImage(img, excludeReason) {
				repo, tag, _ := splitImageRef(img)
				ref := repo
				if tag != "" {
					ref += ":" + tag
				}
				fmt.Printf("除外: %s\n", ref)
				count++
			} else {
				alreadyExcluded++
			}
		}
	}

	if count == 0 && alreadyExcluded == 0 {
		return fmt.Errorf("パターン '%s' に一致するイメージが見つかりません", pattern)
	}
	if count == 0 {
		fmt.Printf("パターン '%s' に一致する %d イメージは既に除外済みです\n", pattern, alreadyExcluded)
	} else {
		fmt.Printf("\n%d イメージを除外しました\n", count)
	}
	return nil
}

var includeCmd = &cobra.Command{
	Use:   "include <operator|image> [name]",
	Short: "除外リストから Operator またはイメージを復帰",
	Long: `除外リストから Operator またはイメージを復帰する。

  oc-mirctl include operator odf-operator
  oc-mirctl include image --match "*rocm*"`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		kind := args[0]
		if kind != "operator" && kind != "image" {
			return fmt.Errorf("第1引数は 'operator' または 'image' を指定してください")
		}

		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		excl, err := collector.LoadExclusions(dir)
		if err != nil {
			return err
		}

		switch kind {
		case "operator":
			if len(args) < 2 {
				return fmt.Errorf("operator 名を指定してください")
			}
			if excl.IncludeOperator(args[1]) {
				fmt.Printf("復帰: operator '%s'\n", args[1])
			} else {
				fmt.Printf("operator '%s' は除外リストに含まれていません\n", args[1])
			}

		case "image":
			if excludeMatch != "" {
				if err := includeByMatch(excl, excludeMatch); err != nil {
					return err
				}
			} else {
				if len(args) < 2 {
					return fmt.Errorf("イメージ参照または --match を指定してください")
				}
				if excl.IncludeImage(args[1]) {
					fmt.Printf("復帰: image '%s'\n", args[1])
				} else {
					fmt.Printf("image '%s' は除外リストに含まれていません\n", args[1])
				}
			}
		}

		return excl.Save(dir)
	},
}

func includeByMatch(excl *collector.Exclusions, pattern string) error {
	count := 0
	// 除外リストの Images を走査してパターンマッチ
	var remaining []collector.ExclusionEntry
	for _, e := range excl.Images {
		nameMatch, _ := filepath.Match(pattern, e.Name)
		baseName := e.Name
		if idx := strings.LastIndex(baseName, "/"); idx >= 0 {
			baseName = baseName[idx+1:]
		}
		baseMatch, _ := filepath.Match(pattern, baseName)

		if nameMatch || baseMatch {
			repo, tag, _ := splitImageRef(e.Name)
			ref := repo
			if tag != "" {
				ref += ":" + tag
			}
			fmt.Printf("復帰: %s\n", ref)
			count++
		} else {
			remaining = append(remaining, e)
		}
	}
	excl.Images = remaining

	if count == 0 {
		return fmt.Errorf("パターン '%s' に一致する除外イメージが見つかりません", pattern)
	}
	fmt.Printf("\n%d イメージを復帰しました\n", count)
	return nil
}

var excludeListCmd = &cobra.Command{
	Use:   "exclude-list",
	Short: "除外リストを表示",
	Long:  `現在の除外リスト (exclusions.json) の内容を表示する。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		excl, err := collector.LoadExclusions(dir)
		if err != nil {
			return err
		}

		if len(excl.Operators) == 0 && len(excl.Images) == 0 {
			fmt.Println("除外リストは空です")
			return nil
		}

		if len(excl.Operators) > 0 {
			fmt.Printf("Excluded Operators (%d):\n", len(excl.Operators))
			for _, op := range excl.Operators {
				reason := ""
				if op.Reason != "" {
					reason = " — " + op.Reason
				}
				fmt.Printf("  ✗ %s%s\n", op.Name, reason)
			}
			fmt.Println()
		}

		if len(excl.Images) > 0 {
			fmt.Printf("Excluded Images (%d):\n", len(excl.Images))
			for _, img := range excl.Images {
				repo, tag, digest := splitImageRef(img.Name)
				ref := repo
				if tag != "" {
					ref += ":" + tag
				}
				if digest != "" {
					ref += " (" + digest + ")"
				}
				reason := ""
				if img.Reason != "" {
					reason = " — " + img.Reason
				}
				fmt.Printf("  ✗ %s%s\n", ref, reason)
			}
		}

		return nil
	},
}

func init() {
	excludeCmd.Flags().StringVar(&excludeReason, "reason", "", "除外理由")
	excludeCmd.Flags().StringVar(&excludeName, "name", "", "CSV の relatedImages name で除外")
	excludeCmd.Flags().StringVar(&excludeMatch, "match", "", "パターンマッチで一括除外 (例: *rocm*)")

	includeCmd.Flags().StringVar(&excludeMatch, "match", "", "パターンマッチで一括復帰 (例: *rocm*)")

	rootCmd.AddCommand(excludeCmd)
	rootCmd.AddCommand(includeCmd)
	rootCmd.AddCommand(excludeListCmd)
}
