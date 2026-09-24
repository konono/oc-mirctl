package collector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

type Collector struct {
	client dynamic.Interface
}

type RelatedImage struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type OperatorInfo struct {
	Name           string         `json:"name"`
	PackageName    string         `json:"packageName"`
	Channel        string         `json:"channel"`
	DefaultChannel string         `json:"defaultChannel,omitempty"`
	CatalogSource  string         `json:"catalogSource"`
	CatalogNS      string         `json:"catalogNamespace"`
	Version        string         `json:"version"`
	RelatedImages  []RelatedImage `json:"relatedImages,omitempty"`
	LastSeen       string         `json:"lastSeen"`
}

// operatorKey は Operator のマージキー
func (o *OperatorInfo) Key() string {
	return o.CatalogSource + "/" + o.PackageName
}

type CatalogInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Image     string `json:"image"`
}

func (c *CatalogInfo) Key() string {
	return c.Namespace + "/" + c.Name
}

type Result struct {
	ClusterName    string         `json:"clusterName"`
	ClusterID      string         `json:"clusterID"`
	OCPVersion     string         `json:"ocpVersion"`
	OCPChannel     string         `json:"ocpChannel"`
	ReleaseImage   string         `json:"releaseImage"`
	Operators      []OperatorInfo `json:"operators"`
	PodImages      []string       `json:"podImages"`
	CustomImages   []string       `json:"customImages"`
	CatalogSources    []CatalogInfo    `json:"catalogSources"`
	PlatformImageSize int64            `json:"platformImageSize,omitempty"`
	ImageSizes        map[string]int64 `json:"imageSizes,omitempty"`
	CollectedAt       string           `json:"collectedAt"`
	pullSecret        []byte
}

func (r *Result) AllImages() []string {
	seen := map[string]bool{}
	var images []string
	for _, op := range r.Operators {
		for _, ri := range op.RelatedImages {
			if !seen[ri.Image] {
				seen[ri.Image] = true
				images = append(images, ri.Image)
			}
		}
	}
	for _, img := range r.CustomImages {
		if !seen[img] {
			seen[img] = true
			images = append(images, img)
		}
	}
	return images
}

func New(kubeconfig string) (*Collector, error) {
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
		return nil, fmt.Errorf("Kubernetes クライアントの作成に失敗: %w", err)
	}

	return &Collector{client: client}, nil
}

func (c *Collector) GetClusterName() (name, id string, err error) {
	ctx := context.Background()
	gvr := schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "infrastructures"}
	infra, err := c.client.Resource(gvr).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("Infrastructure リソースの取得に失敗: %w", err)
	}

	name, _, _ = unstructured.NestedString(infra.Object, "status", "infrastructureName")

	cvGVR := schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	cv, err := c.client.Resource(cvGVR).Get(ctx, "version", metav1.GetOptions{})
	if err == nil {
		id, _, _ = unstructured.NestedString(cv.Object, "spec", "clusterID")
	}

	if name == "" {
		name = "unknown-cluster"
	}
	return name, id, nil
}

