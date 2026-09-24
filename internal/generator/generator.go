package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	data           *CollectedData
	exclusions     *Exclusions
}

type CollectedData struct {
	OCPVersion     string          `json:"ocpVersion"`
	OCPChannel     string          `json:"ocpChannel"`
	ReleaseImage   string          `json:"releaseImage"`
	Operators      []OperatorInfo  `json:"operators"`
	CustomImages   []string        `json:"customImages"`
	CatalogSources []CatalogSource `json:"catalogSources"`
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
	Name     string          `json:"name"`
	Channels []PkgChannelRef `json:"channels,omitempty"`
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

func New(outputDir, mirrorRegistry string) (*Generator, error) {
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
			channelSet := map[string]bool{op.Channel: true}
			ch := PkgChannelRef{Name: op.Channel}
			if op.Version != "" {
				ch.MinVersion = extractVersion(op.Version, op.PackageName)
			}
			pkg.Channels = []PkgChannelRef{ch}
			if op.DefaultChannel != "" && !channelSet[op.DefaultChannel] {
				pkg.Channels = append(pkg.Channels, PkgChannelRef{Name: op.DefaultChannel})
			}
		}
		catalogPackages[op.CatalogSource] = append(catalogPackages[op.CatalogSource], pkg)
	}
	if skipped > 0 {
		fmt.Printf("    %d operators excluded\n", skipped)
	}

	var operatorCatalogs []OperatorCatalog
	for catName, pkgs := range catalogPackages {
		catImage := catalogImages[catName]
		if catImage == "" {
			parts := strings.SplitN(g.data.OCPVersion, ".", 3)
			minor := "4.22"
			if len(parts) >= 2 {
				minor = parts[0] + "." + parts[1]
			}
			catImage = "registry.redhat.io/redhat/redhat-operator-index:v" + minor
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

	// カスタムイメージ + relatedImages から additionalImages を構築
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
	for img := range additionalSet {
		if excludedImgs[img] {
			continue
		}
		config.Mirror.AdditionalImages = append(config.Mirror.AdditionalImages, ImageRef{Name: img})
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
