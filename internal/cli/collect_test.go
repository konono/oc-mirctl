package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/konono/oc-mirctl/internal/collector"
)

func baseResult() *collector.Result {
	return &collector.Result{
		OCPVersion:   "4.22.21",
		PodImages:    []string{"quay.io/x/app@sha256:aaa"},
		PastReleases: []collector.ReleaseEntry{{Version: "4.21.9", Image: "r@sha256:old1"}},
		UnusedImages: []string{"quay.io/x/old@sha256:bbb"},
		Operators: []collector.OperatorInfo{{
			PackageName:   "gpu-operator-certified",
			CatalogSource: "certified-operators",
			Channel:       "v26.7",
			Version:       "gpu-operator-certified.v26.7.3",
			BundleVersion: "26.7.3",
			RelatedImages: []collector.RelatedImage{{Image: "nvcr.io/nvidia/gpu-operator@sha256:op3"}},
			PastVersions:  []collector.PastVersion{{CSV: "gpu-operator-certified.v26.7.2", Version: "26.7.2", Channel: "v26.7"}},
		}},
	}
}

func clone(t *testing.T, r *collector.Result) *collector.Result {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var c collector.Result
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestBackupIfChanged(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(r *collector.Result)
		wantBackup bool
	}{
		{"変更なし", func(r *collector.Result) {}, false},
		{"タイムスタンプのみ変化", func(r *collector.Result) {
			r.CollectedAt = "2026-11-19T00:00:00Z"
			r.Operators[0].LastSeen = "2026-11-19T00:00:00Z"
			r.Operators[0].PastVersions[0].InstalledAt = "2026-11-19T00:00:00Z"
		}, false},
		// 件数が同じまま内容が入れ替わるケース (rev 指摘)
		{"過去のリリースが同件数で置換", func(r *collector.Result) {
			r.PastReleases = []collector.ReleaseEntry{{Version: "4.22.10", Image: "r@sha256:old2"}}
		}, true},
		{"Operator の過去の版が同件数で置換", func(r *collector.Result) {
			r.Operators[0].PastVersions = []collector.PastVersion{{CSV: "gpu-operator-certified.v26.3.3", Version: "26.3.3", Channel: "v26.3"}}
		}, true},
		{"過去の版のチャネルだけ変化", func(r *collector.Result) {
			r.Operators[0].PastVersions[0].Channel = "stable"
		}, true},
		{"relatedImages 件数同じのアップグレード", func(r *collector.Result) {
			r.Operators[0].Version = "gpu-operator-certified.v26.7.4"
			r.Operators[0].BundleVersion = "26.7.4"
			r.Operators[0].RelatedImages = []collector.RelatedImage{{Image: "nvcr.io/nvidia/gpu-operator@sha256:op4"}}
		}, true},
		{"未使用イメージが同件数で置換", func(r *collector.Result) {
			r.UnusedImages = []string{"quay.io/x/old@sha256:ccc"}
		}, true},
		// 同じ OCP バージョンのまま releaseImage の digest だけ変わる (rev 再指摘)
		{"同一版で releaseImage が変化", func(r *collector.Result) {
			r.ReleaseImage = "quay.io/openshift-release-dev/ocp-release@sha256:new"
		}, true},
		{"uncoveredImages が変化", func(r *collector.Result) {
			r.UncoveredImages = []string{"quay.io/x/uncovered@sha256:ddd"}
		}, true},
		{"privateImages / privateRegistries が変化", func(r *collector.Result) {
			r.PrivateRegistries = []string{"registry.example.com"}
			r.PrivateImages = []string{"registry.example.com/app@sha256:eee"}
		}, true},
		{"CatalogSource の image が変化", func(r *collector.Result) {
			r.CatalogSources = []collector.CatalogInfo{{Name: "c", Namespace: "ns", Image: "quay.io/x/catalog@sha256:new"}}
		}, true},
		{"defaultChannel だけ変化", func(r *collector.Result) {
			r.Operators[0].DefaultChannel = "stable"
		}, true},
		// 取得タイミングや並び順だけの違いではバックアップを作らない
		{"イメージサイズのみ変化", func(r *collector.Result) {
			r.ImageSizes = map[string]int64{"quay.io/x/app@sha256:aaa": 123}
			r.PlatformImageSize = 456
		}, false},
		{"並べ替えと同時に内容も追加", func(r *collector.Result) {
			r.PodImages = []string{"quay.io/x/b@sha256:2", "quay.io/x/app@sha256:aaa"}
			r.Operators[0].RelatedImages = []collector.RelatedImage{
				{Image: "nvcr.io/nvidia/z@sha256:9"}, {Image: "nvcr.io/nvidia/gpu-operator@sha256:op3"},
			}
		}, true}, // 純粋な並べ替えは TestHasDiffIgnoresOrdering で確認
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			old := baseResult()
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "collected-data.json"), raw, 0644); err != nil {
				t.Fatal(err)
			}

			newer := clone(t, old)
			c.mutate(newer)

			now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
			path, err := backupIfChanged(dir, old, newer, now)
			if err != nil {
				t.Fatal(err)
			}
			if c.wantBackup {
				want := filepath.Join(dir, "backups", "20261119-120000", "collected-data.json")
				if path != want {
					t.Fatalf("backup path = %q, want %q", path, want)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(raw) {
					t.Error("backup content differs from previous collected-data.json")
				}
			} else if path != "" {
				t.Errorf("unexpected backup %q", path)
			}
		})
	}
}

func TestHasDiffIgnoresOrdering(t *testing.T) {
	old := baseResult()
	old.PodImages = []string{"quay.io/x/a@sha256:1", "quay.io/x/b@sha256:2"}
	old.Operators[0].RelatedImages = []collector.RelatedImage{{Image: "x@sha256:1"}, {Image: "y@sha256:2"}}
	old.Operators = append(old.Operators, collector.OperatorInfo{PackageName: "nfd", CatalogSource: "redhat-operators"})
	old.CatalogSources = []collector.CatalogInfo{{Name: "a", Namespace: "ns"}, {Name: "b", Namespace: "ns"}}

	newer := clone(t, old)
	newer.PodImages = []string{"quay.io/x/b@sha256:2", "quay.io/x/a@sha256:1"}
	newer.Operators[0].RelatedImages = []collector.RelatedImage{{Image: "y@sha256:2"}, {Image: "x@sha256:1"}}
	newer.Operators[0], newer.Operators[1] = newer.Operators[1], newer.Operators[0]
	newer.CatalogSources[0], newer.CatalogSources[1] = newer.CatalogSources[1], newer.CatalogSources[0]

	if hasDiff(old, newer) {
		t.Error("ordering-only change should not be a diff")
	}
	if len(old.PodImages) != 2 || old.PodImages[0] != "quay.io/x/a@sha256:1" || newer.PodImages[0] != "quay.io/x/b@sha256:2" {
		t.Error("hasDiff must not modify its arguments")
	}
}
