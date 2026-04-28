package memory

import "testing"

func TestNormalizeCategoryBusinessAliases(t *testing.T) {
	tests := map[string]string{
		"":              CategoryGeneral,
		"Preference":    CategoryPreference,
		"preferences":   CategoryPreference,
		"constraints":   CategoryConstraint,
		"profiles":      CategoryProfile,
		"runbook":       CategoryWorkflow,
		"decisions":     CategoryDecision,
		"terminology":   CategoryGlossary,
		"open-question": CategoryUnresolvedIssue,
		"blocked":       CategoryUnresolvedIssue,
	}

	for input, want := range tests {
		if got := normalizeCategory(input); got != want {
			t.Fatalf("normalizeCategory(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeCategoryPreservesCustomCategory(t *testing.T) {
	tests := map[string]string{
		"ops_checklist":    "ops_checklist",
		"memory.operation": "memory.operation",
		"project:alpha":    "project:alpha",
		"user_preference":  "user_preference",
	}

	for input, want := range tests {
		if got := normalizeCategory(input); got != want {
			t.Fatalf("normalizeCategory(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestClassifyObservationCategoryBusinessSemantics(t *testing.T) {
	tests := map[string]string{
		"用户偏好默认中文输出":         CategoryPreference,
		"必须遵守团队发布权限规则":       CategoryConstraint,
		"已确认采用 SQLite 作为主存储": CategoryDecision,
		"待确认线上角色权限风险":        CategoryUnresolvedIssue,
		"发布步骤需要先打包再同步":       CategoryWorkflow,
		"CRM 是内部客户系统缩写":      CategoryGlossary,
		"todo: 补充回归测试":       CategoryTodo,
		"fixed upload bug":   CategoryBugfix,
	}

	for input, want := range tests {
		if got := classifyObservationCategory(input); got != want {
			t.Fatalf("classifyObservationCategory(%q) = %q, want %q", input, got, want)
		}
	}
}
