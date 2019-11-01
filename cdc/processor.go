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
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tidb-cdc/cdc/kv"
	"github.com/pingcap/tidb-cdc/cdc/model"
	"github.com/pingcap/tidb-cdc/cdc/schema"
	"github.com/pingcap/tidb-cdc/cdc/txn"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

var fCreateSchema = createSchemaStore

// Processor is used to push sync progress and calculate the checkpointTS
// How to use it:
// 1. Call SetInputChan to set a rawTxn input channel
//        (you can call SetInputChan many time to set multiple input channel)
// 2. Push rawTxn into rawTxn input channel
// 3. Pull ProcessorEntry from ResolvedChan, RawTxn is included in ProcessorEntry
// 4. execute the RawTxn in ProcessorEntry
// 5. Push ProcessorEntry to ExecutedChan
type Processor interface {
	// SetInputChan receives a table and listens a channel
	SetInputChan(tableID uint64, inputTxn <-chan txn.RawTxn) error
	// ResolvedChan returns a channel, which output the resolved transaction or resolvedTS
	ResolvedChan() <-chan ProcessorEntry
	// ExecutedChan returns a channel, when a transaction is executed,
	// you should put the transaction into this channel,
	// processor will calculate checkpointTS according to this channel
	ExecutedChan() chan<- ProcessorEntry
	// Close closes the processor
	Close()
}

// ProcessorTSRWriter reads or writes the resolvedTS and checkpointTS from the storage
type ProcessorTSRWriter interface {
	// WriteResolvedTS writes the loacl resolvedTS into the storage
	WriteResolvedTS(ctx context.Context, resolvedTS uint64) error
	// WriteCheckpointTS writes the checkpointTS into the storage
	WriteCheckpointTS(ctx context.Context, checkpointTS uint64) error
	// ReadGlobalResolvedTS reads the global resolvedTS from the storage
	ReadGlobalResolvedTS(ctx context.Context) (uint64, error)
}

type txnChannel struct {
	inputTxn   <-chan txn.RawTxn
	outputTxn  chan txn.RawTxn
	putBackTxn *txn.RawTxn
}

func (p *txnChannel) Forward(tableID uint64, ts uint64, entryC chan<- ProcessorEntry) {
	if p.putBackTxn != nil {
		t := *p.putBackTxn
		if t.TS > ts {
			return
		}
		p.putBackTxn = nil
		entryC <- NewProcessorDMLsEntry(t.Entries, t.TS)
	}
	for t := range p.outputTxn {
		if t.TS > ts {
			p.PutBack(t)
			return
		}
		entryC <- NewProcessorDMLsEntry(t.Entries, t.TS)
	}
	log.Info("Input channel of table closed", zap.Uint64("tableID", tableID))
}

func (p *txnChannel) PutBack(t txn.RawTxn) {
	if p.putBackTxn != nil {
		log.Fatal("can not put back raw txn continuously")
	}
	p.putBackTxn = &t
}

func newTxnChannel(inputTxn <-chan txn.RawTxn, chanSize int, handleResolvedTS func(uint64)) *txnChannel {
	tc := &txnChannel{
		inputTxn:  inputTxn,
		outputTxn: make(chan txn.RawTxn, chanSize),
	}
	go func() {
		defer close(tc.outputTxn)
		for {
			t, ok := <-tc.inputTxn
			if !ok {
				return
			}
			handleResolvedTS(t.TS)
			tc.outputTxn <- t
		}
	}()
	return tc
}

type ProcessorEntryType int

const (
	ProcessorEntryUnknown ProcessorEntryType = iota
	ProcessorEntryDMLS
	ProcessorEntryResolved
)

type ProcessorEntry struct {
	Entries []*kv.RawKVEntry
	TS      uint64
	Typ     ProcessorEntryType
}

func NewProcessorDMLsEntry(entries []*kv.RawKVEntry, ts uint64) ProcessorEntry {
	return ProcessorEntry{
		Entries: entries,
		TS:      ts,
		Typ:     ProcessorEntryDMLS,
	}
}

func NewProcessorResolvedEntry(ts uint64) ProcessorEntry {
	return ProcessorEntry{
		TS:  ts,
		Typ: ProcessorEntryResolved,
	}
}

type processorImpl struct {
	captureID    string
	changefeedID string
	changefeed   model.ChangeFeedDetail

	mounter *txn.Mounter

	tableResolvedTS sync.Map
	tsRWriter       ProcessorTSRWriter
	resolvedEntries chan ProcessorEntry
	executedEntries chan ProcessorEntry

	tableInputChans map[uint64]*txnChannel
	inputChansLock  sync.RWMutex
	wg              *errgroup.Group

	closed chan struct{}
}

