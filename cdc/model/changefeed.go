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

package model

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/pingcap/errors"
	"github.com/pingcap/tidb-tools/pkg/filter"
	"github.com/pingcap/tidb/store/tikv/oracle"
)

// ChangeFeedDetail describes the detail of a ChangeFeed
type ChangeFeedDetail struct {
	SinkURI    string            `json:"sink-uri"`
	Opts       map[string]string `json:"opts"`
	CreateTime time.Time         `json:"create-time"`
	// Start sync at this commit ts if `StartTs` is specify or using the CreateTime of changefeed.
	StartTs uint64 `json:"start-ts"`
	// The ChangeFeed will exits until sync to timestamp TargetTs
	TargetTs uint64 `json:"target-ts"`
	// used for admin job notification, trigger watch event in capture
	AdminJobType AdminJobType    `json:"admin-job-type"`
	Info         *ChangeFeedInfo `json:"-"`

	filter *filter.Filter
	Config *ReplicaConfig `json:"config"`
}

func (detail *ChangeFeedDetail) getConfig() *ReplicaConfig {
	if detail.Config == nil {
		detail.Config = &ReplicaConfig{}
	}
	return detail.Config
}

func (detail *ChangeFeedDetail) getFilter() *filter.Filter {
	if detail.filter == nil {
		rules := detail.getConfig().FilterRules
		detail.filter = filter.New(detail.getConfig().FilterCaseSensitive, rules)
	}
	return detail.filter
}

// ShouldIgnoreTxn returns true is the given txn should be ignored
func (detail *ChangeFeedDetail) ShouldIgnoreTxn(t *Txn) bool {
	for _, ignoreTs := range detail.getConfig().IgnoreTxnCommitTs {
		if ignoreTs == t.Ts {
			return true
		}
	}
	return false
}

// ShouldIgnoreTable returns true if the specified table should be ignored by this change feed.
// Set `tbl` to an empty string to test against the whole database.
func (detail *ChangeFeedDetail) ShouldIgnoreTable(db, tbl string) bool {
	if IsSysSchema(db) {
		return true
	}
	f := detail.getFilter()
	// TODO: Change filter to support simple check directly
	left := f.ApplyOn([]*filter.Table{{Schema: db, Name: tbl}})
	return len(left) == 0
}

// FilterTxn removes DDL/DMLs that's not wanted by this change feed.
// CDC only supports filtering by database/table now.
func (detail *ChangeFeedDetail) FilterTxn(t *Txn) {
	if t.IsDDL() {
		if detail.ShouldIgnoreTable(t.DDL.Database, t.DDL.Table) {
			t.DDL = nil
		}
	} else {
		var filteredDMLs []*DML
		for _, dml := range t.DMLs {
			if !detail.ShouldIgnoreTable(dml.Database, dml.Table) {
				filteredDMLs = append(filteredDMLs, dml)
			}
		}
		t.DMLs = filteredDMLs
	}
}

// GetStartTs returns StartTs if it's  specified or using the CreateTime of changefeed.
func (detail *ChangeFeedDetail) GetStartTs() uint64 {
	if detail.StartTs > 0 {
		return detail.StartTs
	}

	return oracle.EncodeTSO(detail.CreateTime.Unix() * 1000)
}

// GetTargetTs returns TargetTs if it's specified, otherwise MaxUint64 is returned.
func (detail *ChangeFeedDetail) GetTargetTs() uint64 {
	if detail.TargetTs > 0 {
		return detail.TargetTs
	}
	return uint64(math.MaxUint64)
}

// GetCheckpointTs returns the checkpoint ts of changefeed.
func (detail *ChangeFeedDetail) GetCheckpointTs() uint64 {
	if detail.Info != nil {
		return detail.Info.CheckpointTs
	}

	return detail.GetStartTs()
}

// Marshal returns the json marshal format of a ChangeFeedDetail
func (detail *ChangeFeedDetail) Marshal() (string, error) {
	data, err := json.Marshal(detail)
	return string(data), errors.Trace(err)
}

// Unmarshal unmarshals into *ChangeFeedDetail from json marshal byte slice
func (detail *ChangeFeedDetail) Unmarshal(data []byte) error {
	err := json.Unmarshal(data, &detail)
	return errors.Annotatef(err, "Unmarshal data: %v", data)
}

// IsSysSchema returns true if the given schema is a system schema
func IsSysSchema(db string) bool {
	db = strings.ToUpper(db)
	for _, schema := range []string{"INFORMATION_SCHEMA", "PERFORMANCE_SCHEMA", "MYSQL", "METRIC_SCHEMA"} {
		if schema == db {
			return true
		}
	}
	return false
}
