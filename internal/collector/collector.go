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
	PodImages        []string       `json:"podImages"`
	CustomImages     []string       `json:"customImages"`
	UncoveredImages  []string       `json:"uncoveredImages,omitempty"`
	PrivateRegistries []string          `json:"privateRegistries,omitempty"`
	PrivateImages     []string          `json:"privateImages,omitempty"`
	CatalogSources    []CatalogInfo    `json:"catalogSources"`
	PlatformImageSize int64            `json:"platformImageSize,omitempty"`
	ImageSizes        map[string]int64 `json:"imageSizes,omitempty"`
	CollectedAt       string           `json:"collectedAt"`
	pullSecret        []byte
}

// IsPrivateImage はイメージが private registry 認証を必要とするかを判定する
func (r *Result) IsPrivateImage(img string) bool {
	for _, pi := range r.PrivateImages {
		if img == pi {
			return true
		}
	}
	return false
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
	for _, img := range r.UncoveredImages {
		if !seen[img] {
			seen[img] = true
			images = append(images, img)
		}
	}
	return images
}

// MergePreviousPullSecret は前回保存した pull-secret.json の認証情報を
// 現在の pullSecret にマージする。前回だけ存在した private image の認証を保持するため。
func (r *Result) MergePreviousPullSecret(oldPS []byte) {
	if len(r.pullSecret) == 0 {
		r.pullSecret = oldPS
		return
	}
	merged, err := mergePullSecrets(r.pullSecret, [][]byte{oldPS})
	if err == nil {
		r.pullSecret = merged
	}
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

	// Pod の imagePullSecrets から private registry を検出し、認証情報をマージ
	fmt.Println("==> Pod imagePullSecrets から private registry を検出...")
	privResult := c.collectPrivateRegistryAuth(ctx)
	if len(privResult.registries) > 0 {
		result.PrivateRegistries = privResult.registries
		result.PrivateImages = privResult.images
		fmt.Printf("    %d private registries 検出:\n", len(privResult.registries))
		for _, reg := range privResult.registries {
			fmt.Printf("      - %s\n", reg)
		}
		fmt.Printf("    %d private images:\n", len(privResult.images))
		for _, img := range privResult.images {
			fmt.Printf("      - %s\n", img)
		}
		if len(privResult.extraAuths) > 0 && len(result.pullSecret) > 0 {
			merged, err := mergePullSecrets(result.pullSecret, privResult.extraAuths)
			if err == nil {
				result.pullSecret = merged
				fmt.Println("    認証情報を pull-secret にマージしました")
			} else {
				fmt.Printf("    WARN: 認証情報のマージに失敗: %v\n", err)
			}
		}
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

	fmt.Println("==> Platform イメージ一覧を取得してカバレッジ分析...")
	result.UncoveredImages = detectUncoveredImages(result, authFile)
	if len(result.UncoveredImages) > 0 {
		fmt.Printf("    ⚠ %d 個の未カバーイメージを検出:\n", len(result.UncoveredImages))
		for _, img := range result.UncoveredImages {
			fmt.Printf("      - %s\n", img)
		}
	} else {
		fmt.Println("    全 Pod イメージがカバーされています")
	}

	return result, nil
}

// detectUncoveredImages は Pod で使用中のイメージのうち、platform リリースにも
// operator relatedImages にも customImages にも含まれないイメージを検出する。
// これらは additionalImages として明示的にミラーする必要がある。
//
// platform イメージは repo 名一致でカバー済みと判定する（リリース全体がミラーされるため）。
// operator relatedImages は digest の完全一致で判定する（oc-mirror は CSV に記載された
// 特定の digest のみミラーするため、同じ repo でも異なる digest は未ミラーになる）。
// DetectUncoveredImages は Pod で使用中のイメージのうち、カバーされていないものを検出する。
// Merge 後に再計算するために公開する。
func DetectUncoveredImages(result *Result, authFile string) []string {
	return detectUncoveredImages(result, authFile)
}

func detectUncoveredImages(result *Result, authFile string) []string {
	// exactCovered: digest 完全一致でカバー済み
	exactCovered := map[string]bool{}

	// 1. Platform イメージ (oc adm release info から取得)
	// platform はリリース全体がミラーされるので digest 完全一致で判定する。
	// repo 名一致は使わない — 同一 repo に異なるコンポーネントが複数あり、
	// repo 名だけでは無関係な digest までカバー済みと誤判定する。
	exactCovered[result.ReleaseImage] = true // リリースイメージ自体もカバー済み
	platformImages, err := GetPlatformImages(result.ReleaseImage, authFile)
	if err != nil {
		fmt.Printf("    WARN: platform イメージ一覧の取得に失敗: %v\n", err)
	} else {
		fmt.Printf("    Platform images: %d\n", len(platformImages))
		for _, img := range platformImages {
			exactCovered[img] = true
		}
	}

	// 2. Operator relatedImages — digest 完全一致のみ
	// oc-mirror は CSV の relatedImages に記載された特定 digest のみミラーする。
	// 同じ repo でも Pod が異なる digest を使っていればミラーされない。
	for _, op := range result.Operators {
		for _, ri := range op.RelatedImages {
			exactCovered[ri.Image] = true
		}
	}

	// 3. CustomImages (既に additionalImages として追加される)
	for _, img := range result.CustomImages {
		exactCovered[img] = true
	}

	// 4. CatalogSource イメージ自体
	for _, cs := range result.CatalogSources {
		exactCovered[cs.Image] = true
	}

	var uncovered []string
	for _, podImg := range result.PodImages {
		if exactCovered[podImg] {
			continue
		}

		if isInternalRegistry(registryFromImage(podImg)) {
			continue
		}

		uncovered = append(uncovered, podImg)
	}

	sort.Strings(uncovered)
	return uncovered
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

// internalRegistries はクラスタ内部レジストリ (imagePullSecrets の判定から除外)
var internalRegistries = map[string]bool{
	"image-registry.openshift-image-registry.svc":                true,
	"image-registry.openshift-image-registry.svc:5000":           true,
	"image-registry.openshift-image-registry.svc.cluster.local":       true,
	"image-registry.openshift-image-registry.svc.cluster.local:5000":  true,
}

// isInternalRegistry は IP:port やホスト名がクラスタ内部レジストリかどうかを判定する。
// 静的リストに加え、172.30.0.0/16 (Kubernetes service CIDR) のアドレスも内部と見なす。
func isInternalRegistry(host string) bool {
	if internalRegistries[host] {
		return true
	}
	// IP:port 形式からホスト部分を抽出
	ip := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		ip = host[:idx]
	}
	// 172.30.x.x は OpenShift の service CIDR (ClusterIP)
	if strings.HasPrefix(ip, "172.30.") {
		return true
	}
	// 10.x.x.x も内部ネットワーク (pod/service CIDR)
	if strings.HasPrefix(ip, "10.") {
		return true
	}
	return false
}

// registryFromImage はイメージ参照からレジストリホスト名を抽出する
func registryFromImage(img string) string {
	// docker.io の暗黙参照 (nginx:latest, library/nginx:latest)
	if !strings.Contains(strings.SplitN(img, "/", 2)[0], ".") &&
		!strings.Contains(strings.SplitN(img, "/", 2)[0], ":") {
		return "docker.io"
	}
	parts := strings.SplitN(img, "/", 2)
	if len(parts) < 2 {
		return "docker.io"
	}
	host := parts[0]
	// ポート番号を含む場合 (myregistry.example.com:5000)
	if idx := strings.Index(host, ":"); idx > 0 {
		return host
	}
	return host
}

// privateRegistryResult は private registry 検出の結果
type privateRegistryResult struct {
	registries []string
	images     []string
	extraAuths [][]byte
}

// collectPrivateRegistryAuth は全 Pod の imagePullSecrets を収集し、
// グローバル pull-secret と異なる認証情報を持つレジストリと、
// そのレジストリのイメージを使っている Pod のイメージを特定する。
func (c *Collector) collectPrivateRegistryAuth(ctx context.Context) *privateRegistryResult {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	podList, err := c.client.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return &privateRegistryResult{}
	}

	// グローバル pull-secret のレジストリ → 認証情報を取得
	globalAuthMap := map[string]string{}
	if ps, err := c.getPullSecret(ctx); err == nil {
		globalAuthMap = extractAuthMap(ps)
	}

	// Pod → imagePullSecrets の Secret 参照を収集
	type secretRef struct {
		namespace string
		name      string
	}
	// Secret 参照 → それを使う Pod のイメージ群
	secretToPodImages := map[secretRef]map[string]bool{}
	for _, pod := range podList.Items {
		ns := pod.GetNamespace()
		ips, _, _ := unstructured.NestedSlice(pod.Object, "spec", "imagePullSecrets")
		if len(ips) == 0 {
			continue
		}

		// この Pod のイメージを収集
		podImgs := map[string]bool{}
		containers, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers")
		for _, c := range containers {
			if cMap, ok := c.(map[string]interface{}); ok {
				if img, ok := cMap["image"].(string); ok && img != "" {
					podImgs[img] = true
				}
			}
		}
		initContainers, _, _ := unstructured.NestedSlice(pod.Object, "spec", "initContainers")
		for _, c := range initContainers {
			if cMap, ok := c.(map[string]interface{}); ok {
				if img, ok := cMap["image"].(string); ok && img != "" {
					podImgs[img] = true
				}
			}
		}

		for _, ref := range ips {
			if refMap, ok := ref.(map[string]interface{}); ok {
				if name, ok := refMap["name"].(string); ok && name != "" {
					key := secretRef{namespace: ns, name: name}
					if secretToPodImages[key] == nil {
						secretToPodImages[key] = map[string]bool{}
					}
					for img := range podImgs {
						secretToPodImages[key][img] = true
					}
				}
			}
		}
	}

	if len(secretToPodImages) == 0 {
		return &privateRegistryResult{}
	}

	// Secret を取得して、private 認証を持つものを特定
	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	privateRegs := map[string]bool{}
	privateImgs := map[string]bool{}
	var extraAuths [][]byte

	for ref, podImgs := range secretToPodImages {
		secret, err := c.client.Resource(secretGVR).Namespace(ref.namespace).Get(ctx, ref.name, metav1.GetOptions{})
		if err != nil {
			continue
		}

		secretType, _, _ := unstructured.NestedString(secret.Object, "type")
		if secretType != "kubernetes.io/dockerconfigjson" && secretType != "kubernetes.io/dockercfg" {
			continue
		}

		dataMap, found, _ := unstructured.NestedMap(secret.Object, "data")
		if !found {
			continue
		}

		var encoded string
		if v, ok := dataMap[".dockerconfigjson"].(string); ok {
			encoded = v
		} else if v, ok := dataMap[".dockercfg"].(string); ok {
			encoded = v
		}
		if encoded == "" {
			continue
		}

		decoded, err := base64Decode(encoded)
		if err != nil {
			continue
		}

		// グローバル pull-secret と比較
		localAuthMap := extractAuthMap(decoded)
		privateRegSet := map[string]bool{}
		for reg, localAuth := range localAuthMap {
			if isInternalRegistry(reg) {
				continue
			}
			globalAuth, inGlobal := globalAuthMap[reg]
			if !inGlobal || globalAuth != localAuth {
				privateRegSet[reg] = true
				privateRegs[reg] = true
			}
		}

		if len(privateRegSet) > 0 {
			extraAuths = append(extraAuths, decoded)
			// この Secret を使う Pod のイメージのうち、
			// private registry のホストに一致するものを private イメージとして記録
			for img := range podImgs {
				imgReg := registryFromImage(img)
				if privateRegSet[imgReg] {
					privateImgs[img] = true
				}
			}
		}
	}

	var regs []string
	for reg := range privateRegs {
		regs = append(regs, reg)
	}
	sort.Strings(regs)

	var imgs []string
	for img := range privateImgs {
		imgs = append(imgs, img)
	}
	sort.Strings(imgs)

	return &privateRegistryResult{
		registries: regs,
		images:     imgs,
		extraAuths: extraAuths,
	}
}

