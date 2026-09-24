package applier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

type Applier struct {
	client     dynamic.Interface
	resultsDir string
	dryRun     bool
}

var (
	idmsGVR = schema.GroupVersionResource{
		Group: "config.openshift.io", Version: "v1", Resource: "imagedigestmirrorsets",
	}
	itmsGVR = schema.GroupVersionResource{
		Group: "config.openshift.io", Version: "v1", Resource: "imagetagmirrorsets",
	}
	catalogSourceGVR = schema.GroupVersionResource{
		Group: "operators.coreos.com", Version: "v1alpha1", Resource: "catalogsources",
	}
)

func New(kubeconfig, resultsDir string, dryRun bool) (*Applier, error) {
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig の読み込みに失敗: %w", err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("クライアント作成に失敗: %w", err)
	}

	if _, err := os.Stat(resultsDir); err != nil {
		return nil, fmt.Errorf("results ディレクトリが見つかりません: %s", resultsDir)
	}

	return &Applier{client: client, resultsDir: resultsDir, dryRun: dryRun}, nil
}

func (a *Applier) Apply() error {
	ctx := context.Background()

	files, err := filepath.Glob(filepath.Join(a.resultsDir, "*.yaml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("results ディレクトリに YAML ファイルが見つかりません: %s", a.resultsDir)
	}

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("ファイル読み込み失敗 %s: %w", f, err)
		}

		// 複数ドキュメント対応
		docs := strings.Split(string(raw), "\n---\n")
		for _, doc := range docs {
			doc = strings.TrimSpace(doc)
			if doc == "" {
				continue
			}
			if err := a.applyDocument(ctx, []byte(doc), filepath.Base(f)); err != nil {
				fmt.Fprintf(os.Stderr, "WARN: %s の適用に失敗: %v\n", filepath.Base(f), err)
			}
		}
	}

	return nil
}

func (a *Applier) applyDocument(ctx context.Context, raw []byte, filename string) error {
	var obj unstructured.Unstructured
	if err := yaml.Unmarshal(raw, &obj.Object); err != nil {
		return fmt.Errorf("YAML パース失敗: %w", err)
	}

	kind := obj.GetKind()
	name := obj.GetName()

	switch kind {
	case "ImageDigestMirrorSet":
		return a.mergeIDMS(ctx, &obj, filename)
	case "ImageTagMirrorSet":
		return a.mergeITMS(ctx, &obj, filename)
	case "CatalogSource":
		return a.applyCatalogSource(ctx, &obj, filename)
	default:
		fmt.Printf("  SKIP: %s/%s (未対応の kind)\n", kind, name)
		return nil
	}
}

