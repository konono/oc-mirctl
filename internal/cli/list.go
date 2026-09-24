package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nict-ocpai/oc-mirctl/internal/collector"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var (
	showExcluded bool
	outputFormat string
	authFile     string
	concurrency  int
)

type ListOutput struct {
	Cluster   ClusterInfo        `json:"cluster" yaml:"cluster"`
	Platform  PlatformInfo       `json:"platform" yaml:"platform"`
	Operators []ListOperatorInfo `json:"operators" yaml:"operators"`
	Images    []ListImageInfo    `json:"images" yaml:"images"`
	Summary   SummaryInfo        `json:"summary" yaml:"summary"`
}

type ClusterInfo struct {
	Name        string `json:"name" yaml:"name"`
	OCPVersion  string `json:"ocpVersion" yaml:"ocpVersion"`
	CollectedAt string `json:"collectedAt" yaml:"collectedAt"`
}

type PlatformInfo struct {
	Version      string `json:"version" yaml:"version"`
	Channel      string `json:"channel" yaml:"channel"`
	ReleaseImage string `json:"releaseImage" yaml:"releaseImage"`
}

type ListOperatorInfo struct {
	PackageName   string          `json:"packageName" yaml:"packageName"`
	Channel       string          `json:"channel" yaml:"channel"`
	CatalogSource string          `json:"catalogSource" yaml:"catalogSource"`
	RelatedCount  int             `json:"relatedImageCount" yaml:"relatedImageCount"`
	Size          string          `json:"size,omitempty" yaml:"size,omitempty"`
	SizeBytes     int64           `json:"sizeBytes,omitempty" yaml:"sizeBytes,omitempty"`
	Excluded      bool            `json:"excluded,omitempty" yaml:"excluded,omitempty"`
	ExcludeReason string          `json:"excludeReason,omitempty" yaml:"excludeReason,omitempty"`
	LastSeen      string          `json:"lastSeen" yaml:"lastSeen"`
	RelatedImages []ListImageInfo `json:"relatedImages,omitempty" yaml:"relatedImages,omitempty"`
}

type ListImageInfo struct {
	Name          string `json:"name" yaml:"name"`
	ImageName     string `json:"imageName,omitempty" yaml:"imageName,omitempty"`
	ShortName     string `json:"shortName,omitempty" yaml:"shortName,omitempty"`
	Size          string `json:"size,omitempty" yaml:"size,omitempty"`
	SizeBytes     int64  `json:"sizeBytes,omitempty" yaml:"sizeBytes,omitempty"`
	Excluded      bool   `json:"excluded,omitempty" yaml:"excluded,omitempty"`
	ExcludeReason string `json:"excludeReason,omitempty" yaml:"excludeReason,omitempty"`
}

type SummaryInfo struct {
	ActiveOperators        int   `json:"activeOperators" yaml:"activeOperators"`
	ExcludedOperators      int   `json:"excludedOperators" yaml:"excludedOperators"`
	ActiveRelatedImages    int   `json:"activeRelatedImages" yaml:"activeRelatedImages"`
	ExcludedRelatedImages  int   `json:"excludedRelatedImages" yaml:"excludedRelatedImages"`
	ActiveImages           int   `json:"activeImages" yaml:"activeImages"`
	ExcludedImages         int   `json:"excludedImages" yaml:"excludedImages"`
	PlatformSizeBytes      int64 `json:"platformSizeBytes" yaml:"platformSizeBytes"`
	OperatorSizeBytes      int64 `json:"operatorSizeBytes" yaml:"operatorSizeBytes"`
	ExcludedSizeBytes      int64 `json:"excludedSizeBytes" yaml:"excludedSizeBytes"`
	CustomImageSizeBytes   int64 `json:"customImageSizeBytes" yaml:"customImageSizeBytes"`
	TotalSizeBytes         int64 `json:"totalSizeBytes" yaml:"totalSizeBytes"`
}

func shortenImage(img string) string {
	if idx := strings.LastIndex(img, "@sha256:"); idx > 0 {
		return img[:idx] + "@sha256:..." + img[len(img)-8:]
	}
	return img
}

