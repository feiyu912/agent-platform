package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

var coderReasoningEfforts = []api.ReasoningEffortOption{
	{Key: "NONE", Label: "NONE"},
	{Key: "LOW", Label: "LOW"},
	{Key: "MEDIUM", Label: "MEDIUM"},
	{Key: "HIGH", Label: "HIGH"},
}

func (s *Server) handleModelOptions(w http.ResponseWriter, r *http.Request) {
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	response, err := s.buildModelOptions(agentKey)
	if err != nil {
		s.writeModelOptionsHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) buildModelOptions(agentKey string) (api.CoderModelOptionsResponse, error) {
	agentDef, err := s.coderAgentDefinition(agentKey)
	if err != nil {
		return api.CoderModelOptionsResponse{}, err
	}
	models := []api.CoderModelOption{}
	if s.deps.Models != nil {
		for _, model := range s.deps.Models.List() {
			models = append(models, api.CoderModelOption{
				Key:           model.Key,
				Provider:      model.Provider,
				ModelID:       model.ModelID,
				Protocol:      model.Protocol,
				IsReasoner:    model.IsReasoner,
				IsVision:      model.IsVision,
				ContextWindow: model.ContextWindow,
			})
		}
	}
	settings := contracts.ResolvePlanExecuteSettings(
		agentDef.StageSettings,
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
	)
	return api.CoderModelOptionsResponse{
		Models:                 models,
		ReasoningEfforts:       append([]api.ReasoningEffortOption(nil), coderReasoningEfforts...),
		DefaultModelKey:        firstNonBlank(settings.Execute.ModelKey, settings.Plan.ModelKey, settings.Summary.ModelKey, agentDef.ModelKey),
		DefaultReasoningEffort: defaultCoderReasoningEffort(settings),
	}, nil
}

func (s *Server) coderAgentDefinition(agentKey string) (catalog.AgentDefinition, error) {
	if s.deps.Registry == nil {
		return catalog.AgentDefinition{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent registry is not configured")
	}
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return catalog.AgentDefinition{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey is required")
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return catalog.AgentDefinition{}, newAgentStatusError(http.StatusNotFound, "not_found", "agent not found")
	}
	if !strings.EqualFold(strings.TrimSpace(def.Mode), catalog.AgentModeCoder) {
		return catalog.AgentDefinition{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agent must be CODER")
	}
	return def, nil
}

func defaultCoderReasoningEffort(settings contracts.PlanExecuteSettings) string {
	if effort, ok := normalizeCoderReasoningEffort(firstNonBlank(
		settings.Execute.ReasoningEffort,
		settings.Plan.ReasoningEffort,
		settings.Summary.ReasoningEffort,
	)); ok {
		return effort
	}
	return "MEDIUM"
}

func normalizeCoderReasoningEffort(value string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "", true
	case "NONE":
		return "NONE", true
	case "LOW":
		return "LOW", true
	case "MEDIUM":
		return "MEDIUM", true
	case "HIGH":
		return "HIGH", true
	default:
		return "", false
	}
}

func (s *Server) writeModelOptionsHTTPError(w http.ResponseWriter, err error) {
	var status int
	message := err.Error()
	if statusErr, ok := err.(agentStatusError); ok {
		status = statusErr.status
		message = statusErr.message
	}
	if status == 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, api.Failure(status, message))
}