func (c *Collector) Collect() (*Result, error) {
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	result := &Result{CollectedAt: now}

	fmt.Println("==> クラスタ情報を取得...")
	clusterName, clusterID, err := c.GetClusterName()
	if err != nil {
		fmt.Printf("    WARN: クラスタ名の取得に失敗: %v\n", err)
	}
	result.ClusterName = clusterName
	result.ClusterID = clusterID
	fmt.Printf("    Cluster: %s\n", clusterName)

	fmt.Println("==> OCP バージョンを取得...")
	version, channel, releaseImage, err := c.getOCPVersion(ctx)
	if err != nil {
		return nil, err
	}
	result.OCPVersion = version
	result.OCPChannel = channel
	result.ReleaseImage = releaseImage

	fmt.Println("==> Subscription からオペレーター情報を取得...")
	operators, err := c.getOperatorsFromSubscriptions(ctx, now)
	if err != nil {
		return nil, err
	}

	fmt.Println("==> CSV から relatedImages を取得...")
	if err := c.enrichWithRelatedImages(ctx, operators); err != nil {
		fmt.Printf("    WARN: relatedImages の取得に一部失敗: %v\n", err)
	}

	fmt.Println("==> PackageManifest からデフォルトチャネルを取得...")
	if err := c.enrichWithDefaultChannels(ctx, operators); err != nil {
		fmt.Printf("    WARN: デフォルトチャネルの取得に一部失敗: %v\n", err)
	}
	result.Operators = operators

	fmt.Println("==> CatalogSource を取得...")
	catalogs, err := c.getCatalogSources(ctx)
	if err != nil {
		return nil, err
	}
	result.CatalogSources = catalogs

	fmt.Println("==> Pod イメージを収集...")
	podImages, err := c.getPodImages(ctx)
	if err != nil {
		return nil, err
	}
	result.PodImages = podImages
	result.CustomImages = filterCustomImages(podImages)

	// pull-secret を取得して保存
	fmt.Println("==> Pull secret を取得...")
	if ps, err := c.getPullSecret(ctx); err == nil {
		result.pullSecret = ps
	} else {
		fmt.Printf("    WARN: pull-secret の取得に失敗: %v\n", err)
	}

	fmt.Println("==> OCP リリースイメージのサイズを取得...")
	authFile := ""
	if len(result.pullSecret) > 0 {
		authFile = filepath.Join(os.TempDir(), "oc-mirctl-pull-secret.json")
		os.WriteFile(authFile, result.pullSecret, 0600)
		defer os.Remove(authFile)
	}
	if size, err := GetReleaseImageSize(result.ReleaseImage, authFile); err == nil {
		result.PlatformImageSize = size
		fmt.Printf("    Platform: %s\n", FormatSize(size))
	} else {
		fmt.Printf("    WARN: リリースイメージサイズの取得に失敗: %v\n", err)
	}

	return result, nil
}

func (c *Collector) getPullSecret(ctx context.Context) ([]byte, error) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	secret, err := c.client.Resource(gvr).Namespace("openshift-config").Get(ctx, "pull-secret", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// unstructured 経由の Secret.data は base64 エンコードされたまま
	dataMap, found, _ := unstructured.NestedMap(secret.Object, "data")
	if !found {
		return nil, fmt.Errorf(".data not found in pull-secret")
	}

	encoded, ok := dataMap[".dockerconfigjson"].(string)
	if !ok {
		return nil, fmt.Errorf(".dockerconfigjson not found in pull-secret")
	}

	decoded, err := base64Decode(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode failed: %w", err)
	}

	return decoded, nil
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func (c *Collector) getOCPVersion(ctx context.Context) (version, channel, releaseImage string, err error) {
	gvr := schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	cv, err := c.client.Resource(gvr).Get(ctx, "version", metav1.GetOptions{})
	if err != nil {
		return "", "", "", fmt.Errorf("ClusterVersion の取得に失敗: %w", err)
	}

	version, _, _ = unstructured.NestedString(cv.Object, "status", "desired", "version")
	releaseImage, _, _ = unstructured.NestedString(cv.Object, "status", "desired", "image")

	history, _, _ := unstructured.NestedSlice(cv.Object, "status", "history")
	if len(history) > 0 {
		if entry, ok := history[0].(map[string]interface{}); ok {
			version, _ = entry["version"].(string)
		}
	}

	channel, _, _ = unstructured.NestedString(cv.Object, "spec", "channel")
	return version, channel, releaseImage, nil
}

func (c *Collector) getOperatorsFromSubscriptions(ctx context.Context, now string) ([]OperatorInfo, error) {
	gvr := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}
	subList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Subscription の取得に失敗: %w", err)
	}

	var operators []OperatorInfo
	for _, sub := range subList.Items {
		pkg, _, _ := unstructured.NestedString(sub.Object, "spec", "name")
		ch, _, _ := unstructured.NestedString(sub.Object, "spec", "channel")
		catSrc, _, _ := unstructured.NestedString(sub.Object, "spec", "source")
		catNS, _, _ := unstructured.NestedString(sub.Object, "spec", "sourceNamespace")
		csv, _, _ := unstructured.NestedString(sub.Object, "status", "currentCSV")

		op := OperatorInfo{
			Name:          sub.GetName(),
			PackageName:   pkg,
			Channel:       ch,
			CatalogSource: catSrc,
			CatalogNS:     catNS,
			Version:       csv,
			LastSeen:      now,
		}
		operators = append(operators, op)
		fmt.Printf("    %s (channel=%s, catalog=%s)\n", pkg, ch, catSrc)
	}

	sort.Slice(operators, func(i, j int) bool { return operators[i].PackageName < operators[j].PackageName })
	return operators, nil
}