func findAuthFile(dir string) string {
	candidates := []string{
		filepath.Join(dir, "pull-secret.json"),
		filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "containers/auth.json"),
		filepath.Join(os.Getenv("HOME"), ".docker/config.json"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "収集済みの Operator / イメージを一覧表示",
	Long: `収集済みのデータを一覧表示する。
各 Operator の relatedImages とサイズを表示する。

  -o json   JSON 形式で出力
  -o yaml   YAML 形式で出力`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		data, err := collector.LoadPrevious(dir)
		if err != nil {
			return fmt.Errorf("collected-data.json の読み込みに失敗: %w", err)
		}

		excl, err := collector.LoadExclusions(dir)
		if err != nil {
			return err
		}

		var imageSizes map[string]int64
		if len(data.ImageSizes) > 0 {
			imageSizes = data.ImageSizes
			fmt.Fprintf(os.Stderr, "==> collected-data.json のサイズ情報を使用 (%d イメージ)\n", len(imageSizes))
		} else {
			af := authFile
			if af == "" {
				af = findAuthFile(dir)
			}
			if af != "" {
				fmt.Fprintf(os.Stderr, "==> 認証ファイル: %s\n", af)
			} else {
				fmt.Fprintf(os.Stderr, "==> 認証ファイルなし (pull-secret.json を %s に配置するか --authfile で指定)\n", dir)
			}

			if _, err := exec.LookPath("skopeo"); err != nil {
				return fmt.Errorf("skopeo が見つかりません。サイズ取得には skopeo が必要です\n  インストール: sudo dnf install -y skopeo  (RHEL/Fedora)\n              sudo apt install -y skopeo   (Debian/Ubuntu)\n              brew install skopeo            (macOS)")
			}

			allImages := data.AllImages()
			fmt.Fprintf(os.Stderr, "==> %d イメージのサイズを取得中 (並列=%d)...\n", len(allImages), concurrency)
			imageSizes = collector.FetchSizes(allImages, concurrency, af)
		}

		output := buildListOutput(data, excl, imageSizes)

		switch outputFormat {
		case "json":
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(output)
		case "yaml":
			out, err := yaml.Marshal(output)
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			return nil
		default:
			printHumanList(output)
			return nil
		}
	},
}

func buildListOutput(data *collector.Result, excl *collector.Exclusions, sizes map[string]int64) *ListOutput {
	channel := data.OCPChannel
	if channel == "" {
		parts := strings.SplitN(data.OCPVersion, ".", 3)
		if len(parts) >= 2 {
			channel = "stable-" + parts[0] + "." + parts[1]
		}
	}

	output := &ListOutput{
		Cluster: ClusterInfo{
			Name:        data.ClusterName,
			OCPVersion:  data.OCPVersion,
			CollectedAt: data.CollectedAt,
		},
		Platform: PlatformInfo{
			Version:      data.OCPVersion,
			Channel:      channel,
			ReleaseImage: data.ReleaseImage,
		},
	}

	for _, op := range data.Operators {
		info := ListOperatorInfo{
			PackageName:   op.PackageName,
			Channel:       op.Channel,
			CatalogSource: op.CatalogSource,
			LastSeen:      op.LastSeen,
		}

		excluded, reason := excl.IsOperatorExcluded(op.PackageName)
		if excluded {
			info.Excluded = true
			info.ExcludeReason = reason
			output.Summary.ExcludedOperators++
		} else {
			output.Summary.ActiveOperators++
		}

		var total int64
		activeCount := 0
		for _, ri := range op.RelatedImages {
			imgInfo := ListImageInfo{
				Name:      ri.Image,
				ShortName: shortenImage(ri.Image),
				ImageName: ri.Name,
			}

			imgExcluded, imgReason := excl.IsImageExcluded(ri.Image)
			if imgExcluded {
				imgInfo.Excluded = true
				imgInfo.ExcludeReason = imgReason
				output.Summary.ExcludedRelatedImages++
				if sizes != nil {
					if s, ok := sizes[ri.Image]; ok && s > 0 {
						output.Summary.ExcludedSizeBytes += s
					}
				}
			} else {
				activeCount++
				output.Summary.ActiveRelatedImages++
				if sizes != nil {
					if s, ok := sizes[ri.Image]; ok && s > 0 {
						total += s
					}
				}
			}

			if sizes != nil {
				if s, ok := sizes[ri.Image]; ok {
					imgInfo.SizeBytes = s
					imgInfo.Size = collector.FormatSize(s)
				}
			}
			info.RelatedImages = append(info.RelatedImages, imgInfo)
		}
		info.RelatedCount = activeCount
		if total > 0 {
			info.SizeBytes = total
			info.Size = collector.FormatSize(total)
			if !info.Excluded {
				output.Summary.OperatorSizeBytes += total
			}
		}

		output.Operators = append(output.Operators, info)
	}

	for _, img := range data.CustomImages {
		info := ListImageInfo{
			Name:      img,
			ShortName: shortenImage(img),
		}

		excluded, reason := excl.IsImageExcluded(img)
		if excluded {
			info.Excluded = true
			info.ExcludeReason = reason
			output.Summary.ExcludedImages++
		} else {
			output.Summary.ActiveImages++
		}

		if sizes != nil {
			if s, ok := sizes[img]; ok {
				info.SizeBytes = s
				info.Size = collector.FormatSize(s)
				if !info.Excluded && s > 0 {
					output.Summary.CustomImageSizeBytes += s
				}
			}
		}

		output.Images = append(output.Images, info)
	}

	output.Summary.PlatformSizeBytes = data.PlatformImageSize
	output.Summary.TotalSizeBytes = output.Summary.PlatformSizeBytes + output.Summary.OperatorSizeBytes + output.Summary.CustomImageSizeBytes

	return output
}

