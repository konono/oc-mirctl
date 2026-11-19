package collector

import "testing"

func TestTemplateImages(t *testing.T) {
	spec := map[string]interface{}{
		"containers": []interface{}{
			map[string]interface{}{"name": "a", "image": "quay.io/x/app:v1"},
			map[string]interface{}{"name": "b", "image": "registry.access.redhat.com/ubi9/ubi-micro"},
		},
		"initContainers": []interface{}{
			map[string]interface{}{"name": "i", "image": "quay.io/x/init@sha256:aaa"},
		},
	}
	got := templateImages(spec)
	// 書かれたまま返す (":latest" の補完は detectIdleImages で行う)
	want := []string{"quay.io/x/app:v1", "registry.access.redhat.com/ubi9/ubi-micro", "quay.io/x/init@sha256:aaa"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDetectIdleImages(t *testing.T) {
	result := &Result{
		ReleaseImage: "quay.io/openshift-release-dev/ocp-release@sha256:cur",
		PodImages:    []string{"quay.io/x/running@sha256:aaa"},
		Operators: []OperatorInfo{{RelatedImages: []RelatedImage{
			{Image: "nvcr.io/nvidia/k8s-mig-manager:v0.15.2@sha256:mig"},
		}}},
	}
	workloadImages := map[string][]string{
		// 動いている Pod と同じ → 対象外
		"quay.io/x/running@sha256:aaa": {"Deployment ns/running"},
		// relatedImages でカバー (tag+digest を digest のみで照合) → 対象外
		"nvcr.io/nvidia/k8s-mig-manager@sha256:mig": {"DaemonSet nvidia/mig-manager"},
		// platform でカバー → 対象外
		"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:plat": {"CronJob openshift-image-registry/image-pruner"},
		// 内部レジストリ → 対象外
		"image-registry.openshift-image-registry.svc:5000/ns/app:latest": {"Deployment ns/internal"},
		// タグなし参照で Pod が動いている (Pod 側は digest に解決済み) → spec の文字列で照合して対象外
		"registry.access.redhat.com/ubi9/httpd-24": {"Deployment existing-app/web"},
		// 待機中 (複数の参照元、重複あり)
		"registry.access.redhat.com/ubi9/ubi-micro:9.6": {"Deployment b/idle", "CronJob a/job", "Deployment b/idle"},
		// 待機中のタグなし参照 → ":latest" を補う
		"quay.io/x/idle-notag": {"Deployment c/notag"},
	}
	podSpec := map[string]bool{"quay.io/x/running@sha256:aaa": true, "registry.access.redhat.com/ubi9/httpd-24": true}
	got := detectIdleImages(workloadImages, podSpec, result, []string{"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:plat"})
	if len(got) != 2 || got[0].Image != "quay.io/x/idle-notag:latest" || got[1].Image != "registry.access.redhat.com/ubi9/ubi-micro:9.6" {
		t.Fatalf("got %+v", got)
	}
	got = got[1:]
	if w := got[0].Workloads; len(w) != 2 || w[0] != "CronJob a/job" || w[1] != "Deployment b/idle" {
		t.Errorf("workloads: got %v", w)
	}
}

func TestUnusedExcludesIdle(t *testing.T) {
	result := &Result{IdleImages: []IdleImage{{Image: "registry.access.redhat.com/ubi9/ubi-micro@sha256:idle"}}}
	nodeImages := []nodeImage{
		{names: []string{"registry.access.redhat.com/ubi9/ubi-micro@sha256:idle"}},
		{names: []string{"registry.access.redhat.com/ubi8/ubi-minimal@sha256:unused"}},
	}
	got := detectUnusedImages(nodeImages, result, nil)
	if len(got) != 1 || got[0] != "registry.access.redhat.com/ubi8/ubi-minimal@sha256:unused" {
		t.Errorf("got %v, want only the unused image (idle image is not unused)", got)
	}
}
