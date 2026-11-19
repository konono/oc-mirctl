package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

type Exclusions struct {
	Operators []ExclusionEntry `json:"operators,omitempty"`
	Images    []ExclusionEntry `json:"images,omitempty"`
}

type ExclusionEntry struct {
	Name string `json:"name"`
}

type Generator struct {
	outputDir      string
	mirrorRegistry string
	opts           Options
	data           *CollectedData
	exclusions     *Exclusions
}

// Options は ImageSetConfiguration 生成の挙動を制御する
type Options struct {
	// DataFile は outputDir 配下の入力データファイル名
	// (current-data.json = 最新状態のみ / collected-data.json = 累積)
	DataFile string
	// PinOperators が true の場合、Operator を現行 CSV のバージョンに固定する
	// (maxVersion = minVersion)。false の場合は現行〜チャネル最新をミラーする。
	//
	// Subscription のチャネルが defaultChannel と異なる場合、true では
	// defaultChannel をミラー対象に含めず、フィルタ済みカタログの defaultChannel を
	// Subscription のチャネルで上書きする。false では従来どおり defaultChannel を
	// チャネル名のみで追加する (oc-mirror はその head 1 版をミラーする)。
	PinOperators bool
}

type CollectedData struct {
	OCPVersion     string          `json:"ocpVersion"`
	OCPChannel     string          `json:"ocpChannel"`
	ReleaseImage   string          `json:"releaseImage"`
	Operators      []OperatorInfo  `json:"operators"`
	CustomImages    []string        `json:"customImages"`
	UncoveredImages []string        `json:"uncoveredImages"`
	CatalogSources  []CatalogSource `json:"catalogSources"`
}

type RelatedImage struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type OperatorInfo struct {
	PackageName    string         `json:"packageName"`
	Channel        string         `json:"channel"`
	DefaultChannel string         `json:"defaultChannel"`
	Version        string         `json:"version"`
	BundleVersion  string         `json:"bundleVersion"`
	CatalogSource  string         `json:"catalogSource"`
	CatalogNS      string         `json:"catalogNamespace"`
	RelatedImages []RelatedImage `json:"relatedImages"`
}

type CatalogSource struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Image     string `json:"image"`
}

// ---- ImageSetConfiguration types ----

type ImageSetConfig struct {
	Kind       string       `json:"kind"`
	APIVersion string       `json:"apiVersion"`
	Mirror     MirrorConfig `json:"mirror"`
}

type MirrorConfig struct {
	Platform         PlatformConfig    `json:"platform"`
	Operators        []OperatorCatalog `json:"operators"`
	AdditionalImages []ImageRef        `json:"additionalImages,omitempty"`
}

type PlatformConfig struct {
	Channels []ChannelConfig `json:"channels"`
	Graph    bool            `json:"graph"`
}

type ChannelConfig struct {
	Name       string `json:"name"`
	MinVersion string `json:"minVersion,omitempty"`
	MaxVersion string `json:"maxVersion,omitempty"`
	Type       string `json:"type,omitempty"`
}

type OperatorCatalog struct {
	Catalog  string       `json:"catalog"`
	Packages []PkgConfig  `json:"packages"`
}

type PkgConfig struct {
	Name string `json:"name"`
	// DefaultChannel は oc-mirror が生成するフィルタ済みカタログ内の
	// defaultChannel を上書きする。フィルタ後に元の defaultChannel が
	// 残らない場合、oc-mirror は上書き指定がないとエラーにする。
	DefaultChannel string          `json:"defaultChannel,omitempty"`
	Channels       []PkgChannelRef `json:"channels,omitempty"`
}

type PkgChannelRef struct {
	Name       string `json:"name"`
	MinVersion string `json:"minVersion,omitempty"`
	MaxVersion string `json:"maxVersion,omitempty"`
}

type ImageRef struct {
	Name string `json:"name"`
}

// ---- Disconnected overrides ----

type DisconnectedOverrides struct {
	AdditionalTrustBundleFile string              `json:"additionalTrustBundleFile"`
	ImageDigestSources        []ImageDigestSource `json:"imageDigestSources"`
	MirrorRegistry            string              `json:"mirrorRegistry"`
}

type ImageDigestSource struct {
	Mirrors []string `json:"mirrors"`
	Source  string   `json:"source"`
}

func New(outputDir, mirrorRegistry string, opts Options) (*Generator, error) {
	if opts.DataFile == "" {
		opts.DataFile = "collected-data.json"
	}
	dataFile := filepath.Join(outputDir, opts.DataFile)
	raw, err := os.ReadFile(dataFile)
	if err != nil {
		return nil, fmt.Errorf("%s の読み込みに失敗: %w", opts.DataFile, err)
	}

	var data CollectedData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("%s のパースに失敗: %w", opts.DataFile, err)
	}

	// exclusions を読み込み (なければ空)
	var excl Exclusions
	exclFile := filepath.Join(outputDir, "exclusions.json")
	if raw, err := os.ReadFile(exclFile); err == nil {
		json.Unmarshal(raw, &excl)
	}

	return &Generator{
		outputDir:      outputDir,
		mirrorRegistry: mirrorRegistry,
		opts:           opts,
		data:           &data,
		exclusions:     &excl,
	}, nil
}

