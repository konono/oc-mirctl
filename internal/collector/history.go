package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// このファイルは「collect 時点でクラスタに残っているが、現在は使われていない」
// 情報 (過去の OCP リリース、過去にインストールした Operator の版、ノードに
// キャッシュされた未使用イメージ) を収集する。generate / list は --all のときだけ
// これらを対象に含める (ダウングレードへの備え)。

// ReleaseEntry は ClusterVersion の status.history に残る過去のリリース
type ReleaseEntry struct {
	Version string `json:"version"`
	Image   string `json:"image"`
	State   string `json:"state,omitempty"`
}

// PastVersion は InstallPlan に残る、過去にインストールした Operator の版
type PastVersion struct {
	CSV     string `json:"csv"`
	Version string `json:"version"`
	// Channel はこの版を含むチャネル (同じカタログの PackageManifest から判定)。
	// Subscription のチャネルに含まれていればそれを優先する。
	// カタログから既に消えている場合は空。
	Channel     string `json:"channel,omitempty"`
	InstalledAt string `json:"installedAt"`
}

// packageChannels は PackageManifest のチャネル構成 (キー: srcNS/src/package)
type packageChannels struct {
	defaultChannel string
	// channel 名 → その channel に含まれる CSV 名の集合
	entries map[string]map[string]bool
}

// getPackageChannels は全 PackageManifest を読み、カタログ単位のチャネル構成を返す。
// 同名パッケージが複数の CatalogSource に存在しうるため、
// catalogSourceNamespace/catalogSource/name をキーにする。
func (c *Collector) getPackageChannels(ctx context.Context) (map[string]*packageChannels, error) {
	gvr := schema.GroupVersionResource{Group: "packages.operators.coreos.com", Version: "v1", Resource: "packagemanifests"}
	pmList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("PackageManifest の取得に失敗: %w", err)
	}

	result := map[string]*packageChannels{}
	for _, pm := range pmList.Items {
		src, _, _ := unstructured.NestedString(pm.Object, "status", "catalogSource")
		srcNS, _, _ := unstructured.NestedString(pm.Object, "status", "catalogSourceNamespace")
		defaultCh, _, _ := unstructured.NestedString(pm.Object, "status", "defaultChannel")
		pc := &packageChannels{defaultChannel: defaultCh, entries: map[string]map[string]bool{}}

		channels, _, _ := unstructured.NestedSlice(pm.Object, "status", "channels")
		for _, ch := range channels {
			chMap, ok := ch.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := chMap["name"].(string)
			if name == "" {
				continue
			}
			set := map[string]bool{}
			if entries, ok := chMap["entries"].([]interface{}); ok {
				for _, e := range entries {
					if eMap, ok := e.(map[string]interface{}); ok {
						if n, _ := eMap["name"].(string); n != "" {
							set[n] = true
						}
					}
				}
			}
			if head, _ := chMap["currentCSV"].(string); head != "" {
				set[head] = true
			}
			pc.entries[name] = set
		}
		result[packageKey(srcNS, src, pm.GetName())] = pc
	}
	return result, nil
}

func packageKey(srcNS, src, pkg string) string {
	return srcNS + "/" + src + "/" + pkg
}

// channelFor は CSV を含むチャネルを返す。preferred (Subscription のチャネル) に
// 含まれていればそれを、次に defaultChannel を、それ以外は名前順で最初のチャネルを返す。
func (pc *packageChannels) channelFor(csv, preferred string) string {
	if pc == nil {
		return ""
	}
	if pc.entries[preferred][csv] {
		return preferred
	}
	if pc.entries[pc.defaultChannel][csv] {
		return pc.defaultChannel
	}
	names := make([]string, 0, len(pc.entries))
	for name := range pc.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if pc.entries[name][csv] {
			return name
		}
	}
	return ""
}

