package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/konono/oc-mirctl/internal/collector"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var (
	showExcluded    bool
	listAll         bool
	listRunningOnly bool
	outputFormat string
	authFile     string
	concurrency  int
)

type ListOutput struct {
	Cluster          ClusterInfo        `json:"cluster" yaml:"cluster"`
	Platform         PlatformInfo       `json:"platform" yaml:"platform"`
	Operators        []ListOperatorInfo `json:"operators" yaml:"operators"`
	Images           []ListImageInfo    `json:"images" yaml:"images"`
	UncoveredImages  []ListImageInfo    `json:"uncoveredImages,omitempty" yaml:"uncoveredImages,omitempty"`
	// IdleImages は待機中のワークロードが参照するイメージ (--running-only のときは出力しない)
	IdleImages       []ListIdleImage    `json:"idleImages,omitempty" yaml:"idleImages,omitempty"`
	// 以下は --all のときだけ出力する (collect 時点でクラスタに残っている未使用の情報)
	PastReleases     []string           `json:"pastReleases,omitempty" yaml:"pastReleases,omitempty"`
	UnusedImages     []ListImageInfo    `json:"unusedImages,omitempty" yaml:"unusedImages,omitempty"`
	Summary          SummaryInfo        `json:"summary" yaml:"summary"`
}

type ListIdleImage struct {
	ListImageInfo `json:",inline" yaml:",inline"`
	Workloads     []string `json:"workloads" yaml:"workloads"`
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
	PastVersions  []string        `json:"pastVersions,omitempty" yaml:"pastVersions,omitempty"`
}

type ListImageInfo struct {
	Name          string `json:"name" yaml:"name"`
	ImageName     string `json:"imageName,omitempty" yaml:"imageName,omitempty"`
	ShortName     string `json:"shortName,omitempty" yaml:"shortName,omitempty"`
	Size          string `json:"size,omitempty" yaml:"size,omitempty"`
	SizeBytes     int64  `json:"sizeBytes,omitempty" yaml:"sizeBytes,omitempty"`
	Excluded      bool   `json:"excluded,omitempty" yaml:"excluded,omitempty"`
	ExcludeReason string `json:"excludeReason,omitempty" yaml:"excludeReason,omitempty"`
	Private       bool   `json:"private,omitempty" yaml:"private,omitempty"`
}

