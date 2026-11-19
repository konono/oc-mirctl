package update

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.2.0", "v0.1.0", 1},
		{"v0.1.0", "v0.1.0", 0},
		{"0.1.0", "v0.1.0", 0},
		{"v0.10.0", "v0.9.9", 1},
		{"v1.0.0", "v0.99.99", 1},
		// go install の疑似バージョンは同じ X.Y.Z のリリースより小さい
		{"v0.1.0", "v0.0.0-20261119073401-c56b2626fbe1", 1},
		{"v0.1.0", "v0.1.1-0.20261119073401-c56b2626fbe1", -1},
		{"v0.1.0", "v0.1.0+build.5", 0}, // +dirty 以外のビルドメタデータは無視
		{"v1.0.0-rc.1", "v1.0.0-rc.10", -1},
	}
	for _, c := range cases {
		got, err := compareVersions(c.a, c.b)
		if err != nil {
			t.Errorf("compareVersions(%q, %q): %v", c.a, c.b, err)
			continue
		}
		if got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{
		"dev", "c56b262-dirty", "v1.2", "(devel)",
		// make build (git describe --tags --always --dirty) のタグ後の開発ビルド
		"v0.1.0-1-gabcdef1", "v0.1.0-12-g0123abc-dirty", "v0.1.0-dirty",
		// 作業ツリーに変更がある go build
		"v0.0.0-20261119073401-c56b2626fbe1+dirty",
	} {
		if _, err := compareVersions("v0.1.0", bad); err == nil {
			t.Errorf("compareVersions with %q should fail", bad)
		}
	}
}