// normalizeHost はレジストリ URL からホスト名を正規化する
func normalizeHost(reg string) string {
	host := reg
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	return host
}

// parseDockerAuth は dockerconfigjson または .dockercfg 形式をパースし、
// レジストリ → 認証情報の RawMessage マップを返す。
// .dockercfg 形式: ルートに直接 {"registry": {"auth": "..."}} がある
// dockerconfigjson 形式: {"auths": {"registry": {"auth": "..."}}} がある
func parseDockerAuth(data []byte) map[string]json.RawMessage {
	// まず dockerconfigjson 形式を試す
	var dockerConfigJSON struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(data, &dockerConfigJSON); err == nil && len(dockerConfigJSON.Auths) > 0 {
		return dockerConfigJSON.Auths
	}

	// .dockercfg 形式: ルートが直接 { "registry": { ... } } の形
	var dockerCfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &dockerCfg); err == nil {
		// "auths" キーが存在する場合は dockerconfigjson として扱う (auths が空だった場合)
		if _, hasAuths := dockerCfg["auths"]; hasAuths {
			return nil
		}
		return dockerCfg
	}

	return nil
}

// extractAuthMap は dockerconfigjson/.dockercfg からレジストリ → 認証情報の文字列表現のマップを返す
func extractAuthMap(data []byte) map[string]string {
	auths := parseDockerAuth(data)
	result := map[string]string{}
	for reg, auth := range auths {
		result[normalizeHost(reg)] = string(auth)
	}
	return result
}

