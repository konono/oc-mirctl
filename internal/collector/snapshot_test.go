package collector

import (
	"strings"
	"testing"
)

func TestCurrentSnapshotExcludesOldImages(t *testing.T) {
	old := &Result{
		OCPVersion:   "4.19.5",
		Operators:    []OperatorInfo{{PackageName: "op", CatalogSource: "cs", Version: "op.v1.0.0", RelatedImages: []RelatedImage{{Image: "quay.io/x/op@sha256:v1"}}}},
		PodImages:    []string{"example.com/app@sha256:old"},
		CustomImages: []string{"example.com/app@sha256:old"},
	}
	latest := &Result{
		OCPVersion:   "4.20.1",
		Operators:    []OperatorInfo{{PackageName: "op", CatalogSource: "cs", Version: "op.v1.1.0", RelatedImages: []RelatedImage{{Image: "quay.io/x/op@sha256:v2"}}}},
		PodImages:    []string{"example.com/app@sha256:new"},
		CustomImages: []string{"example.com/app@sha256:new"},
		pullSecret:   []byte(`{"auths":{}}`),
	}
	merged := Merge(old, latest)
	if len(merged.AllImages()) != 4 {
		t.Fatalf("merged should accumulate old images, got %v", merged.AllImages())
	}
	sizes := map[string]int64{}
	for _, img := range merged.AllImages() {
		sizes[img] = 100
	}

	snap := CurrentSnapshot(latest, sizes)
	imgs := snap.AllImages()
	if len(imgs) != 2 {
		t.Fatalf("snapshot should contain only latest images, got %v", imgs)
	}
	for _, img := range imgs {
		if strings.Contains(img, "old") || strings.HasSuffix(img, ":v1") {
			t.Errorf("snapshot contains old image %s", img)
		}
	}
	if len(snap.ImageSizes) != 2 {
		t.Errorf("snapshot sizes should be filtered to 2 images, got %d", len(snap.ImageSizes))
	}
	if snap.pullSecret != nil {
		t.Error("snapshot must not carry pull secret")
	}
	if latest.pullSecret == nil {
		t.Error("CurrentSnapshot must not mutate the source result")
	}
}

func TestLoadDataMissingCurrent(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadData(dir, false)
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("expected error hinting --all, got %v", err)
	}
}
