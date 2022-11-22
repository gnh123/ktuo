package etcd

import (
	"context"
	"errors"
	"time"

	"github.com/gnh123/scheduler/model"
	"github.com/gnh123/scheduler/utils"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// 使用分布式锁
func (e *EtcdStore) AssignMutex(ctx context.Context, oneTask model.KeyVal, failover bool) {
	e.AssignMutexWithCb(ctx, oneTask, failover, nil)
}

func (e *EtcdStore) LockUnlock(ctx context.Context, key string, cb func() error) error {

	mutexName := model.AssignTaskMutex(key)

	s, err := concurrency.NewSession(e.defaultClient)
	if err != nil {
		return err
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	// 创建分布式锁
	l := concurrency.NewMutex(s, mutexName)

	if err := l.Lock(ctx); err != nil {
		e.Debug().Msgf("assign lock:%s\n", err)
		return err
	}

	defer l.Unlock(ctx)

	return cb()
}

func (e *EtcdStore) TryLockUnlock(ctx context.Context, key string, cb func() error) error {

	mutexName := model.AssignTaskMutex(key)

	s, err := concurrency.NewSession(e.defaultClient)
	if err != nil {
		return err
	}

	defer s.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	// 创建分布式锁
	l := concurrency.NewMutex(s, mutexName)

	if err := l.TryLock(ctx); err != nil {
		e.Debug().Msgf("assign trylock:%s\n", err)
		return err
	}
	defer l.Unlock(ctx)

	return cb()
}

func (e *EtcdStore) AssignMutexWithCb(ctx context.Context, oneTask model.KeyVal, failover bool, cb func()) error {
	return e.TryLockUnlock(ctx, oneTask.Key, func() error {

		if err := e.assign(ctx, oneTask, failover); err != nil {
			e.Warn().Msgf("assign err:%v\n", err)
		}

		if cb != nil {
			cb()
		}
		return nil
	})

}

// mjobs子命令的的入口函数
// 随机选择一个runtimeNode
func (e *EtcdStore) selectRuntimeNode() (string, error) {

	if e.runtimeNode.Len() == 0 {
		e.Warn().Msgf("assign.runtimeNodes.size is 0\n")
		return "", errors.New("assign.runtimeNodes.size is 0")
	}

	runtimeNodes := e.runtimeNode.Keys()

	return utils.SliceRandOne(runtimeNodes), nil
}

// 分配任务的逻辑
func (e *EtcdStore) assign(ctx context.Context, oneTask model.KeyVal, failover bool) error {
	e.Debug().Msgf("call assign, key:%s, state:%s\n", oneTask.Key, oneTask.State.State)

	// 如果有两个mjobs访问同一个事情，不是并发访问，可能两个进程都进入这个流程
	// 所以这里要判断任务状态，running状态就直接返回, 这样可以解决任务重复分放的问题
	rspState, err := e.defaultKVC.Get(ctx, oneTask.Key)
	if err != nil {
		e.Warn().Msgf("failover:(%t) ", failover)
		return err
	}
	state, err := model.ValueToState(rspState.Kvs[0].Value)
	if !failover {
		if err != nil {
			e.Warn().Msgf("value to state:%s ", err)
			return err
		}
		if state.IsRunning() {
			return nil
		}
	} else {

	}
	kv := oneTask
	runtimeNode, err := e.selectRuntimeNode()
	if err != nil {
		return err
	}
	// 从状态信息里面获取tastName
	taskName := model.TaskName(kv.Key)
	if taskName == "" {
		e.Debug().Msgf("taskName is empty, %s\n", kv.Key)
		return errors.New("taskName is empty")
	}

	e.Debug().Msgf("assign, taskName %s, action:%s\n", taskName, oneTask.State.Action)
	rsp, err := e.defaultKVC.Get(ctx, model.FullGlobalTask(taskName), clientv3.WithRev(int64(oneTask.Version)))
	if err != nil {
		e.Error().Msgf("get global task path fail:%s\n", err)
		return err
	}

	if len(rsp.Kvs) == 0 {
		e.Warn().Msgf("get %s value is nil\n", model.FullGlobalTask(taskName))
		return err
	}

	// 如果是删除任务，没有在运行中的删除，直接删除
	if oneTask.State.IsRemove() && !oneTask.State.InRuntime && !oneTask.State.IsFailed() {
		e.defaultKVC.Delete(ctx, model.FullGlobalTask(taskName))
		e.defaultKVC.Delete(ctx, model.FullGlobalTaskState(taskName))
		return nil
	}

	// 如果runtimeNode绑定好，除了出错，或者新建，会取目前绑定的runtimeNode直接使用
	if !failover && !state.IsCreate() && !state.IsFailed() {
		e.Debug().Msgf("state:%v\n", state)
		runtimeNode = state.RuntimeNode
	}

	if state.IsOneRuntime() {

		if err = e.UpdateLocalAndGlobal(ctx, taskName, runtimeNode, rspState, state.Action); err != nil {
			return err
		}

		// 更新状态中的值
		e.Debug().Msgf("oneRuntime:key(%s):value(%s)\n", model.FullGlobalTaskState(taskName), runtimeNode)
	} else if state.IsBroadcast() {

		e.runtimeNode.Range(func(key, val string) bool {
			err = e.UpdateLocalAndGlobal(ctx, taskName, runtimeNode, rspState, state.Action)
			return err == nil
		})
	} else {
		e.Warn().Msgf("Unknown kind:%s\n", state.Kind)
	}
	return err
}