func (c *Collector) enrichWithRelatedImages(ctx context.Context, operators []OperatorInfo) error {
	gvr := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}
	csvList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("CSV の取得に失敗: %w", err)
	}

	csvMap := map[string]*unstructured.Unstructured{}
	for i := range csvList.Items {
		csv := &csvList.Items[i]
		csvMap[csv.GetName()] = csv
	}

	for i := range operators {
		csv, ok := csvMap[operators[i].Version]
		if !ok {
			continue
		}

		relatedImages, _, _ := unstructured.NestedSlice(csv.Object, "spec", "relatedImages")
		for _, ri := range relatedImages {
			if riMap, ok := ri.(map[string]interface{}); ok {
				img, _ := riMap["image"].(string)
				name, _ := riMap["name"].(string)
				if img != "" {
					operators[i].RelatedImages = append(operators[i].RelatedImages, RelatedImage{
						Name:  name,
						Image: img,
					})
				}
			}
		}
	}

	return nil
}

func (c *Collector) enrichWithDefaultChannels(ctx context.Context, operators []OperatorInfo) error {
	gvr := schema.GroupVersionResource{Group: "packages.operators.coreos.com", Version: "v1", Resource: "packagemanifests"}
	pmList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("PackageManifest の取得に失敗: %w", err)
	}

	pmMap := map[string]string{}
	for _, pm := range pmList.Items {
		name := pm.GetName()
		defaultCh, _, _ := unstructured.NestedString(pm.Object, "status", "defaultChannel")
		if defaultCh != "" {
			pmMap[name] = defaultCh
		}
	}

	for i := range operators {
		if ch, ok := pmMap[operators[i].PackageName]; ok {
			operators[i].DefaultChannel = ch
			if ch != operators[i].Channel {
				fmt.Printf("    %s: channel=%s, defaultChannel=%s\n", operators[i].PackageName, operators[i].Channel, ch)
			}
		}
	}

	return nil
}

func (c *Collector) getCatalogSources(ctx context.Context) ([]CatalogInfo, error) {
	gvr := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "catalogsources"}
	csList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("CatalogSource の取得に失敗: %w", err)
	}

	var catalogs []CatalogInfo
	for _, cs := range csList.Items {
		image, _, _ := unstructured.NestedString(cs.Object, "spec", "image")
		catalogs = append(catalogs, CatalogInfo{
			Name:      cs.GetName(),
			Namespace: cs.GetNamespace(),
			Image:     image,
		})
	}
	return catalogs, nil
}

func (c *Collector) getPodImages(ctx context.Context) ([]string, error) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	podList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Pod の取得に失敗: %w", err)
	}

	imageSet := map[string]bool{}
	for _, pod := range podList.Items {
		extractImages(pod.Object, "containers", imageSet)
		extractImages(pod.Object, "initContainers", imageSet)
	}

	var images []string
	for img := range imageSet {
		images = append(images, img)
	}
	sort.Strings(images)
	return images, nil
}

func extractImages(obj map[string]interface{}, field string, imageSet map[string]bool) {
	containers, _, _ := unstructured.NestedSlice(obj, "spec", field)
	for _, c := range containers {
		if cMap, ok := c.(map[string]interface{}); ok {
			if img, ok := cMap["image"].(string); ok && img != "" {
				imageSet[img] = true
			}
		}
	}
}

func filterCustomImages(images []string) []string {
	redhatPrefixes := []string{
		"quay.io/openshift",
		"registry.redhat.io",
		"registry.access.redhat.com",
		"image-registry.openshift-image-registry.svc",
	}

	var custom []string
	for _, img := range images {
		isRedhat := false
		for _, prefix := range redhatPrefixes {
			if strings.HasPrefix(img, prefix) {
				isRedhat = true
				break
			}
		}
		if !isRedhat {
			custom = append(custom, img)
		}
	}
	return custom
}

