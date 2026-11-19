package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	// All が true の場合、collect 時点でクラスタに残っている未使用の情報
	// (過去のリリース、過去にインストールした Operator の版、ノードにキャッシュされた
	// 未使用イメージ) も対象に含める。false の場合は使用中のものだけを対象にする。
	All bool
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
	PastReleases    []ReleaseEntry  `json:"pastReleases"`
	UnusedImages    []string        `json:"unusedImages"`
	CatalogSources  []CatalogSource `json:"catalogSources"`
}

// ReleaseEntry は ClusterVersion の履歴に残る過去のリリース
type ReleaseEntry struct {
	Version string `json:"version"`
	Image   string `json:"image"`
}

// PastVersion は InstallPlan に残る、過去にインストールした Operator の版 (古い順)
type PastVersion struct {
	CSV     string `json:"csv"`
	Version string `json:"version"`
	Channel string `json:"channel"`
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
	PastVersions  []PastVersion  `json:"pastVersions"`
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
	dataFile := filepath.Join(outputDir, "collected-data.json")
	raw, err := os.ReadFile(dataFile)
	if err != nil {
		return nil, fmt.Errorf("collected-data.json の読み込みに失敗: %w", err)
	}

	var data CollectedData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("collected-data.json のパースに失敗: %w", err)
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
			pkg.Channels = g.operatorChannels(op)
			if op.DefaultChannel != "" && op.DefaultChannel != op.Channel {
				if g.opts.PinOperators {
					// defaultChannel の head を余分にミラーしないよう、
					// フィルタ済みカタログの defaultChannel を Subscription のチャネルにする
					pkg.DefaultChannel = op.Channel
				} else if !hasChannel(pkg.Channels, op.DefaultChannel) {
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
				Channels: g.platformChannels(channel),
				Graph:    true,
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
	if g.opts.All {
		for _, img := range g.data.UnusedImages {
			additionalSet[img] = true
		}
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

// operatorChannels は Operator のチャネル指定を返す。
//
// Subscription のチャネルは現行の版を minVersion にする (--pin-operators では maxVersion も)。
// --all では、InstallPlan に残る過去の版も含むよう範囲を広げる:
//   - Subscription のチャネル → 現行の版と過去の版のうち最も低い版を minVersion
//     (--pin-operators では最も高い版を maxVersion) にする
//   - 別のチャネルにある過去の版 → そのチャネルを、過去の版の最低〜最高の範囲で追加する
//
// 範囲はインストール順ではなくバージョンの大小で決める (ダウングレードを含む履歴でも
// minVersion > maxVersion にならないように)。
func (g *Generator) operatorChannels(op OperatorInfo) []PkgChannelRef {
	cur := operatorVersion(op)
	ch := PkgChannelRef{Name: op.Channel}
	if cur != "" {
		ch.MinVersion = cur
		if g.opts.PinOperators {
			ch.MaxVersion = cur
		}
	}
	channels := []PkgChannelRef{ch}
	if !g.opts.All || cur == "" {
		return channels
	}

	// 過去の版をチャネルごとにまとめ、バージョンの大小で下限・上限を求める
	type versionRange struct{ min, max string }
	widen := func(r *versionRange, v string) {
		if compareVersions(v, r.min) < 0 {
			r.min = v
		}
		if compareVersions(v, r.max) > 0 {
			r.max = v
		}
	}
	ranges := map[string]*versionRange{op.Channel: {min: cur, max: cur}}
	var order []string
	for _, pv := range op.PastVersions {
		if pv.Version == "" {
			continue
		}
		if pv.Channel == "" {
			fmt.Printf("    WARN: %s %s はカタログに存在しないためミラーできません\n", op.PackageName, pv.Version)
			continue
		}
		r, ok := ranges[pv.Channel]
		if !ok {
			ranges[pv.Channel] = &versionRange{min: pv.Version, max: pv.Version}
			order = append(order, pv.Channel)
			continue
		}
		widen(r, pv.Version)
	}

	cr := ranges[op.Channel]
	channels[0].MinVersion = cr.min
	if g.opts.PinOperators {
		channels[0].MaxVersion = cr.max
	}
	for _, name := range order {
		r := ranges[name]
		channels = append(channels, PkgChannelRef{Name: name, MinVersion: r.min, MaxVersion: r.max})
	}
	return channels
}

func hasChannel(channels []PkgChannelRef, name string) bool {
	for _, ch := range channels {
		if ch.Name == name {
			return true
		}
	}
	return false
}

// platformChannels は platform のチャネル指定を返す。デフォルトは現在の版のみ。
// --all では ClusterVersion の履歴に残る過去のリリースも、マイナーバージョンごとの
// チャネル (例: stable-4.20) に min/max で含める。現在のマイナーは現在のチャネルを使う。
func (g *Generator) platformChannels(channel string) []ChannelConfig {
	cur := ChannelConfig{Name: channel, MinVersion: g.data.OCPVersion, MaxVersion: g.data.OCPVersion}
	if !g.opts.All || len(g.data.PastReleases) == 0 {
		return []ChannelConfig{cur}
	}

	prefix := "stable"
	if i := strings.LastIndex(channel, "-"); i > 0 {
		prefix = channel[:i]
	}
	curMinor := minorOf(g.data.OCPVersion)

	byMinor := map[string]*ChannelConfig{curMinor: &cur}
	order := []string{}
	for _, r := range g.data.PastReleases {
		minor := minorOf(r.Version)
		if minor == "" {
			continue
		}
		c, ok := byMinor[minor]
		if !ok {
			c = &ChannelConfig{Name: prefix + "-" + minor, MinVersion: r.Version, MaxVersion: r.Version}
			byMinor[minor] = c
			order = append(order, minor)
			continue
		}
		if compareVersions(r.Version, c.MinVersion) < 0 {
			c.MinVersion = r.Version
		}
		if compareVersions(r.Version, c.MaxVersion) > 0 {
			c.MaxVersion = r.Version
		}
	}
	sort.Slice(order, func(i, j int) bool { return compareVersions(order[i], order[j]) < 0 })

	var channels []ChannelConfig
	for _, minor := range order {
		channels = append(channels, *byMinor[minor])
	}
	return append(channels, *byMinor[curMinor])
}

// minorOf は "4.22.21" から "4.22" を返す
func minorOf(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// compareVersions は semver 形式のバージョンを比較する。
// 数値部分 (major.minor.patch...) を比べ、同じならプレリリース部分を semver の規則で比べる
// (プレリリース無し > あり、識別子は数値なら数値、それ以外は文字列として比較)。
// ビルドメタデータ (+ 以降) は無視する。先頭の "v" は許容する。
func compareVersions(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	if c := compareNumbers(versionNumbers(a), versionNumbers(b)); c != 0 {
		return c
	}
	return comparePrerelease(prereleaseOf(a), prereleaseOf(b))
}

func compareNumbers(pa, pb []int) int {
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func prereleaseOf(v string) string {
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	if i := strings.Index(v, "-"); i >= 0 {
		return v[i+1:]
	}
	return ""
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	ia, ib := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		na, errA := strconv.Atoi(ia[i])
		nb, errB := strconv.Atoi(ib[i])
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case errA == nil:
			return -1 // 数値の識別子は文字列より小さい
		case errB == nil:
			return 1
		default:
			if c := strings.Compare(ia[i], ib[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(ia) < len(ib):
		return -1
	case len(ia) > len(ib):
		return 1
	}
	return 0
}

func versionNumbers(v string) []int {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var nums []int
	for _, p := range strings.Split(v, ".") {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		nums = append(nums, n)
	}
	return nums
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