// getPastReleases は ClusterVersion の status.history から、現在の版以外のリリースを
// 古い順に返す。
func (c *Collector) getPastReleases(ctx context.Context, currentVersion string) ([]ReleaseEntry, error) {
	gvr := schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	cv, err := c.client.Resource(gvr).Get(ctx, "version", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("ClusterVersion の取得に失敗: %w", err)
	}
	history, _, _ := unstructured.NestedSlice(cv.Object, "status", "history")
	return pastReleasesFromHistory(history, currentVersion), nil
}

// pastReleasesFromHistory は status.history (新しい順) から現在の版以外を古い順に返す
func pastReleasesFromHistory(history []interface{}, currentVersion string) []ReleaseEntry {
	seen := map[string]bool{currentVersion: true}
	var past []ReleaseEntry
	for i := len(history) - 1; i >= 0; i-- {
		entry, ok := history[i].(map[string]interface{})
		if !ok {
			continue
		}
		version, _ := entry["version"].(string)
		image, _ := entry["image"].(string)
		state, _ := entry["state"].(string)
		if version == "" || image == "" || seen[version] {
			continue
		}
		seen[version] = true
		past = append(past, ReleaseEntry{Version: version, Image: image, State: state})
	}
	return past
}

// enrichWithPastVersions は InstallPlan から、各 Operator が過去にインストールした版
// (現在の CSV 以外) を古い順に記録する。InstallPlan は OLM により古いものから
// 削除されるため、残っている範囲の履歴になる。
func (c *Collector) enrichWithPastVersions(ctx context.Context, operators []OperatorInfo, pkgChannels map[string]*packageChannels) error {
	gvr := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "installplans"}
	ipList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("InstallPlan の取得に失敗: %w", err)
	}

	type installed struct {
		csv, pkg, version, srcNS, src, at string
	}
	// namespace → InstallPlan に記録された bundle
	byNamespace := map[string][]installed{}
	for _, ip := range ipList.Items {
		phase, _, _ := unstructured.NestedString(ip.Object, "status", "phase")
		if phase != "Complete" {
			continue
		}
		at := ip.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")
		lookups, _, _ := unstructured.NestedSlice(ip.Object, "status", "bundleLookups")
		for _, l := range lookups {
			lMap, ok := l.(map[string]interface{})
			if !ok {
				continue
			}
			csv, _ := lMap["identifier"].(string)
			props, _ := lMap["properties"].(string)
			pkg, version := packageFromProperties(props)
			if csv == "" || pkg == "" {
				continue
			}
			var srcNS, src string
			if ref, ok := lMap["catalogSourceRef"].(map[string]interface{}); ok {
				src, _ = ref["name"].(string)
				srcNS, _ = ref["namespace"].(string)
			}
			byNamespace[ip.GetNamespace()] = append(byNamespace[ip.GetNamespace()],
				installed{csv: csv, pkg: pkg, version: version, srcNS: srcNS, src: src, at: at})
		}
	}

	for i := range operators {
		op := &operators[i]
		ns := op.Namespace
		seen := map[string]bool{op.Version: true}
		var past []PastVersion
		for _, b := range byNamespace[ns] {
			if b.pkg != op.PackageName || seen[b.csv] {
				continue
			}
			// 別カタログから入れた同名パッケージは対象外
			if b.src != "" && (b.src != op.CatalogSource || b.srcNS != op.CatalogNS) {
				continue
			}
			seen[b.csv] = true
			pc := pkgChannels[packageKey(op.CatalogNS, op.CatalogSource, op.PackageName)]
			past = append(past, PastVersion{
				CSV:         b.csv,
				Version:     b.version,
				Channel:     pc.channelFor(b.csv, op.Channel),
				InstalledAt: b.at,
			})
		}
		sort.SliceStable(past, func(a, b int) bool { return past[a].InstalledAt < past[b].InstalledAt })
		op.PastVersions = past
	}
	return nil
}

