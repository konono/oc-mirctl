package update

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// devBuildRe は開発ビルドのバージョン表記にマッチする:
//   - make build の git describe --tags --always --dirty (例: v0.1.0-1-gabcdef1、v0.1.0-dirty)
//   - 作業ツリーに変更がある状態の go build (例: v0.0.0-20261119073401-c56b2626fbe1+dirty)
//
// これらは semver のプレリリース / ビルドメタデータとして解釈できてしまうが、リリースとの
// 大小に意味が無い (例: v0.1.0-1-gabcdef1 は v0.1.0 より新しいのに、semver では古いと判定される)。
var devBuildRe = regexp.MustCompile(`-\d+-g[0-9a-f]+(-dirty)?$|-dirty$|\+dirty$`)

// semver は "vX.Y.Z[-prerelease][+build]" を表す
type semver struct {
	nums [3]int
	pre  string
}

func parseSemver(s string) (semver, error) {
	var v semver
	orig := s
	if devBuildRe.MatchString(s) {
		return v, fmt.Errorf("バージョン %q は開発ビルドのためリリースと比較できません", orig)
	}
	s = strings.TrimPrefix(s, "v")
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "-"); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("バージョン %q は X.Y.Z 形式ではありません", orig)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("バージョン %q は X.Y.Z 形式ではありません", orig)
		}
		v.nums[i] = n
	}
	return v, nil
}

// compareVersions は a と b を semver として比較する (a > b なら正、a < b なら負)。
// go install の疑似バージョン (v0.0.0-<時刻>-<コミット>) はプレリリースとして扱われ、
// 同じ X.Y.Z のリリースより小さい。どちらかが semver でなければエラーを返す。
func compareVersions(a, b string) (int, error) {
	va, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	for i := range va.nums {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] > vb.nums[i] {
				return 1, nil
			}
			return -1, nil
		}
	}
	return comparePrerelease(va.pre, vb.pre), nil
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1 // プレリリース無しのほうが大きい
	case b == "":
		return -1
	}
	ia, ib := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		na, errA := strconv.Atoi(ia[i])
		nb, errB := strconv.Atoi(ib[i])
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				if na > nb {
					return 1
				}
				return -1
			}
		case errA == nil:
			return -1
		case errB == nil:
			return 1
		default:
			if c := strings.Compare(ia[i], ib[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(ia) > len(ib):
		return 1
	case len(ia) < len(ib):
		return -1
	}
	return 0
}
