// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package cdc

import (
	"context"
	"sync"

	"github.com/pingcap/errors"
	timodel "github.com/pingcap/parser/model"
	pd "github.com/pingcap/pd/client"
	"github.com/pingcap/ticdc/cdc/entry"
	"github.com/pingcap/ticdc/cdc/model"
	"github.com/pingcap/ticdc/cdc/puller"
	"github.com/pingcap/ticdc/pkg/util"
	"golang.org/x/sync/errgroup"
)

//TODO: add tests
type ddlHandler struct {
	puller     puller.Puller
	resolvedTS uint64
	ddlJobs    []*timodel.Job

	mu     sync.Mutex
	wg     *errgroup.Group
	cancel func()
}

func newDDLHandler(pdCli pd.Client, checkpointTS uint64) *ddlHandler {
	// The key in DDL kv pair returned from TiKV is already memcompariable encoded,
	// so we set `needEncode` to false.
	puller := puller.NewPuller(pdCli, checkpointTS, []util.Span{util.GetDDLSpan(), util.GetAddIndexDDLSpan()}, false, nil)
	ctx, cancel := context.WithCancel(context.Background())
	h := &ddlHandler{
		puller: puller,
		cancel: cancel,
	}
	// Set it up so that one failed goroutine cancels all others sharing the same ctx
	errg, ctx := errgroup.WithContext(ctx)

	// FIXME: user of ddlHandler can't know error happen.
	errg.Go(func() error {
		return puller.Run(ctx)
	})

	rawDDLCh := puller.SortedOutput(ctx)

	errg.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case e := <-rawDDLCh:
				err := h.receiveDDL(e)
				if err != nil {
					return errors.Trace(err)
				}
			}
		}
	})
	h.wg = errg
	return h
}

func (h *ddlHandler) receiveDDL(rawDDL *model.RawKVEntry) error {
	if rawDDL.OpType == model.OpTypeResolved {
		h.mu.Lock()
		h.resolvedTS = rawDDL.Ts
		h.mu.Unlock()
		return nil
	}
	job, err := entry.UnmarshalDDL(rawDDL)
	if err != nil {
		return errors.Trace(err)
	}
	if job == nil || entry.SkipJob(job) {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.ddlJobs = append(h.ddlJobs, job)
	return nil
}

var _ OwnerDDLHandler = &ddlHandler{}

// PullDDL implements `roles.OwnerDDLHandler` interface.
func (h *ddlHandler) PullDDL() (uint64, []*timodel.Job, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := h.ddlJobs
	h.ddlJobs = nil
	return h.resolvedTS, result, nil
}

func (h *ddlHandler) Close() error {
	h.cancel()
	err := h.wg.Wait()
	return errors.Trace(err)
}
