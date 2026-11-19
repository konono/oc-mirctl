package cli

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name         string
		v, c, d      string
		info         *debug.BuildInfo
		wantV, wantC string
		wantD        string
	}{
		{
			name: "make build (ldflags 埋め込み済み) はそのまま",
			v:    "v1.2.0", c: "abc1234", d: "2026-11-19T00:00:00Z",
			info:  &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}},
			wantV: "v1.2.0", wantC: "abc1234", wantD: "2026-11-19T00:00:00Z",
		},
		{
			name: "go install @main (疑似バージョン)",
			v:    "dev", c: "unknown", d: "unknown",
			info:  &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261119072208-6a469f569639"}},
			wantV: "v0.0.0-20261119072208-6a469f569639", wantC: "unknown", wantD: "unknown",
		},
		{
			name: "リポジトリ内の go build (vcs 情報あり、変更あり)",
			v:    "dev", c: "unknown", d: "unknown",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "6a469f5696396f0e448eb11b5c83a7c8a9d05289"},
				{Key: "vcs.time", Value: "2026-11-19T07:22:09Z"},
				{Key: "vcs.modified", Value: "true"},
			}},
			wantV: "dev", wantC: "6a469f569639-dirty", wantD: "2026-11-19T07:22:09Z",
		},
		{
			name: "ビルド情報なし",
			v:    "dev", c: "unknown", d: "unknown", info: nil,
			wantV: "dev", wantC: "unknown", wantD: "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, c, d := resolveVersion(tc.v, tc.c, tc.d, tc.info)
			if v != tc.wantV || c != tc.wantC || d != tc.wantD {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)", v, c, d, tc.wantV, tc.wantC, tc.wantD)
			}
		})
	}
}
