package collector

import "testing"

func TestPastReleasesFromHistory(t *testing.T) {
	// status.history は新しい順
	history := []interface{}{
		map[string]interface{}{"version": "4.22.21", "image": "r@sha256:c", "state": "Completed"},
		map[string]interface{}{"version": "4.21.9", "image": "r@sha256:b", "state": "Completed"},
		map[string]interface{}{"version": "4.20.3", "image": "r@sha256:a", "state": "Partial"},
		map[string]interface{}{"version": "4.21.9", "image": "r@sha256:b", "state": "Completed"},
	}
	// 時系列: 4.21.9 → 4.20.3 (ロールバック) → 4.21.9 → 4.22.21。
	// 最初に現れた順で、現在の版と重複を除く
	got := pastReleasesFromHistory(history, "4.22.21")
	if len(got) != 2 || got[0].Version != "4.21.9" || got[1].Version != "4.20.3" {
		t.Errorf("got %+v, want [4.21.9 4.20.3]", got)
	}
}

func TestPackageFromProperties(t *testing.T) {
	props := `{"properties":[{"type":"olm.gvk","value":{"group":"g","kind":"K","version":"v1"}},` +
		`{"type":"olm.package","value":{"packageName":"gpu-operator-certified","version":"26.7.2"}}]}`
	pkg, ver := packageFromProperties(props)
	if pkg != "gpu-operator-certified" || ver != "26.7.2" {
		t.Errorf("got %q %q", pkg, ver)
	}
	if pkg, _ := packageFromProperties("not json"); pkg != "" {
		t.Errorf("invalid json should return empty, got %q", pkg)
	}
}

func TestChannelFor(t *testing.T) {
	pc := &packageChannels{
		defaultChannel: "stable-3.x",
		entries: map[string]map[string]bool{
			"stable-3.4": {"op.3.4.6": true, "op.3.4.3": true},
			"stable-3.5": {"op.3.5.3": true},
			"stable-3.x": {"op.3.5.3": true, "op.3.4.3": true},
		},
	}
	cases := []struct{ csv, preferred, want string }{
		{"op.3.5.3", "stable-3.5", "stable-3.5"}, // Subscription のチャネルを優先
		{"op.3.4.3", "stable-3.5", "stable-3.x"}, // 次に defaultChannel
		{"op.3.4.6", "stable-3.5", "stable-3.4"}, // それ以外は含むチャネル
		{"op.2.0.0", "stable-3.5", ""},           // カタログに無い
	}
	for _, c := range cases {
		if got := pc.channelFor(c.csv, c.preferred); got != c.want {
			t.Errorf("channelFor(%s, %s) = %q, want %q", c.csv, c.preferred, got, c.want)
		}
	}
	var nilPC *packageChannels
	if got := nilPC.channelFor("x", "y"); got != "" {
		t.Errorf("nil packageChannels should return empty, got %q", got)
	}
}

func TestDetectUnusedImages(t *testing.T) {
	result := &Result{
		ReleaseImage: "quay.io/openshift-release-dev/ocp-release@sha256:cur",
		PodImages:    []string{"registry.access.redhat.com/ubi9/ubi-minimal@sha256:inuse"},
		Operators: []OperatorInfo{{RelatedImages: []RelatedImage{
			{Image: "nvcr.io/nvidia/gpu-operator:v26.7.3@sha256:op3"},
		}}},
		PastReleases: []ReleaseEntry{{Version: "4.21.0", Image: "quay.io/openshift-release-dev/ocp-release@sha256:old"}},
	}
	nodeImages := []nodeImage{
		{names: []string{"registry.access.redhat.com/ubi9/ubi-minimal@sha256:inuse", "registry.access.redhat.com/ubi9/ubi-minimal:latest"}},
		{names: []string{"nvcr.io/nvidia/gpu-operator@sha256:op3"}}, // tag+digest の relatedImage と digest で一致
		{names: []string{"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:plat"}},
		{names: []string{"quay.io/openshift-release-dev/ocp-release@sha256:old"}},
		{names: []string{"registry.access.redhat.com/ubi8/ubi-minimal@sha256:unused", "registry.access.redhat.com/ubi8/ubi-minimal:8.10"}},
		{names: []string{"nvcr.io/nvidia/gpu-operator@sha256:op2", "nvcr.io/nvidia/gpu-operator:v26.7.2"}},
		{names: []string{"image-registry.openshift-image-registry.svc:5000/ns/app@sha256:internal"}},
	}
	got := detectUnusedImages(nodeImages, result, []string{"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:plat"})
	want := []string{
		"nvcr.io/nvidia/gpu-operator@sha256:op2",
		"registry.access.redhat.com/ubi8/ubi-minimal@sha256:unused",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}
