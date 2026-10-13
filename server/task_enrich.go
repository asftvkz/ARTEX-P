package server

import (
	"context"
	"errors"
	"strconv"

	"github.com/Autumn-27/artex/enrich"
)

// 探测作为 Worker 正式操作的子任务运行，Scope.Close 完成前不得释放占用。
func (m *Manager) taskEnrichRoute(ctx context.Context, taskID int64) (enrich.Route, error) {
	id := strconv.FormatInt(taskID, 10)
	if !m.TaskProxyOperationAdmitted(ctx, id) {
		return enrich.Route{}, errors.New("资产探测缺少任务准入凭证")
	}
	row, err := m.pg.GetTask(taskID)
	if err != nil {
		return enrich.Route{}, err
	}
	addr, ca, err := m.TaskToolProxy(ctx, &Task{ID: id, ProxyMode: row.ProxyMode, ProxyID: row.ProxyID, ProxySnapshot: row.ProxySnapshot})
	if err != nil {
		return enrich.Route{}, err
	}
	return enrich.Route{ProxyURL: addr, CACertPath: ca}, nil
}
