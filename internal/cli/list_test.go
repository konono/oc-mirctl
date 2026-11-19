package cli

import (
	"testing"

	"github.com/konono/oc-mirctl/internal/collector"
)

func TestImageKey(t *testing.T) {
	cases := map[string]string{
		"nvcr.io/nvidia/gpu-operator:v26.7.3@sha256:aaa": "nvcr.io/nvidia/gpu-operator@sha256:aaa",
		"nvcr.io/nvidia/gpu-operator@sha256:aaa":         "nvcr.io/nvidia/gpu-operator@sha256:aaa",
		"registry.local:5000/app@sha256:bbb":             "registry.local:5000/app@sha256:bbb",
		"registry.local:5000/app:v1@sha256:bbb":          "registry.local:5000/app@sha256:bbb",
		"quay.io/x/app:v1":                               "quay.io/x/app:v1",
	}
	for in, want := range cases {
		if got := imageKey(in); got != want {
			t.Errorf("imageKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// 同じイメージを複数回数えないこと (relatedImages 内の別名、Operator 間、
// Custom Images との重なり、tag+digest と digest のみ)
func TestBuildListOutputDedupSizes(t *testing.T) {
	const (
		vllm  = "registry.redhat.io/rhaii/vllm-cuda-rhel9@sha256:v"
		gpuOp = "nvcr.io/nvidia/gpu-operator:v26.7.3@sha256:g"
		uniq  = "registry.redhat.io/rhoai/odh-dashboard-rhel9@sha256:d"
		app   = "quay.io/example/app@sha256:a"
		unc   = "registry.access.redhat.com/ubi9/httpd-24@sha256:h"
		idle  = "quay.io/example/batch:1.0"
		old   = "nvcr.io/nvidia/gpu-operator@sha256:old"
	)
	data := &collector.Result{
		OCPVersion:        "4.22.21",
		PlatformImageSize: 22_000,
		Operators: []collector.OperatorInfo{
			{PackageName: "rhods-operator", CatalogSource: "redhat-operators", RelatedImages: []collector.RelatedImage{
				{Name: "rhaii_vllm_cuda_image", Image: vllm},
				{Name: "rhaii_vllm_cuda_fast_1_image", Image: vllm},
				{Name: "rhaii_vllm_cuda_fast_2_image", Image: vllm},
				{Name: "odh_dashboard_image", Image: uniq},
			}},
			{PackageName: "gpu-operator-certified", CatalogSource: "certified-operators", RelatedImages: []collector.RelatedImage{
				{Name: "gpu-operator-image", Image: gpuOp},
				{Name: "gpu-operator-validator-image", Image: gpuOp},
			}},
		},
		// Pod のイメージは digest のみ (relatedImages は tag+digest) で同じイメージ
		CustomImages:    []string{"nvcr.io/nvidia/gpu-operator@sha256:g", app},
		UncoveredImages: []string{unc},
		IdleImages:      []collector.IdleImage{{Image: idle, Workloads: []string{"CronJob app/batch"}}},
		UnusedImages:    []string{old},
	}
	sizes := map[string]int64{
		vllm: 8_700, uniq: 300, gpuOp: 140, "nvcr.io/nvidia/gpu-operator@sha256:g": 140,
		app: 50, unc: 100, idle: 10, old: 130,
	}
	excl := &collector.Exclusions{}

	out := buildListOutput(data, excl, sizes, false, false)
	if out.Operators[0].SizeBytes != 9_000 {
		t.Errorf("rhods size = %d, want 9000 (vllm counted once)", out.Operators[0].SizeBytes)
	}
	if out.Operators[1].SizeBytes != 140 {
		t.Errorf("gpu size = %d, want 140", out.Operators[1].SizeBytes)
	}
	s := out.Summary
	if s.ActiveRelatedImages != 6 || s.UniqueRelatedImages != 3 {
		t.Errorf("related images = %d (unique %d), want 6 (unique 3)", s.ActiveRelatedImages, s.UniqueRelatedImages)
	}
	if s.OperatorSizeBytes != 9_140 {
		t.Errorf("operators = %d, want 9140", s.OperatorSizeBytes)
	}
	if s.CustomImageSizeBytes != 50 {
		t.Errorf("custom = %d, want 50 (gpu-operator already counted in operators)", s.CustomImageSizeBytes)
	}
	if s.UncoveredSizeBytes != 100 || s.IdleSizeBytes != 10 || s.UnusedSizeBytes != 0 {
		t.Errorf("uncovered/idle/unused = %d/%d/%d, want 100/10/0", s.UncoveredSizeBytes, s.IdleSizeBytes, s.UnusedSizeBytes)
	}
	if want := int64(22_000 + 9_140 + 50 + 100 + 10); s.TotalSizeBytes != want {
		t.Errorf("total = %d, want %d", s.TotalSizeBytes, want)
	}

	// --running-only: Idle を含めない、--all: Unused を含める
	if got := buildListOutput(data, excl, sizes, false, true).Summary; got.IdleSizeBytes != 0 || got.TotalSizeBytes != 22_000+9_140+50+100 {
		t.Errorf("running-only: idle=%d total=%d", got.IdleSizeBytes, got.TotalSizeBytes)
	}
	if got := buildListOutput(data, excl, sizes, true, false).Summary; got.UnusedSizeBytes != 130 || got.TotalSizeBytes != 22_000+9_140+50+100+10+130 {
		t.Errorf("all: unused=%d total=%d", got.UnusedSizeBytes, got.TotalSizeBytes)
	}
}

// 同じイメージの参照の一部だけサイズ取得に失敗していても、取得できたサイズが
// 合計から消えないこと (rev 指摘)
func TestBuildListOutputPartialSizeFailure(t *testing.T) {
	const (
		tagged = "nvcr.io/nvidia/gpu-operator:v26.7.3@sha256:x"
		digest = "nvcr.io/nvidia/gpu-operator@sha256:x"
	)
	data := &collector.Result{
		Operators: []collector.OperatorInfo{{
			PackageName: "gpu-operator-certified", CatalogSource: "certified-operators",
			// Operator 内でも先に未取得の参照、後に取得済みの参照
			RelatedImages: []collector.RelatedImage{{Name: "a", Image: tagged}, {Name: "b", Image: digest}},
		}},
		CustomImages: []string{digest},
	}
	sizes := map[string]int64{tagged: -1, digest: 1000}

	out := buildListOutput(data, &collector.Exclusions{}, sizes, false, false)
	if got := out.Operators[0].SizeBytes; got != 1000 {
		t.Errorf("operator size = %d, want 1000", got)
	}
	s := out.Summary
	if s.OperatorSizeBytes != 1000 || s.CustomImageSizeBytes != 0 || s.TotalSizeBytes != 1000 {
		t.Errorf("operators/custom/total = %d/%d/%d, want 1000/0/1000", s.OperatorSizeBytes, s.CustomImageSizeBytes, s.TotalSizeBytes)
	}

	// 区分をまたぐ場合: relatedImages 側は未取得 (sizes に無い)、Custom Images 側だけ取得済み
	data.Operators[0].RelatedImages = []collector.RelatedImage{{Name: "a", Image: tagged}}
	sizes = map[string]int64{digest: 1000}
	s = buildListOutput(data, &collector.Exclusions{}, sizes, false, false).Summary
	if s.TotalSizeBytes != 1000 {
		t.Errorf("total = %d, want 1000 (size known via another reference must not be lost)", s.TotalSizeBytes)
	}
}
