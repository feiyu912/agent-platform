package runctl

import "agent-platform/internal/contracts"

type RunControl = contracts.RunControl
type SubmitResult = contracts.SubmitResult
type InMemoryRunManager = contracts.InMemoryRunManager

func NewInMemoryRunManager() *InMemoryRunManager {
	return contracts.NewInMemoryRunManager()
}