func (g *Generator) GenerateImageSetConfig() error {
	// Subscription の CatalogSource ごとにパッケージをグルーピング
	catalogPackages := map[string][]PkgConfig{}
	catalogImages := map[string]string{}

	for _, cs := range g.data.CatalogSources {
		catalogImages[cs.Name] = cs.Image
	}

	excludedOps := map[string]bool{}
	for _, e := range g.exclusions.Operators {
		excludedOps[e.Name] = true
	}
	excludedImgs := map[string]bool{}
	for _, e := range g.exclusions.Images {
		excludedImgs[e.Name] = true
	}

	seen := map[string]bool{}
	skipped := 0
	for _, op := range g.data.Operators {
		if excludedOps[op.PackageName] {
			skipped++
			continue
		}
		key := op.CatalogSource + "/" + op.PackageName
		if seen[key] || op.PackageName == "" {
			continue
		}
		seen[key] = true

		pkg := PkgConfig{Name: op.PackageName}
		if op.Channel != "" {
			ch := PkgChannelRef{Name: op.Channel}
			if v := operatorVersion(op); v != "" {
				ch.MinVersion = v
				if g.opts.PinOperators {
					ch.MaxVersion = v
				}
			}
			pkg.Channels = []PkgChannelRef{ch}
			if op.DefaultChannel != "" && op.DefaultChannel != op.Channel {
				if g.opts.PinOperators {
					// defaultChannel の head を余分にミラーしないよう、
					// フィルタ済みカタログの defaultChannel を Subscription のチャネルにする
					pkg.DefaultChannel = op.Channel
				} else {
					pkg.Channels = append(pkg.Channels, PkgChannelRef{Name: op.DefaultChannel})
				}
			}
		}
		catalogPackages[op.CatalogSource] = append(catalogPackages[op.CatalogSource], pkg)
	}
	if skipped > 0 {
		fmt.Printf("    %d operators excluded\n", skipped)
	}

	var operatorCatalogs []OperatorCatalog
	catNames := make([]string, 0, len(catalogPackages))
	for catName := range catalogPackages {
		catNames = append(catNames, catName)
	}
	sort.Strings(catNames)
	for _, catName := range catNames {
		pkgs := catalogPackages[catName]
		catImage := catalogImages[catName]
		if catImage == "" {
			// spec.image を持たない CatalogSource (address 指定の gRPC 等)。
			// デフォルトカタログなら名前から対応するインデックスを決める。
			img, err := defaultCatalogIndex(catName, g.data.OCPVersion)
			if err != nil {
				return err
			}
			catImage = img
		}
		operatorCatalogs = append(operatorCatalogs, OperatorCatalog{
			Catalog:  catImage,
			Packages: pkgs,
		})
	}

	// OCP チャネルの決定
	channel := g.data.OCPChannel
	if channel == "" {
		parts := strings.SplitN(g.data.OCPVersion, ".", 3)
		if len(parts) >= 2 {
			channel = "stable-" + parts[0] + "." + parts[1]
		}
	}

	config := ImageSetConfig{
		Kind:       "ImageSetConfiguration",
		APIVersion: "mirror.openshift.io/v2alpha1",
		Mirror: MirrorConfig{
			Platform: PlatformConfig{
				Channels: []ChannelConfig{{
					Name:       channel,
					MinVersion: g.data.OCPVersion,
					MaxVersion: g.data.OCPVersion,
					}},
				Graph: true,
			},
			Operators: operatorCatalogs,
		},
	}

	// カスタムイメージ + relatedImages + 未カバーイメージから additionalImages を構築
	additionalSet := map[string]bool{}
	for _, img := range g.data.CustomImages {
		additionalSet[img] = true
	}
	for _, op := range g.data.Operators {
		for _, ri := range op.RelatedImages {
			if isCustomImage(ri.Image) {
				additionalSet[ri.Image] = true
			}
		}
	}
	for _, img := range g.data.UncoveredImages {
		additionalSet[img] = true
	}
	var untagged []string
	for img := range additionalSet {
		if excludedImgs[img] {
			continue
		}
		// タグも digest も無い参照は oc-mirror が拒否する。collect は imageID の digest で
		// 解決するが、修正前に collect した累積データ (--all) には残っている場合がある。
		if !hasTagOrDigest(img) {
			untagged = append(untagged, img)
			img += ":latest"
		}
		config.Mirror.AdditionalImages = append(config.Mirror.AdditionalImages, ImageRef{Name: img})
	}
	sort.Slice(config.Mirror.AdditionalImages, func(i, j int) bool {
		return config.Mirror.AdditionalImages[i].Name < config.Mirror.AdditionalImages[j].Name
	})
	if len(untagged) > 0 {
		sort.Strings(untagged)
		fmt.Printf("    WARN: タグも digest も無いイメージに :latest を補いました (collect は実行中の digest で解決しますが、--all の累積データには以前の参照が残る場合があります):\n")
		for _, img := range untagged {
			fmt.Printf("      - %s\n", img)
		}
	}

	out, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	path := filepath.Join(g.outputDir, "imageset-config.yaml")
	fmt.Printf("==> %s を生成\n", path)
	return os.WriteFile(path, out, 0644)
}