func NewProcessor(tsRWriter ProcessorTSRWriter, pdEndpoints []string, changefeed model.ChangeFeedDetail, captureID, changefeedID string) (Processor, error) {
	schema, err := fCreateSchema(pdEndpoints)
	if err != nil {
		return nil, errors.Trace(err)
	}

	// TODO: get time zone from config
	mounter, err := txn.NewTxnMounter(schema, time.UTC)
	if err != nil {
		return nil, errors.Trace(err)
	}

	wg, _ := errgroup.WithContext(context.Background())
	p := &processorImpl{
		captureID:    captureID,
		changefeedID: changefeedID,
		changefeed:   changefeed,

		mounter: mounter,

		tsRWriter: tsRWriter,
		// TODO set the channel size
		resolvedEntries: make(chan ProcessorEntry),
		// TODO set the channel size
		executedEntries: make(chan ProcessorEntry),

		tableInputChans: make(map[uint64]*txnChannel),
		closed:          make(chan struct{}),
		wg:              wg,
	}

	wg.Go(func() error {
		p.localResolvedWorker()
		return nil
	})
	wg.Go(func() error {
		p.checkpointWorker()
		return nil
	})
	wg.Go(func() error {
		p.globalResolvedWorker()
		return nil
	})
	return p, nil
}

func (p *processorImpl) localResolvedWorker() {
	for {
		select {
		case <-p.closed:
			log.Info("Local resolved worker exited")
			return
		case <-time.After(3 * time.Second):
			minResolvedTs := uint64(math.MaxUint64)
			p.tableResolvedTS.Range(func(key, value interface{}) bool {
				resolvedTS := value.(uint64)
				if minResolvedTs > resolvedTS {
					minResolvedTs = resolvedTS
				}
				return true
			})
			if minResolvedTs == uint64(math.MaxUint64) {
				// no table in this processor
				continue
			}
			// TODO: refine context management
			err := p.tsRWriter.WriteResolvedTS(context.Background(), minResolvedTs)
			if err != nil {
				log.Error("Local resolved worker: write resolved ts failed", zap.Error(err))
			}
		}
	}
}

func (p *processorImpl) checkpointWorker() {
	checkpointTS := uint64(0)
	for {
		select {
		case e, ok := <-p.executedEntries:
			if !ok {
				log.Info("Checkpoint worker exited")
				return
			}
			if e.Typ == ProcessorEntryResolved {
				checkpointTS = e.TS
			}
		case <-time.After(3 * time.Second):
			// TODO: better context management
			err := p.tsRWriter.WriteCheckpointTS(context.Background(), checkpointTS)
			if err != nil {
				log.Error("Checkpoint worker: write checkpoint ts failed", zap.Error(err))
			}
		}
	}
}

func (p *processorImpl) globalResolvedWorker() {
	log.Info("Global resolved worker started")
	lastGlobalResolvedTS := uint64(0)
	ctx := context.Background()
	wg, _ := errgroup.WithContext(ctx)
	retryCfg := backoff.WithMaxRetries(
		backoff.WithContext(
			backoff.NewExponentialBackOff(), ctx),
		3,
	)
	for {
		select {
		case <-p.closed:
			close(p.resolvedEntries)
			close(p.executedEntries)
			log.Info("Global resolved worker exited")
			return
		default:
		}
		var globalResolvedTS uint64
		err := backoff.Retry(func() error {
			var err error
			globalResolvedTS, err = p.tsRWriter.ReadGlobalResolvedTS(context.Background())
			if err != nil {
				log.Error("Global resolved worker: read global resolved ts failed", zap.Error(err))
			}
			return err
		}, retryCfg)
		if err != nil {
			return
		}
		if lastGlobalResolvedTS == globalResolvedTS {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		lastGlobalResolvedTS = globalResolvedTS
		p.inputChansLock.RLock()
		for table, input := range p.tableInputChans {
			table := table
			input := input
			globalResolvedTS := globalResolvedTS
			wg.Go(func() error {
				input.Forward(table, globalResolvedTS, p.resolvedEntries)
				return nil
			})
		}
		p.inputChansLock.RUnlock()
		wg.Wait()
		p.resolvedEntries <- NewProcessorResolvedEntry(globalResolvedTS)
	}
}

func (p *processorImpl) SetInputChan(tableID uint64, inputTxn <-chan txn.RawTxn) error {
	tc := newTxnChannel(inputTxn, 64, func(resolvedTS uint64) {
		p.tableResolvedTS.Store(tableID, resolvedTS)
	})
	p.inputChansLock.Lock()
	defer p.inputChansLock.Unlock()
	if _, exist := p.tableInputChans[tableID]; exist {
		return errors.Errorf("this chan is already exist, tableID: %d", tableID)
	}
	p.tableInputChans[tableID] = tc
	return nil
}

func (p *processorImpl) ResolvedChan() <-chan ProcessorEntry {
	return p.resolvedEntries
}

func (p *processorImpl) ExecutedChan() chan<- ProcessorEntry {
	return p.executedEntries
}

func (p *processorImpl) Close() {
	close(p.closed)
	p.wg.Wait()
}

func createSchemaStore(pdEndpoints []string) (*schema.Schema, error) {
	// here we create another pb client,we should reuse them
	kvStore, err := createTiStore(strings.Join(pdEndpoints, ","))
	if err != nil {
		return nil, err
	}
	jobs, err := kv.LoadHistoryDDLJobs(kvStore)
	if err != nil {
		return nil, err
	}
	schema, err := schema.NewSchema(jobs, false)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return schema, nil
}