// packageFromProperties は bundleLookups[].properties (JSON 文字列) から
// olm.package のパッケージ名とバージョンを取り出す
func packageFromProperties(props string) (pkg, version string) {
	if props == "" {
		return "", ""
	}
	var parsed struct {
		Properties []struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(props), &parsed); err != nil {
		return "", ""
	}
	for _, p := range parsed.Properties {
		if p.Type != "olm.package" {
			continue
		}
		var v struct {
			PackageName string `json:"packageName"`
			Version     string `json:"version"`
		}
		if err := json.Unmarshal(p.Value, &v); err == nil {
			return v.PackageName, v.Version
		}
	}
	return "", ""
}

// nodeImage はノードの status.images の 1 エントリ (同じイメージの別名を含む)
type nodeImage struct {
	names []string
}

// ref はミラー対象として使う参照を返す (digest 形式を優先)
func (n nodeImage) ref() string {
	for _, name := range n.names {
		if strings.Contains(name, "@sha256:") {
			return name
		}
	}
	for _, name := range n.names {
		if hasTagOrDigest(name) {
			return name
		}
	}
	return ""
}

// getNodeImages は全ノードの status.images を返す。kubelet はノードごとにサイズの
// 大きい順で最大 50 件 (kubelet の nodeStatusMaxImages の既定値) しか報告しないため、小さい
// イメージは含まれないことがある。この一覧を使うのは --all の未使用イメージだけで、
// 使用中・待機中のイメージは Pod やワークロードの定義から取るので影響しない。
func (c *Collector) getNodeImages(ctx context.Context) ([]nodeImage, error) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}
	nodeList, err := c.client.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Node の取得に失敗: %w", err)
	}
	var images []nodeImage
	for _, node := range nodeList.Items {
		list, _, _ := unstructured.NestedSlice(node.Object, "status", "images")
		for _, img := range list {
			iMap, ok := img.(map[string]interface{})
			if !ok {
				continue
			}
			var names []string
			if ns, ok := iMap["names"].([]interface{}); ok {
				for _, n := range ns {
					if s, _ := n.(string); s != "" {
						names = append(names, s)
					}
				}
			}
			if len(names) > 0 {
				images = append(images, nodeImage{names: names})
			}
		}
	}
	return images, nil
}

// detectUnusedImages はノードにキャッシュされたイメージのうち、使用中のもの
// (Pod / relatedImages / CatalogSource / 待機中のワークロード) とリリース (現在・過去) に含まれるものを
// 除いた参照を返す。過去のリリースのイメージは --all で platform としてミラーされるため除く。
func detectUnusedImages(nodeImages []nodeImage, result *Result, platformImages []string) []string {
	used := map[string]bool{}
	add := func(img string) {
		if img == "" {
			return
		}
		used[img] = true
		// tag+digest 形式は digest のみの形でも照合する
		used[normalizeImageRef(img)] = true
	}
	for _, img := range result.PodImages {
		add(img)
	}
	for _, op := range result.Operators {
		for _, ri := range op.RelatedImages {
			add(ri.Image)
		}
	}
	for _, cs := range result.CatalogSources {
		add(cs.Image)
	}
	// 待機中のワークロードのイメージは「今の定義」が参照しているので未使用ではない
	for _, ii := range result.IdleImages {
		add(ii.Image)
	}
	add(result.ReleaseImage)
	for _, r := range result.PastReleases {
		add(r.Image)
	}
	for _, img := range platformImages {
		add(img)
	}

	set := map[string]bool{}
	for _, ni := range nodeImages {
		covered := false
		for _, name := range ni.names {
			if used[name] || used[normalizeImageRef(name)] {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		ref := ni.ref()
		if ref == "" || isInternalRegistry(registryFromImage(ref)) {
			continue
		}
		set[ref] = true
	}

	unused := make([]string, 0, len(set))
	for img := range set {
		unused = append(unused, img)
	}
	sort.Strings(unused)
	return unused
}
