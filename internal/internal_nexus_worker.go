package internal

import "github.com/nexus-rpc/sdk-go/nexus"

type OperationHandler interface {
	GetName() string
	nexus.Handler
	// TODO: enforce only internal implementations of this without causing import cycles
}

func (aw *AggregatedWorker) RegisterOperation(OperationHandler) {
}
