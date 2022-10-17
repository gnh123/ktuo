package executer

import (
	"context"

	"github.com/gnh123/scheduler/model"
)

// 留空，下个版本实现
func init() {
	Register("grpc", createGRPCExecuter)
}

type grpcExecuter struct {
}

func (s *grpcExecuter) Cancel() error {
	return nil
}

func (s *grpcExecuter) Run() error {
	return nil
}

func createGRPCExecuter(ctx context.Context, param *model.ExecutorParam) Executer {
	return nil
}