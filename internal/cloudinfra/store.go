package cloudinfra

import "context"

// Store commits attempts before side effects. WithOperationLock serializes live
// workers without hiding checkpoints in the lock's open transaction.
type Store interface {
	CreatePreview(context.Context, *Operation) error
	OperationByID(context.Context, string, string) (*Operation, error)
	ServerInUse(context.Context, *Server) (bool, error)
	RetireServer(context.Context, *Server) error
	GetOperation(context.Context, string, string, string) (*Operation, error)
	ListOperations(context.Context, string, string) ([]*Operation, error)
	Accept(context.Context, *Operation) error
	SaveOperation(context.Context, *Operation) error
	GetServer(context.Context, string, string, string) (*Server, error)
	ListServers(context.Context, string, string) ([]*Server, error)
	SaveServer(context.Context, *Server) error
	Pending(context.Context) ([]*Operation, error)
	WithOperationLock(context.Context, string, func() error) error
}