// splitImageRef はイメージ参照を registry/repo と tag/digest に分離する
func splitImageRef(image string) (repo, tag, digest string) {
	if atIdx := strings.Index(image, "@sha256:"); atIdx > 0 {
		repo = image[:atIdx]
		digest = "sha256:..." + image[len(image)-8:]
		if colonIdx := strings.LastIndex(repo, ":"); colonIdx > 0 {
			afterColon := repo[colonIdx+1:]
			if !strings.Contains(afterColon, "/") {
				tag = afterColon
				repo = repo[:colonIdx]
			}
		}
		return repo, tag, digest
	}
	if colonIdx := strings.LastIndex(image, ":"); colonIdx > 0 {
		afterColon := image[colonIdx+1:]
		if !strings.Contains(afterColon, "/") {
			return image[:colonIdx], afterColon, ""
		}
	}
	return image, "latest", ""
}

func printHumanList(output *ListOutput) {
	fmt.Printf("Cluster: %s  (OCP %s)\n", output.Cluster.Name, output.Cluster.OCPVersion)
	fmt.Printf("Collected: %s\n\n", output.Cluster.CollectedAt)

	// Platform table
	tw := tablewriter.NewWriter(os.Stdout)
	tw.SetHeader([]string{"Platform", "Channel", "Release Image", "Size"})
	tw.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})
	tw.SetAutoWrapText(false)
	tw.SetColumnAlignment([]int{
		tablewriter.ALIGN_LEFT,
		tablewriter.ALIGN_LEFT,
		tablewriter.ALIGN_LEFT,
		tablewriter.ALIGN_RIGHT,
	})
	repo, tag, digest := splitImageRef(output.Platform.ReleaseImage)
	releaseRef := tag
	if digest != "" {
		releaseRef = digest
	}
	platformSize := ""
	if output.Summary.PlatformSizeBytes > 0 {
		platformSize = strings.TrimSpace(collector.FormatSize(output.Summary.PlatformSizeBytes))
	}
	tw.Append([]string{"OCP " + output.Platform.Version, output.Platform.Channel, repo + " " + releaseRef, platformSize})
	tw.Render()
	fmt.Println()

	// Operators — カタログ別にテーブルを分ける
	catalogs := map[string][]ListOperatorInfo{}
	catalogOrder := []string{}
	for _, op := range output.Operators {
		cat := op.CatalogSource
		if _, exists := catalogs[cat]; !exists {
			catalogOrder = append(catalogOrder, cat)
		}
		catalogs[cat] = append(catalogs[cat], op)
	}

	for _, catName := range catalogOrder {
		ops := catalogs[catName]

		fmt.Printf("Catalog: %s\n", catName)

		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"", "Operator", "Channel", "Images", "Size"})
		tw.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})
		tw.SetAutoWrapText(false)
		tw.SetColumnAlignment([]int{
			tablewriter.ALIGN_CENTER,
			tablewriter.ALIGN_LEFT,
			tablewriter.ALIGN_LEFT,
			tablewriter.ALIGN_RIGHT,
			tablewriter.ALIGN_RIGHT,
		})

		for _, op := range ops {
			if op.Excluded && !showExcluded {
				continue
			}

			st := ""
			name := op.PackageName
			if op.Excluded {
				st = "✗"
				name += " [EXCLUDED: " + op.ExcludeReason + "]"
			}

			imgs := fmt.Sprintf("%d", op.RelatedCount)

			sz := ""
			if op.Size != "" {
				sz = strings.TrimSpace(op.Size)
			}
			tw.Append([]string{st, name, op.Channel, imgs, sz})
		}

		tw.Render()

		for _, op := range ops {
			if op.Excluded && !showExcluded {
				continue
			}
			if len(op.RelatedImages) == 0 {
				continue
			}

			fmt.Printf("  └─ %s relatedImages:\n", op.PackageName)

			itw := tablewriter.NewWriter(os.Stdout)
			itw.SetHeader([]string{"", "Name", "Repository", "Tag", "Digest", "Size"})
			itw.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})
			itw.SetAutoWrapText(false)
			itw.SetColumnAlignment([]int{
				tablewriter.ALIGN_CENTER,
				tablewriter.ALIGN_LEFT,
				tablewriter.ALIGN_LEFT,
				tablewriter.ALIGN_LEFT,
				tablewriter.ALIGN_RIGHT,
			})

			for _, img := range op.RelatedImages {
				if img.Excluded && !showExcluded {
					continue
				}

				ist := ""
				if img.Excluded {
					ist = "✗"
				}

				label := img.ImageName
				if label == "" {
					label = "-"
				}

				imgRepo, imgTag, imgDigest := splitImageRef(img.Name)

				sz := ""
				if img.Size != "" {
					sz = strings.TrimSpace(img.Size)
				}

				itw.Append([]string{ist, label, imgRepo, imgTag, imgDigest, sz})
			}

			itw.Render()
		}

		fmt.Println()
	}

	fmt.Printf("Operators: %d active, %d excluded\n", output.Summary.ActiveOperators, output.Summary.ExcludedOperators)
	if output.Summary.ExcludedRelatedImages > 0 {
		fmt.Printf("  relatedImages: %d active (%s), %d excluded (%s)\n",
			output.Summary.ActiveRelatedImages, collector.FormatSize(output.Summary.OperatorSizeBytes),
			output.Summary.ExcludedRelatedImages, collector.FormatSize(output.Summary.ExcludedSizeBytes))
	} else {
		fmt.Printf("  relatedImages: %d (%s)\n",
			output.Summary.ActiveRelatedImages, collector.FormatSize(output.Summary.OperatorSizeBytes))
	}
	fmt.Println()

	// Custom Images table
	fmt.Println("Custom Images:")
	tw = tablewriter.NewWriter(os.Stdout)
	tw.SetHeader([]string{"", "Repository", "Tag", "Digest", "Size"})
	tw.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})
	tw.SetAutoWrapText(false)
	tw.SetColumnAlignment([]int{
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_LEFT,
		tablewriter.ALIGN_LEFT,
		tablewriter.ALIGN_LEFT,
		tablewriter.ALIGN_RIGHT,
	})

	for _, img := range output.Images {
		if img.Excluded && !showExcluded {
			continue
		}

		st := ""
		if img.Excluded {
			st = "✗"
		}

		imgRepo, imgTag, imgDigest := splitImageRef(img.Name)

		sz := ""
		if img.Size != "" {
			sz = strings.TrimSpace(img.Size)
		}
		tw.Append([]string{st, imgRepo, imgTag, imgDigest, sz})
	}

	tw.Render()

	fmt.Printf("\nImages: %d active, %d excluded (%s)\n",
		output.Summary.ActiveImages, output.Summary.ExcludedImages,
		collector.FormatSize(output.Summary.CustomImageSizeBytes))

	if output.Summary.ExcludedSizeBytes > 0 {
		fmt.Printf("\nExcluded: %s (%d images) — oc-mirctl exclude-list で確認\n",
			collector.FormatSize(output.Summary.ExcludedSizeBytes), output.Summary.ExcludedRelatedImages)
	}

	fmt.Printf("\nTotal estimated size:\n")
	if output.Summary.PlatformSizeBytes > 0 {
		fmt.Printf("  Platform:  %s\n", collector.FormatSize(output.Summary.PlatformSizeBytes))
	}
	fmt.Printf("  Operators: %s\n", collector.FormatSize(output.Summary.OperatorSizeBytes))
	fmt.Printf("  Images:    %s\n", collector.FormatSize(output.Summary.CustomImageSizeBytes))
	fmt.Printf("  ─────────────────\n")
	fmt.Printf("  Total:     %s\n", collector.FormatSize(output.Summary.TotalSizeBytes))

}

func init() {
	listCmd.Flags().BoolVar(&showExcluded, "show-excluded", false, "除外済みの項目も表示する")
	listCmd.Flags().StringVarP(&outputFormat, "output", "o", "", "出力形式 (json, yaml)")
	listCmd.Flags().StringVar(&authFile, "authfile", "", "レジストリ認証ファイル (default: 自動検出)")
	listCmd.Flags().IntVar(&concurrency, "concurrency", 50, "サイズ取得の並列数")
	rootCmd.AddCommand(listCmd)
}