// mergeIDMS は既存の IDMS エントリとマージする（上書きしない）
func (a *Applier) mergeIDMS(ctx context.Context, newObj *unstructured.Unstructured, filename string) error {
	name := newObj.GetName()

	newMirrors, _, _ := unstructured.NestedSlice(newObj.Object, "spec", "imageDigestMirrors")

	existing, err := a.client.Resource(idmsGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// 存在しない → 新規作成
		fmt.Printf("  CREATE: ImageDigestMirrorSet/%s (%d entries)\n", name, len(newMirrors))
		if a.dryRun {
			return nil
		}
		_, err := a.client.Resource(idmsGVR).Create(ctx, newObj, metav1.CreateOptions{})
		return err
	}

	// 既存あり → マージ
	existingMirrors, _, _ := unstructured.NestedSlice(existing.Object, "spec", "imageDigestMirrors")

	// 既存の source をインデックス化
	existingSources := map[string]bool{}
	for _, m := range existingMirrors {
		if entry, ok := m.(map[string]interface{}); ok {
			if src, ok := entry["source"].(string); ok {
				existingSources[src] = true
			}
		}
	}

	// 新規エントリのうち、既存にない source だけ追加
	added := 0
	for _, m := range newMirrors {
		if entry, ok := m.(map[string]interface{}); ok {
			if src, ok := entry["source"].(string); ok {
				if !existingSources[src] {
					existingMirrors = append(existingMirrors, m)
					existingSources[src] = true
					added++
				}
			}
		}
	}

	if added == 0 {
		fmt.Printf("  NOOP:   ImageDigestMirrorSet/%s (全エントリが既に存在)\n", name)
		return nil
	}

	fmt.Printf("  MERGE:  ImageDigestMirrorSet/%s (+%d entries, 既存 %d を保持)\n",
		name, added, len(existingMirrors)-added)

	if a.dryRun {
		return nil
	}

	if err := unstructured.SetNestedSlice(existing.Object, existingMirrors, "spec", "imageDigestMirrors"); err != nil {
		return err
	}
	_, err = a.client.Resource(idmsGVR).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// mergeITMS は IDMS と同じマージロジック
func (a *Applier) mergeITMS(ctx context.Context, newObj *unstructured.Unstructured, filename string) error {
	name := newObj.GetName()

	newMirrors, _, _ := unstructured.NestedSlice(newObj.Object, "spec", "imageTagMirrors")

	existing, err := a.client.Resource(itmsGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("  CREATE: ImageTagMirrorSet/%s (%d entries)\n", name, len(newMirrors))
		if a.dryRun {
			return nil
		}
		_, err := a.client.Resource(itmsGVR).Create(ctx, newObj, metav1.CreateOptions{})
		return err
	}

	existingMirrors, _, _ := unstructured.NestedSlice(existing.Object, "spec", "imageTagMirrors")

	existingSources := map[string]bool{}
	for _, m := range existingMirrors {
		if entry, ok := m.(map[string]interface{}); ok {
			if src, ok := entry["source"].(string); ok {
				existingSources[src] = true
			}
		}
	}

	added := 0
	for _, m := range newMirrors {
		if entry, ok := m.(map[string]interface{}); ok {
			if src, ok := entry["source"].(string); ok {
				if !existingSources[src] {
					existingMirrors = append(existingMirrors, m)
					existingSources[src] = true
					added++
				}
			}
		}
	}

	if added == 0 {
		fmt.Printf("  NOOP:   ImageTagMirrorSet/%s (全エントリが既に存在)\n", name)
		return nil
	}

	fmt.Printf("  MERGE:  ImageTagMirrorSet/%s (+%d entries, 既存 %d を保持)\n",
		name, added, len(existingMirrors)-added)

	if a.dryRun {
		return nil
	}

	if err := unstructured.SetNestedSlice(existing.Object, existingMirrors, "spec", "imageTagMirrors"); err != nil {
		return err
	}
	_, err = a.client.Resource(itmsGVR).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (a *Applier) applyCatalogSource(ctx context.Context, newObj *unstructured.Unstructured, filename string) error {
	name := newObj.GetName()
	ns := newObj.GetNamespace()
	if ns == "" {
		ns = "openshift-marketplace"
	}

	existing, err := a.client.Resource(catalogSourceGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("  CREATE: CatalogSource/%s (ns=%s)\n", name, ns)
		if a.dryRun {
			return nil
		}
		newObj.SetNamespace(ns)
		_, err := a.client.Resource(catalogSourceGVR).Namespace(ns).Create(ctx, newObj, metav1.CreateOptions{})
		return err
	}

	// 既存あり → image を比較して変更があれば更新
	existingImage, _, _ := unstructured.NestedString(existing.Object, "spec", "image")
	newImage, _, _ := unstructured.NestedString(newObj.Object, "spec", "image")

	if existingImage == newImage {
		fmt.Printf("  NOOP:   CatalogSource/%s (image 変更なし)\n", name)
		return nil
	}

	fmt.Printf("  UPDATE: CatalogSource/%s (image: %s → %s)\n", name, existingImage, newImage)
	if a.dryRun {
		return nil
	}

	if err := unstructured.SetNestedField(existing.Object, newImage, "spec", "image"); err != nil {
		return err
	}
	_, err = a.client.Resource(catalogSourceGVR).Namespace(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}