// LoadPrevious は既存の collected-data.json を読み込む (マージ用)
func LoadPrevious(dir string) (*Result, error) {
	path := filepath.Join(dir, "collected-data.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Merge は old (前回) と new (今回) をマージして新しい Result を返す
func Merge(old, new *Result) *Result {
	merged := &Result{
		ClusterName:       new.ClusterName,
		ClusterID:         new.ClusterID,
		OCPVersion:        new.OCPVersion,
		OCPChannel:        new.OCPChannel,
		ReleaseImage:      new.ReleaseImage,
		PlatformImageSize: new.PlatformImageSize,
		CollectedAt:       new.CollectedAt,
		pullSecret:        new.pullSecret,
	}

	// --- Operators: キー = catalogSource/packageName ---
	merged.Operators = mergeOperators(old.Operators, new.Operators)

	// --- PodImages: 和集合 ---
	merged.PodImages = mergeStringSlices(old.PodImages, new.PodImages)

	// --- CustomImages: 和集合 ---
	merged.CustomImages = mergeStringSlices(old.CustomImages, new.CustomImages)

	// --- CatalogSources: キー = namespace/name, image は最新 ---
	merged.CatalogSources = mergeCatalogSources(old.CatalogSources, new.CatalogSources)

	return merged
}

func mergeOperators(old, new []OperatorInfo) []OperatorInfo {
	index := map[string]*OperatorInfo{}

	// old を先にインデックスに入れる
	for i := range old {
		op := old[i]
		index[op.Key()] = &op
	}

	// new で上書き or 追加
	for i := range new {
		op := new[i]
		key := op.Key()

		if existing, ok := index[key]; ok {
			// 同一キー: channel, version は最新を採用
			existing.Channel = op.Channel
			existing.DefaultChannel = op.DefaultChannel
			existing.Version = op.Version
			existing.Name = op.Name
			existing.CatalogNS = op.CatalogNS
			existing.LastSeen = op.LastSeen

			// relatedImages は和集合 (image をキーにマージ)
			existing.RelatedImages = mergeRelatedImages(existing.RelatedImages, op.RelatedImages)
		} else {
			// 新規追加
			index[key] = &op
		}
	}

	// old のみに存在するもの: 保持 (lastSeen は更新しない = 前回のまま)

	var result []OperatorInfo
	for _, op := range index {
		result = append(result, *op)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PackageName < result[j].PackageName })
	return result
}

func mergeCatalogSources(old, new []CatalogInfo) []CatalogInfo {
	index := map[string]*CatalogInfo{}

	for i := range old {
		cs := old[i]
		index[cs.Key()] = &cs
	}

	for i := range new {
		cs := new[i]
		// 既存は最新の image で上書き、新規は追加
		index[cs.Key()] = &cs
	}

	var result []CatalogInfo
	for _, cs := range index {
		result = append(result, *cs)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func mergeRelatedImages(old, new []RelatedImage) []RelatedImage {
	index := map[string]RelatedImage{}
	for _, ri := range old {
		index[ri.Image] = ri
	}
	for _, ri := range new {
		index[ri.Image] = ri
	}
	var result []RelatedImage
	for _, ri := range index {
		result = append(result, ri)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Image < result[j].Image })
	return result
}

func mergeStringSlices(a, b []string) []string {
	set := map[string]bool{}
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		set[s] = true
	}

	var result []string
	for s := range set {
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}

func (r *Result) Save(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(dir, "collected-data.json")
	fmt.Printf("==> %s に保存\n", path)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	if len(r.pullSecret) > 0 {
		psPath := filepath.Join(dir, "pull-secret.json")
		if err := os.WriteFile(psPath, r.pullSecret, 0600); err != nil {
			fmt.Printf("    WARN: pull-secret.json の保存に失敗: %v\n", err)
		} else {
			fmt.Printf("==> %s に保存 (list -v のサイズ取得用)\n", psPath)
		}
	}

	return nil
}
