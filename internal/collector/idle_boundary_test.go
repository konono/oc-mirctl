package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/nict-ocpai/oc-mirctl/internal/generator"
)

// fakeDynamic は List だけを実装した dynamic.Interface (テスト用)。
// client-go の dynamic/fake は追加の依存モジュールが必要になるため使わない。
// 実装していないメソッドを呼ぶと埋め込みの nil インターフェースで panic する。
type fakeDynamic struct {
	dynamic.Interface
	objects map[schema.GroupVersionResource][]unstructured.Unstructured
}

func (f *fakeDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &fakeResource{items: f.objects[gvr]}
}

type fakeResource struct {
	dynamic.NamespaceableResourceInterface
	items []unstructured.Unstructured
}

func (r *fakeResource) Namespace(string) dynamic.ResourceInterface { return r }

func (r *fakeResource) List(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return &unstructured.UnstructuredList{Items: r.items}, nil
}

func pod(name, phase, image string, owner string) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": name, "namespace": "app"},
		"spec": map[string]interface{}{
			"containers": []interface{}{map[string]interface{}{"name": "c", "image": image}},
		},
		"status": map[string]interface{}{"phase": phase},
	}
	if owner != "" {
		obj["metadata"].(map[string]interface{})["ownerReferences"] = []interface{}{
			map[string]interface{}{"kind": "Job", "name": owner, "apiVersion": "batch/v1", "uid": "u"},
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

func workload(apiVersion, kind, name string, templatePath []string, image string) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]interface{}{"name": name, "namespace": "app"},
	}
	podSpec := map[string]interface{}{
		"containers": []interface{}{map[string]interface{}{"name": "c", "image": image}},
	}
	if err := unstructured.SetNestedField(obj, podSpec, templatePath...); err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: obj}
}

// 完了済みの Job Pod が残っている状態の CronJob / Job (次回実行待ち) のイメージは、
// 使用中ではなく待機中として扱われ、generate のデフォルトでは含まれ、
// --running-only では外れること (rev 指摘の本番経路テスト)。
func TestCompletedJobPodIsIdleNotRunning(t *testing.T) {
	const (
		batchImage   = "quay.io/example/batch:1.0"
		runningImage = "quay.io/example/web:2.0"
		pendingImage = "quay.io/example/starting:3.0"
	)
	gvr := func(group, version, resource string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
	}
	client := &fakeDynamic{objects: map[schema.GroupVersionResource][]unstructured.Unstructured{
		gvr("", "v1", "pods"): {
			*pod("web-1", "Running", runningImage, ""),
			*pod("starting-1", "Pending", pendingImage, ""),
			*pod("nightly-28000000-abcde", "Succeeded", batchImage, "nightly-28000000"),
			*pod("migrate-xyz", "Failed", batchImage, "migrate"),
		},
		gvr("batch", "v1", "cronjobs"): {
			*workload("batch/v1", "CronJob", "nightly", []string{"spec", "jobTemplate", "spec", "template", "spec"}, batchImage),
		},
		gvr("batch", "v1", "jobs"): {
			*workload("batch/v1", "Job", "nightly-28000000", []string{"spec", "template", "spec"}, batchImage),
			*workload("batch/v1", "Job", "migrate", []string{"spec", "template", "spec"}, batchImage),
		},
		gvr("apps", "v1", "deployments"): {
			*workload("apps/v1", "Deployment", "web", []string{"spec", "template", "spec"}, runningImage),
		},
	}}
	c := &Collector{client: client}
	ctx := context.Background()

	podImages, err := c.getPodImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inUse := map[string]bool{}
	for _, img := range podImages {
		inUse[img] = true
	}
	if !inUse[runningImage] || !inUse[pendingImage] {
		t.Errorf("Running / Pending pod images must be in use, got %v", podImages)
	}
	if inUse[batchImage] || c.podSpecImages[batchImage] {
		t.Errorf("completed pod image must not be in use, got PodImages=%v podSpecImages=%v", podImages, c.podSpecImages)
	}

	workloadImages, err := c.getWorkloadImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := &Result{
		OCPVersion:   "4.22.21",
		PodImages:    podImages,
		CustomImages: filterCustomImages(podImages),
	}
	result.IdleImages = detectIdleImages(workloadImages, c.podSpecImages, result, nil)
	if len(result.IdleImages) != 1 || result.IdleImages[0].Image != batchImage {
		t.Fatalf("idle images: got %+v, want only %s", result.IdleImages, batchImage)
	}
	if w := result.IdleImages[0].Workloads; len(w) != 3 {
		t.Errorf("idle workloads: got %v, want CronJob + 2 Jobs", w)
	}

	// generate: デフォルトは含む、--running-only は外れる
	dir := t.TempDir()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "collected-data.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	additional := func(opts generator.Options) map[string]bool {
		g, err := generator.New(dir, "", opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.GenerateImageSetConfig(); err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(filepath.Join(dir, "imageset-config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg generator.ImageSetConfig
		if err := yaml.Unmarshal(out, &cfg); err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, img := range cfg.Mirror.AdditionalImages {
			m[img.Name] = true
		}
		return m
	}
	if got := additional(generator.Options{}); !got[batchImage] || !got[runningImage] {
		t.Errorf("default: want batch and running images, got %v", got)
	}
	if got := additional(generator.Options{RunningOnly: true}); got[batchImage] || !got[runningImage] {
		t.Errorf("running-only: want running image only, got %v", got)
	}
}
