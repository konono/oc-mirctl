package collector

import "testing"

func TestResolvePodImageRef(t *testing.T) {
	cases := []struct {
		img, imageID, want string
	}{
		// タグあり / digest あり はそのまま
		{"quay.io/x/app:v1", "", "quay.io/x/app:v1"},
		{"quay.io/x/app@sha256:aaa", "", "quay.io/x/app@sha256:aaa"},
		{"quay.io/x/app:v1@sha256:aaa", "", "quay.io/x/app:v1@sha256:aaa"},
		// host:port はタグではない
		{"registry.local:5000/app", "registry.local:5000/app@sha256:bbb", "registry.local:5000/app@sha256:bbb"},
		// タグなし → imageID の digest
		{"registry.access.redhat.com/ubi9/ubi-minimal",
			"registry.access.redhat.com/ubi9/ubi-minimal@sha256:1d11",
			"registry.access.redhat.com/ubi9/ubi-minimal@sha256:1d11"},
		{"docker.io/library/nginx", "docker-pullable://docker.io/library/nginx@sha256:ccc", "docker.io/library/nginx@sha256:ccc"},
		// imageID なし → :latest
		{"registry.access.redhat.com/ubi9/httpd-24", "", "registry.access.redhat.com/ubi9/httpd-24:latest"},
		{"registry.local:5000/app", "", "registry.local:5000/app:latest"},
	}
	for _, c := range cases {
		if got := resolvePodImageRef(c.img, c.imageID); got != c.want {
			t.Errorf("resolvePodImageRef(%q, %q) = %q, want %q", c.img, c.imageID, got, c.want)
		}
	}
}

func TestPodContainerImages(t *testing.T) {
	pod := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{"name": "app", "image": "registry.access.redhat.com/ubi9/ubi-minimal"},
				map[string]interface{}{"name": "side", "image": "quay.io/x/side:v2"},
			},
			"initContainers": []interface{}{
				map[string]interface{}{"name": "app", "image": "registry.access.redhat.com/ubi9/httpd-24"},
			},
		},
		"status": map[string]interface{}{
			"containerStatuses": []interface{}{
				map[string]interface{}{"name": "app", "imageID": "registry.access.redhat.com/ubi9/ubi-minimal@sha256:1d11"},
			},
		},
	}
	got := podContainerImages(pod)
	want := []string{
		"registry.access.redhat.com/ubi9/ubi-minimal@sha256:1d11",
		"quay.io/x/side:v2",
		// init コンテナは同名でも containerStatuses の imageID を使わない
		"registry.access.redhat.com/ubi9/httpd-24:latest",
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