type SummaryInfo struct {
	ActiveOperators        int   `json:"activeOperators" yaml:"activeOperators"`
	ExcludedOperators      int   `json:"excludedOperators" yaml:"excludedOperators"`
	ActiveRelatedImages    int   `json:"activeRelatedImages" yaml:"activeRelatedImages"`
	ExcludedRelatedImages  int   `json:"excludedRelatedImages" yaml:"excludedRelatedImages"`
	ActiveImages           int   `json:"activeImages" yaml:"activeImages"`
	ExcludedImages         int   `json:"excludedImages" yaml:"excludedImages"`
	UncoveredImages        int   `json:"uncoveredImages" yaml:"uncoveredImages"`
	PlatformSizeBytes      int64 `json:"platformSizeBytes" yaml:"platformSizeBytes"`
	OperatorSizeBytes      int64 `json:"operatorSizeBytes" yaml:"operatorSizeBytes"`
	ExcludedSizeBytes      int64 `json:"excludedSizeBytes" yaml:"excludedSizeBytes"`
	CustomImageSizeBytes   int64 `json:"customImageSizeBytes" yaml:"customImageSizeBytes"`
	UncoveredSizeBytes     int64 `json:"uncoveredSizeBytes" yaml:"uncoveredSizeBytes"`
	IdleSizeBytes          int64 `json:"idleSizeBytes" yaml:"idleSizeBytes"`
	UnusedSizeBytes        int64 `json:"unusedSizeBytes,omitempty" yaml:"unusedSizeBytes,omitempty"`
	// UniqueRelatedImages は有効な relatedImages を重複 (同じイメージの別名) を除いて数えた件数
	UniqueRelatedImages    int   `json:"uniqueRelatedImages" yaml:"uniqueRelatedImages"`
	// 各 *SizeBytes と TotalSizeBytes は、同じイメージを複数の箇所で数えないよう、
	// Operators → Images → Uncovered → Idle → Unused の順に初出の 1 回だけ計上する
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

デフォルトでは collect 時点で使用中のものと、待機中のワークロード (Pod が動いていない
Deployment / CronJob / DaemonSet 等) が参照するイメージを表示する。

  --running-only  待機中のワークロードのイメージを除き、動いている Pod のイメージだけを表示
  --all           未使用の過去のリリース / Operator の版 / キャッシュ済みイメージも表示
  -o json   JSON 形式で出力
  -o yaml   YAML 形式で出力`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveOutputDir()
		if err != nil {
			return err
		}

		if listAll && listRunningOnly {
			return fmt.Errorf("--all と --running-only は同時に指定できません (--all は待機中のワークロードのイメージも含む全体です)")
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

		output := buildListOutput(data, excl, imageSizes, listAll, listRunningOnly)

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

func buildListOutput(data *collector.Result, excl *collector.Exclusions, sizes map[string]int64, all, runningOnly bool) *ListOutput {
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

	// 合計は同じイメージを 1 回だけ数える。relatedImages には同じイメージが別名で複数回
	// 現れることがあり (例: vllm の *_fast_1_image / *_fast_2_image)、Operator の
	// relatedImages と Pod のイメージ (Custom Images) も重なるため。
	// サイズは参照ごとではなくイメージ (imageKey) ごとに集約する。同じイメージの参照の
	// 一部だけサイズ取得に失敗していても (例: repo:tag@digest は未取得、repo@digest は取得済み)、
	// どの参照が先に数えられても取得できたサイズを使う。
	keySize := map[string]int64{}
	for ref, s := range sizes {
		if k := imageKey(ref); s > keySize[k] {
			keySize[k] = s
		}
	}
	sizeOf := func(img string) int64 {
		return keySize[imageKey(img)]
	}
	counted := map[string]bool{}
	excludedCounted := map[string]bool{}
	countOnce := func(set map[string]bool, img string) int64 {
		k := imageKey(img)
		if set[k] {
			return 0
		}
		set[k] = true
		return sizeOf(img)
	}
	uniqueRelated := map[string]bool{}

	for _, op := range data.Operators {
		info := ListOperatorInfo{
			PackageName:   op.PackageName,
			Channel:       op.Channel,
			CatalogSource: op.CatalogSource,
			LastSeen:      op.LastSeen,
		}
		if all {
			for _, pv := range op.PastVersions {
				ch := pv.Channel
				if ch == "" {
					ch = "not in catalog"
				}
				info.PastVersions = append(info.PastVersions, pv.Version+" ("+ch+")")
			}
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
		opSeen := map[string]bool{} // Operator 内の重複を除いて Operator のサイズを求める
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
				output.Summary.ExcludedSizeBytes += countOnce(excludedCounted, ri.Image)
			} else {
				activeCount++
				output.Summary.ActiveRelatedImages++
				if k := imageKey(ri.Image); !opSeen[k] {
					opSeen[k] = true
					total += sizeOf(ri.Image)
				}
				if !info.Excluded {
					uniqueRelated[imageKey(ri.Image)] = true
					output.Summary.OperatorSizeBytes += countOnce(counted, ri.Image)
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
		}

		output.Operators = append(output.Operators, info)
	}

	for _, img := range data.CustomImages {
		info := ListImageInfo{
			Name:      img,
			ShortName: shortenImage(img),
			Private:   data.IsPrivateImage(img),
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
				if !info.Excluded {
					output.Summary.CustomImageSizeBytes += countOnce(counted, img)
				}
			}
		}

		output.Images = append(output.Images, info)
	}

	// Uncovered Images
	for _, img := range data.UncoveredImages {
		info := ListImageInfo{
			Name:      img,
			ShortName: shortenImage(img),
			Private:   data.IsPrivateImage(img),
		}
		if sizes != nil {
			if s, ok := sizes[img]; ok {
				info.SizeBytes = s
				info.Size = collector.FormatSize(s)
				output.Summary.UncoveredSizeBytes += countOnce(counted, img)
			}
		}
		output.UncoveredImages = append(output.UncoveredImages, info)
		output.Summary.UncoveredImages++
	}

	if !runningOnly {
		for _, ii := range data.IdleImages {
			info := ListIdleImage{
				ListImageInfo: ListImageInfo{Name: ii.Image, ShortName: shortenImage(ii.Image)},
				Workloads:     ii.Workloads,
			}
			if excluded, reason := excl.IsImageExcluded(ii.Image); excluded {
				info.Excluded = true
				info.ExcludeReason = reason
			}
			if s, ok := sizes[ii.Image]; ok {
				info.SizeBytes = s
				info.Size = collector.FormatSize(s)
			}
			if !info.Excluded {
				output.Summary.IdleSizeBytes += countOnce(counted, ii.Image)
			}
			output.IdleImages = append(output.IdleImages, info)
		}
	}

	if all {
		for _, r := range data.PastReleases {
			output.PastReleases = append(output.PastReleases, r.Version)
		}
		for _, img := range data.UnusedImages {
			info := ListImageInfo{Name: img, ShortName: shortenImage(img)}
			if s, ok := sizes[img]; ok {
				info.SizeBytes = s
				info.Size = collector.FormatSize(s)
			}
			if excluded, reason := excl.IsImageExcluded(img); excluded {
				info.Excluded = true
				info.ExcludeReason = reason
			} else {
				output.Summary.UnusedSizeBytes += countOnce(counted, img)
			}
			output.UnusedImages = append(output.UnusedImages, info)
		}
	}

	output.Summary.PlatformSizeBytes = data.PlatformImageSize
	output.Summary.UniqueRelatedImages = len(uniqueRelated)
	output.Summary.TotalSizeBytes = output.Summary.PlatformSizeBytes + output.Summary.OperatorSizeBytes +
		output.Summary.CustomImageSizeBytes + output.Summary.UncoveredSizeBytes +
		output.Summary.IdleSizeBytes + output.Summary.UnusedSizeBytes

	return output
}

// imageKey はサイズの重複判定用のキーを返す。repo:tag@digest は repo@digest と同じイメージ
// として扱う (同じ digest をタグ付き / タグなしで参照していても 1 つと数える)。
func imageKey(img string) string {
	at := strings.Index(img, "@sha256:")
	if at < 0 {
		return img
	}
	repo := img[:at]
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo + img[at:]
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
	if len(output.PastReleases) > 0 {
		fmt.Printf("  過去のリリース (--all): %s\n", strings.Join(output.PastReleases, ", "))
	}
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
			if len(op.PastVersions) > 0 && !(op.Excluded && !showExcluded) {
				fmt.Printf("  %s の過去の版 (--all): %s\n", op.PackageName, strings.Join(op.PastVersions, ", "))
			}
		}

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
	related := fmt.Sprintf("%d", output.Summary.ActiveRelatedImages)
	if output.Summary.UniqueRelatedImages != output.Summary.ActiveRelatedImages {
		related += fmt.Sprintf(" (重複を除くと %d)", output.Summary.UniqueRelatedImages)
	}
	if output.Summary.ExcludedRelatedImages > 0 {
		fmt.Printf("  relatedImages: %s active (%s), %d excluded (%s)\n",
			related, strings.TrimSpace(collector.FormatSize(output.Summary.OperatorSizeBytes)),
			output.Summary.ExcludedRelatedImages, strings.TrimSpace(collector.FormatSize(output.Summary.ExcludedSizeBytes)))
	} else {
		fmt.Printf("  relatedImages: %s (%s)\n",
			related, strings.TrimSpace(collector.FormatSize(output.Summary.OperatorSizeBytes)))
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
		} else if img.Private {
			st = "🔒"
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

	// Uncovered Images
	if len(output.UncoveredImages) > 0 {
		fmt.Printf("\n⚠ Uncovered Images (%d) — platform/operator でカバーされていないイメージ:\n", len(output.UncoveredImages))
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

		for _, img := range output.UncoveredImages {
			st := ""
			if img.Private {
				st = "🔒"
			}
			imgRepo, imgTag, imgDigest := splitImageRef(img.Name)
			sz := ""
			if img.Size != "" {
				sz = strings.TrimSpace(img.Size)
			}
			tw.Append([]string{st, imgRepo, imgTag, imgDigest, sz})
		}
		tw.Render()
		fmt.Println("  → generate で additionalImages に自動追加されます")
	}

	if len(output.IdleImages) > 0 {
		fmt.Printf("\nIdle Workload Images (%d) — Pod は動いていないがワークロード定義が参照するイメージ:\n", len(output.IdleImages))
		tw = tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"", "Repository", "Tag", "Digest", "Size", "Workloads"})
		tw.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})
		tw.SetAutoWrapText(false)
		for _, img := range output.IdleImages {
			if img.Excluded && !showExcluded {
				continue
			}
			st := ""
			if img.Excluded {
				st = "✗"
			}
			imgRepo, imgTag, imgDigest := splitImageRef(img.Name)
			tw.Append([]string{st, imgRepo, imgTag, imgDigest, strings.TrimSpace(img.Size), strings.Join(img.Workloads, ", ")})
		}
		tw.Render()
		fmt.Println("  → generate で additionalImages に追加されます (--running-only で除外)")
	}

	if len(output.UnusedImages) > 0 {
		fmt.Printf("\nUnused Images (%d) — ノードにキャッシュされた未使用イメージ (--all):\n", len(output.UnusedImages))
		tw = tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"Repository", "Tag", "Digest", "Size"})
		tw.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})
		tw.SetAutoWrapText(false)
		for _, img := range output.UnusedImages {
			imgRepo, imgTag, imgDigest := splitImageRef(img.Name)
			tw.Append([]string{imgRepo, imgTag, imgDigest, strings.TrimSpace(img.Size)})
		}
		tw.Render()
		fmt.Println("  → generate --all で additionalImages に追加されます")
	}

	if output.Summary.ExcludedSizeBytes > 0 {
		fmt.Printf("\nExcluded: %s (%d images) — oc-mirctl exclude-list で確認\n",
			collector.FormatSize(output.Summary.ExcludedSizeBytes), output.Summary.ExcludedRelatedImages)
	}

	fmt.Printf("\nTotal estimated size:\n")
	if output.Summary.PlatformSizeBytes > 0 {
		fmt.Printf("  Platform:    %s\n", collector.FormatSize(output.Summary.PlatformSizeBytes))
	}
	fmt.Printf("  Operators:   %s\n", collector.FormatSize(output.Summary.OperatorSizeBytes))
	fmt.Printf("  Images:      %s\n", collector.FormatSize(output.Summary.CustomImageSizeBytes))
	if output.Summary.UncoveredSizeBytes > 0 {
		fmt.Printf("  Uncovered:   %s\n", collector.FormatSize(output.Summary.UncoveredSizeBytes))
	}
	if output.Summary.IdleSizeBytes > 0 {
		fmt.Printf("  Idle:        %s\n", collector.FormatSize(output.Summary.IdleSizeBytes))
	}
	if output.Summary.UnusedSizeBytes > 0 {
		fmt.Printf("  Unused:      %s\n", collector.FormatSize(output.Summary.UnusedSizeBytes))
	}
	fmt.Printf("  ─────────────────\n")
	fmt.Printf("  Total:       %s\n", collector.FormatSize(output.Summary.TotalSizeBytes))

}

func init() {
	listCmd.Flags().BoolVar(&showExcluded, "show-excluded", false, "除外済みの項目も表示する")
	listCmd.Flags().BoolVar(&listRunningOnly, "running-only", false, "待機中のワークロードのイメージを除き、動いている Pod のイメージだけを表示する")
	listCmd.Flags().BoolVar(&listAll, "all", false, "collect 時点でクラスタに残っている未使用の情報 (過去のリリース / Operator の版 / キャッシュ済みイメージ) も表示する (--running-only とは併用不可)")
	listCmd.Flags().StringVarP(&outputFormat, "output", "o", "", "出力形式 (json, yaml)")
	listCmd.Flags().StringVar(&authFile, "authfile", "", "レジストリ認証ファイル (default: 自動検出)")
	listCmd.Flags().IntVar(&concurrency, "concurrency", 50, "サイズ取得の並列数")
	rootCmd.AddCommand(listCmd)
}
