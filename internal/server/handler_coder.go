package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
)

var coderReasoningEfforts = []api.ReasoningEffortOption{
	{Key: "NONE", Label: "NONE"},
	{Key: "LOW", Label: "LOW"},
	{Key: "MEDIUM", Label: "MEDIUM"},
	{Key: "HIGH", Label: "HIGH"},
}

func (s *Server) handleModelOptions(w http.ResponseWriter, r *http.Request) {
	response := s.buildModelOptions()
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) buildModelOptions() api.CoderModelOptionsResponse {
	models := s.listModelOptions()
	defaultModelKey := ""
	if s.deps.Models != nil {
		if model, _, err := s.deps.Models.Default(); err == nil {
			defaultModelKey = strings.TrimSpace(model.Key)
		}
	}
	return api.CoderModelOptionsResponse{
		Models:                 models,
		ReasoningEfforts:       append([]api.ReasoningEffortOption(nil), coderReasoningEfforts...),
		DefaultModelKey:        defaultModelKey,
		DefaultReasoningEffort: "MEDIUM",
	}
}

func (s *Server) listModelOptions() []api.CoderModelOption {
	models := []api.CoderModelOption{}
	if s.deps.Models == nil {
		return models
	}
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
	return models
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