func (g *Generator) GenerateDisconnectedOverrides() error {
	if g.mirrorRegistry == "" {
		return fmt.Errorf("--mirror-registry が必要です")
	}

	overrides := DisconnectedOverrides{
		AdditionalTrustBundleFile: filepath.Join(g.outputDir, "mirror-ca.pem"),
		MirrorRegistry:           g.mirrorRegistry,
		ImageDigestSources: []ImageDigestSource{
			{
				Mirrors: []string{g.mirrorRegistry + "/openshift/release-images"},
				Source:  "quay.io/openshift-release-dev/ocp-release",
			},
			{
				Mirrors: []string{g.mirrorRegistry + "/openshift/release"},
				Source:  "quay.io/openshift-release-dev/ocp-v4.0-art-dev",
			},
		},
	}

	out, err := yaml.Marshal(overrides)
	if err != nil {
		return err
	}

	path := filepath.Join(g.outputDir, "disconnected-overrides.yaml")
	fmt.Printf("==> %s を生成\n", path)
	return os.WriteFile(path, out, 0644)
}

// defaultCatalogIndexes は OpenShift のデフォルト CatalogSource 名とインデックスイメージの対応
var defaultCatalogIndexes = map[string]string{
	"redhat-operators":    "registry.redhat.io/redhat/redhat-operator-index",
	"certified-operators": "registry.redhat.io/redhat/certified-operator-index",
	"community-operators": "registry.redhat.io/redhat/community-operator-index",
	"redhat-marketplace":  "registry.redhat.io/redhat/redhat-marketplace-index",
}

// defaultCatalogIndex は spec.image を持たない CatalogSource のインデックスイメージを返す。
// デフォルトカタログ以外は推測せずエラーにする (誤ったカタログに割り当てると
// oc-mirror がパッケージを見つけられず、Operator がミラーされないため)。
func defaultCatalogIndex(catName, ocpVersion string) (string, error) {
	repo, ok := defaultCatalogIndexes[catName]
	if !ok {
		return "", fmt.Errorf("CatalogSource %q のインデックスイメージが不明です (spec.image が空)。"+
			"該当 Operator を exclude するか、collected-data.json の catalogSources に image を設定してください", catName)
	}
	parts := strings.SplitN(ocpVersion, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("OCP バージョン %q からカタログのタグを決められません", ocpVersion)
	}
	return repo + ":v" + parts[0] + "." + parts[1], nil
}

// operatorVersion は minVersion / maxVersion に使うバージョンを返す。
// CSV の spec.version (BundleVersion) を優先し、無い場合 (旧データ) は CSV 名から推定する。
func operatorVersion(op OperatorInfo) string {
	if op.BundleVersion != "" {
		return op.BundleVersion
	}
	if op.Version != "" {
		return extractVersion(op.Version, op.PackageName)
	}
	return ""
}

// extractVersion は CSV 名からバージョン部分を抽出する
// 例: "rhods-operator.3.5.1" → "3.5.1"
//     "authorino-operator.v1.4.3" → "v1.4.3"
//     "nfd.4.22.0-202609151747" → "4.22.0-202609151747"
func extractVersion(csvName, packageName string) string {
	prefix := packageName + "."
	if strings.HasPrefix(csvName, prefix) {
		return csvName[len(prefix):]
	}
	if idx := strings.LastIndex(csvName, ".v"); idx >= 0 {
		return csvName[idx+1:]
	}
	if idx := strings.LastIndex(csvName, "."); idx >= 0 {
		return csvName[idx+1:]
	}
	return csvName
}

// hasTagOrDigest はイメージ参照にタグか digest が含まれるかを判定する。
// レジストリの host:port を誤ってタグと判定しないよう、最後の "/" 以降のみを見る。
func hasTagOrDigest(img string) bool {
	if strings.Contains(img, "@") {
		return true
	}
	last := img
	if i := strings.LastIndex(img, "/"); i >= 0 {
		last = img[i+1:]
	}
	return strings.Contains(last, ":")
}

func isCustomImage(img string) bool {
	redhatPrefixes := []string{
		"quay.io/openshift",
		"registry.redhat.io",
		"registry.access.redhat.com",
		"image-registry.openshift-image-registry.svc",
	}
	for _, prefix := range redhatPrefixes {
		if strings.HasPrefix(img, prefix) {
			return false
		}
	}
	return true
}
