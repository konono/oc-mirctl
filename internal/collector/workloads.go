package collector

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// このファイルは、ワークロード定義 (Deployment 等) の Pod テンプレートが参照しているが、
// collect 時点で動いている Pod が使っていないイメージ (待機中のワークロードのイメージ) を
// 収集する。例: replicas=0 の Deployment、次回実行を待つ CronJob、対象ノードの無い
// DaemonSet。これらは今の定義の一部なので、generate / list はデフォルトで対象に含める
// (--running-only で除外できる)。ミラーしないと、disconnected 環境でスケールアップや
// ノード追加をした時点で ImagePullBackOff になる。

// IdleImage は待機中のワークロードが参照するイメージと、その参照元
type IdleImage struct {
	Image string `json:"image"`
	// Workloads は参照元 ("Kind namespace/name")
	Workloads []string `json:"workloads"`
}

// workloadKind は Pod テンプレートを持つワークロードの種類
type workloadKind struct {
	kind     string
	gvr      schema.GroupVersionResource
	template []string // Pod spec までのフィールドパス
}

var workloadKinds = []workloadKind{
	{"Deployment", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, []string{"spec", "template", "spec"}},
	{"StatefulSet", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, []string{"spec", "template", "spec"}},
	{"DaemonSet", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, []string{"spec", "template", "spec"}},
	{"Job", schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, []string{"spec", "template", "spec"}},
	{"CronJob", schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, []string{"spec", "jobTemplate", "spec", "template", "spec"}},
	{"DeploymentConfig", schema.GroupVersionResource{Group: "apps.openshift.io", Version: "v1", Resource: "deploymentconfigs"}, []string{"spec", "template", "spec"}},
}

// getWorkloadImages はワークロード定義の Pod テンプレートが参照するイメージ (書かれたままの
// 参照) と参照元を返す。
func (c *Collector) getWorkloadImages(ctx context.Context) (map[string][]string, error) {
	images := map[string][]string{}
	for _, wk := range workloadKinds {
		list, err := c.client.Resource(wk.gvr).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil {
			// DeploymentConfig の API が無いクラスタ等はスキップする
			if errors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("%s の取得に失敗: %w", wk.kind, err)
		}
		for _, item := range list.Items {
			spec, found, _ := unstructured.NestedMap(item.Object, wk.template...)
			if !found {
				continue
			}
			ref := wk.kind + " " + item.GetNamespace() + "/" + item.GetName()
			for _, img := range templateImages(spec) {
				images[img] = append(images[img], ref)
			}
		}
	}
	return images, nil
}

// templateImages は Pod spec の containers / initContainers のイメージ参照を書かれたまま返す
func templateImages(spec map[string]interface{}) []string {
	var images []string
	for _, field := range []string{"containers", "initContainers"} {
		containers, _, _ := unstructured.NestedSlice(spec, field)
		for _, c := range containers {
			if cMap, ok := c.(map[string]interface{}); ok {
				if img, _ := cMap["image"].(string); img != "" {
					images = append(images, img)
				}
			}
		}
	}
	return images
}

// rawPodImages は Pod の spec に書かれたイメージ参照を解決せずに返す
func rawPodImages(pod map[string]interface{}) []string {
	spec, _, _ := unstructured.NestedMap(pod, "spec")
	return templateImages(spec)
}

// detectIdleImages は、ワークロード定義が参照するイメージのうち、動いている Pod が
// 使っておらず、platform / Operator の relatedImages / CatalogSource でもカバーされない
// ものを返す (generate で additionalImages に入る対象)。内部レジストリのイメージは除く。
//
// workloadImages のキーは書かれたままの参照。動いている Pod の spec に同じ文字列があれば
// 使用中とみなす (タグなし参照は Pod 側では digest に解決されるため、解決後の値では
// 一致しない)。タグも digest も無い参照は、実行中の digest が分からないので ":latest" を補う。
func detectIdleImages(workloadImages map[string][]string, podSpecImages map[string]bool, result *Result, platformImages []string) []IdleImage {
	covered := map[string]bool{}
	add := func(img string) {
		if img != "" {
			covered[img] = true
			covered[normalizeImageRef(img)] = true
		}
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
	add(result.ReleaseImage)
	for _, img := range platformImages {
		add(img)
	}

	merged := map[string][]string{}
	for raw, refs := range workloadImages {
		if podSpecImages[raw] {
			continue
		}
		img := resolvePodImageRef(raw, "")
		if covered[img] || covered[normalizeImageRef(img)] {
			continue
		}
		merged[img] = append(merged[img], refs...)
	}

	var idle []IdleImage
	for img, refs := range merged {
		if isInternalRegistry(registryFromImage(img)) {
			continue
		}
		sorted := append([]string(nil), refs...)
		sort.Strings(sorted)
		idle = append(idle, IdleImage{Image: img, Workloads: dedupSorted(sorted)})
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].Image < idle[j].Image })
	return idle
}

func dedupSorted(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
