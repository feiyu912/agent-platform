package contracts

import (
	"context"

	"agent-platform/internal/api"
)

type EngineSelectionInput struct {
	Request api.QueryRequest
	Session QuerySession
}

type EngineSelection struct {
	Name   string
	Engine AgentEngine
}

type AgentEngineSelector interface {
	Select(context.Context, EngineSelectionInput) (EngineSelection, error)
}
