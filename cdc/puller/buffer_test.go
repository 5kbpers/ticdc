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

package puller

import (
	"context"
	"sync"
	"time"

	"github.com/pingcap/check"
	"github.com/pingcap/ticdc/cdc/model"
	"github.com/pingcap/ticdc/pkg/util"
)

type bufferSuite struct{}

var _ = check.Suite(&bufferSuite{})

func (bs *bufferSuite) TestCanAddAndReadEntriesInOrder(c *check.C) {
	b := makeBuffer()
	ctx := context.Background()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		first, err := b.Get(ctx)
		c.Assert(err, check.IsNil)
		c.Assert(first.Val.Ts, check.Equals, uint64(110))
		second, err := b.Get(ctx)
		c.Assert(err, check.IsNil)
		c.Assert(second.Resolved.ResolvedTs, check.Equals, uint64(111))
	}()

	err := b.AddEntry(ctx, model.RegionFeedEvent{
		Val: &model.RawKVEntry{Ts: 110},
	})
	c.Assert(err, check.IsNil)
	err = b.AddEntry(ctx, model.RegionFeedEvent{
		Resolved: &model.ResolvedSpan{
			Span:       util.Span{},
			ResolvedTs: 111,
		},
	})
	c.Assert(err, check.IsNil)

	wg.Wait()
}

func (bs *bufferSuite) TestWaitsCanBeCanceled(c *check.C) {
	b := makeBuffer()
	ctx := context.Background()

	timeout, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		for {
			err := b.AddEntry(timeout, model.RegionFeedEvent{
				Resolved: &model.ResolvedSpan{
					Span:       util.Span{},
					ResolvedTs: 111,
				},
			})
			if err == context.DeadlineExceeded {
				close(stopped)
				return
			}
			c.Assert(err, check.Equals, nil)
		}
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Millisecond):
		c.Fatal("AddEntry doesn't stop in time.")
	}
}