// extractRegistriesFromAuth は dockerconfigjson/.dockercfg からレジストリホスト名を抽出する
func extractRegistriesFromAuth(data []byte) []string {
	auths := parseDockerAuth(data)
	var regs []string
	for reg := range auths {
		regs = append(regs, normalizeHost(reg))
	}
	return regs
}

// mergePullSecrets はグローバル pull-secret に追加の認証情報をマージする。
// extras は dockerconfigjson または .dockercfg 形式のいずれでもよい。
func mergePullSecrets(globalPS []byte, extras [][]byte) ([]byte, error) {
	globalAuths := parseDockerAuth(globalPS)
	if globalAuths == nil {
		globalAuths = map[string]json.RawMessage{}
	}

	for _, extra := range extras {
		extAuths := parseDockerAuth(extra)
		for reg, auth := range extAuths {
			// Pod の imagePullSecrets の認証を優先して上書きする。
			// private repo では Pod 側の認証がアクセスに必要。
			globalAuths[reg] = auth
		}
	}

	// 出力は常に dockerconfigjson 形式 (auths ラッパー付き)
	result := struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}{Auths: globalAuths}
	return json.Marshal(result)
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

	// --- UncoveredImages: マージ後の PodImages から再計算が必要 ---
	// Merge 時点では authFile がないため再計算できない。
	// collect.go 側で RecalculateUncovered を呼ぶ。暫定的に new を使う。
	merged.UncoveredImages = new.UncoveredImages

	// --- PrivateRegistries / PrivateImages: 和集合 ---
	merged.PrivateRegistries = mergeStringSlices(old.PrivateRegistries, new.PrivateRegistries)
	merged.PrivateImages = mergeStringSlices(old.PrivateImages, new.PrivateImages)

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
