package llm

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
)

func TestSystemInitFingerprintStableAndToolOrderIndependent(t *testing.T) {
	session := fingerprintTestSession()
	toolsA := []api.ToolDetailResponse{
		{Name: "bash", Description: "run shell", Parameters: map[string]any{"type": "object"}},
		{Name: "datetime", Description: "get time", Parameters: map[string]any{"type": "object"}},
	}
	toolsB := []api.ToolDetailResponse{toolsA[1], toolsA[0]}

	first := ComputeSystemInitFingerprint(session, "main", toolsA)
	second := ComputeSystemInitFingerprint(session, "main", toolsB)
	if first == "" || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("unexpected fingerprint %q", first)
	}
	if first != second {
		t.Fatalf("expected tool order independent fingerprint, got %q and %q", first, second)
	}
}

func TestSystemInitFingerprintIgnoresRequestDynamicContext(t *testing.T) {
	session := fingerprintTestSession()
	changed := session
	changed.RequestID = "request-2"
	changed.RunID = "run-2"
	changed.StableMemoryContext = "Runtime Context: Stable Memory\n- changed"
	changed.SessionMemoryContext = "Runtime Context: Current Session\n- changed"
	changed.ObservationContext = "Runtime Context: Relevant Observations\n- changed"
	changed.RuntimeContext.References = []api.Reference{{Name: "new-ref"}}

	tools := []api.ToolDetailResponse{{Name: "bash", Description: "run shell"}}
	first := ComputeSystemInitFingerprint(session, "main", tools)
	second := ComputeSystemInitFingerprint(changed, "main", tools)
	if first != second {
		t.Fatalf("expected dynamic request context to be excluded, got %q and %q", first, second)
	}
}

func TestSystemInitFingerprintChangesWithPromptAndStage(t *testing.T) {
	session := fingerprintTestSession()
	tools := []api.ToolDetailResponse{{Name: "bash", Description: "run shell"}}
	base := ComputeSystemInitFingerprint(session, "main", tools)

	changedPrompt := session
	changedPrompt.SoulPrompt = "new soul"
	if got := ComputeSystemInitFingerprint(changedPrompt, "main", tools); got == base {
		t.Fatalf("expected prompt change to update fingerprint")
	}
	if got := ComputeSystemInitFingerprint(session, "plan", tools); got == base {
		t.Fatalf("expected stage change to update fingerprint")
	}
}

func TestCachedSystemInitConversions(t *testing.T) {
	profiles := BuildSystemInitProfiles(fingerprintTestSession(), api.QueryRequest{ChatID: "chat-1", Message: "hello"}, []api.ToolDetailResponse{
		{Name: "bash", Description: "run shell", Parameters: map[string]any{"type": "object"}},
	})
	if len(profiles) != 1 {
		t.Fatalf("expected one profile, got %#v", profiles)
	}
	systemMessage, ok := cachedSystemMessageToOpenAI(profiles[0].SystemMessage)
	if !ok || systemMessage.Role != "system" {
		t.Fatalf("unexpected cached system message %#v", systemMessage)
	}
	specs, err := cachedToolSpecsToOpenAI(profiles[0].Tools)
	if err != nil {
		t.Fatalf("cached tool specs: %v", err)
	}
	if len(specs) != 1 || specs[0].Function.Name != "bash" {
		t.Fatalf("unexpected specs %#v", specs)
	}
	if !reflect.DeepEqual(openAIToolSpecsToAny(specs), profiles[0].Tools) {
		t.Fatalf("expected tools to round trip, got %#v", openAIToolSpecsToAny(specs))
	}
}

func fingerprintTestSession() contracts.QuerySession {
	return contracts.QuerySession{
		RequestID:        "request-1",
		RunID:            "run-1",
		ChatID:           "chat-1",
		AgentKey:         "agent",
		AgentName:        "Agent",
		AgentRole:        "helper",
		AgentDescription: "does work",
		ModelKey:         "mock-model",
		ToolNames:        []string{"datetime", "bash"},
		Mode:             "REACT",
		SkillKeys:        []string{"skill-a"},
		ContextTags:      []string{"system", "session"},
		PromptAppend:     contracts.DefaultPromptAppendConfig(),
		SoulPrompt:       "soul",
		AgentsPrompt:     "agents",
		PlanPrompt:       "plan",
		ExecutePrompt:    "execute",
		SummaryPrompt:    "summary",
		ResolvedStageSettings: contracts.PlanExecuteSettings{
			Plan:    contracts.StageSettings{SystemPrompt: "plan system"},
			Execute: contracts.StageSettings{SystemPrompt: "execute system"},
			Summary: contracts.StageSettings{SystemPrompt: "summary system"},
		},
		RuntimeEnvOverrides: map[string]string{"FOO": "bar"},
	}
}
