package server

import (
	"fmt"
	"regexp"

	"github.com/Autumn-27/artex/selfupdate"
)

var candidateVersion = regexp.MustCompile(`^dev-[0-9a-f]{12}$`)
var stableReleaseVersion = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// releaseUpdateOffer 将候选切换与正式版本比较分开，避免改变容器启动时的选版规则。
func releaseUpdateOffer(current string, rel *selfupdate.Release) (available, comparable, releaseSwitch bool, reason string) {
	if rel == nil || rel.Draft || rel.Prerelease || !stableReleaseVersion.MatchString(rel.TagName) {
		return false, false, false, "更新源未提供有效的正式发布版本"
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if comparable {
		return cmp < 0, true, false, ""
	}
	if candidateVersion.MatchString(current) {
		return true, false, true, "当前为 dev 候选，可切换到最新正式版；无法比较两者先后，请先核对更新日志。"
	}
	return false, false, false, fmt.Sprintf("当前版本 %q 无法识别为正式版或 dev 候选，已禁用一键更新", current)
}
