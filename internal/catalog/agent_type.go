package catalog

import (
	"fmt"
	"path/filepath"
	"strings"
)

const AgentTypeCoder = "CODER"

func normalizeAgentType(value string) (string, error) {
	agentType := strings.ToUpper(strings.TrimSpace(value))
	if agentType == "" {
		return "", nil
	}
	switch agentType {
	case AgentTypeCoder:
		return agentType, nil
	default:
		return "", fmt.Errorf("unsupported agent type %q", value)
	}
}

func parseAgentWorkspaceConfig(value any) AgentWorkspaceConfig {
	root := strings.TrimSpace(stringNode(mapNode(value)["root"]))
	if root == "" {
		return AgentWorkspaceConfig{}
	}
	return AgentWorkspaceConfig{Root: filepath.Clean(root)}
}

func validateAgentTypeWorkspace(agentType string, workspace AgentWorkspaceConfig) error {
	switch agentType {
	case AgentTypeCoder:
		root := strings.TrimSpace(workspace.Root)
		if root == "" {
			return fmt.Errorf("workspaceConfig.root is required for CODER agents")
		}
		if !filepath.IsAbs(root) {
			return fmt.Errorf("workspaceConfig.root must be an absolute path for CODER agents")
		}
	}
	return nil
}
