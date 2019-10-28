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

	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"go.uber.org/zap"
)

type ProcessTableInfo struct {
	ID      uint64 `json:"id"`
	StartTS uint64 `json:"start-ts"`
}

// SubChangeFeedInfo records the process information of a capture
type SubChangeFeedInfo struct {
	// The maximum event CommitTS that has been synchronized. This is updated by corresponding processor.
	CheckPointTS uint64 `json:"checkpoint-ts"`
	// The event that satisfies CommitTS <= ResolvedTS can be synchronized. This is updated by corresponding processor.
	ResolvedTS uint64 `json:"resolved-ts"`
	// Table information list, containing tables that processor should process, updated by ownrer, processor is read only.
	TableInfos []*ProcessTableInfo `json:"table-infos"`
}

// Marshal returns the json marshal format of a SubChangeFeedInfo
func (scfi *SubChangeFeedInfo) Marshal() string {
	data, err := json.Marshal(scfi)
	if err != nil {
		log.Error("fail to marshal ChangeFeedDetail to json", zap.Error(err))
	}
	return string(data)
}

// Unmarshal unmarshals into *SubChangeFeedInfo from json marshal byte slice
func (scfi *SubChangeFeedInfo) Unmarshal(data []byte) error {
	err := json.Unmarshal(data, scfi)
	return errors.Annotatef(err, "Unmarshal data: %v", data)
}

type CaptureID = string
type ChangeFeedID = string
type ProcessorsInfos = map[CaptureID]*SubChangeFeedInfo

type ChangeFeedStatus int

const (
	ChangeFeedUnknown ChangeFeedStatus = iota
	ChangeFeedSyncDML
	ChangeFeedWaitToExecDDL
	ChangeFeedExecDDL
	ChangeFeedDDLExecuteFailed
)

func (s ChangeFeedStatus) String() string {
	switch s {
	case ChangeFeedUnknown:
		return "Unknown"
	case ChangeFeedSyncDML:
		return "SyncDML"
	case ChangeFeedWaitToExecDDL:
		return "WaitToExecDDL"
	case ChangeFeedExecDDL:
		return "ExecDDL"
	case ChangeFeedDDLExecuteFailed:
		return "DDLExecuteFailed"
	}
	return ""
}

// ChangeFeedInfo stores information about a ChangeFeed
type ChangeFeedInfo struct {
	Status       ChangeFeedStatus `json:"-"`
	ResolvedTS   uint64           `json:"resolved-ts"`
	CheckpointTS uint64           `json:"checkpoint-ts"`

	ProcessorInfos  ProcessorsInfos `json:"-"`
	DDLCurrentIndex int             `json:"-"`
}

// Marshal returns json encoded string of ChangeFeedInfo, only contains necessary fields stored in storage
func (info *ChangeFeedInfo) Marshal() (string, error) {
	data, err := json.Marshal(info)
	return string(data), errors.Trace(err)
}

// Unmarshal unmarshals into *ChangeFeedInfo from json marshal byte slice
func (info *ChangeFeedInfo) Unmarshal(data []byte) error {
	err := json.Unmarshal(data, info)
	return errors.Annotatef(err, "Unmarshal data: %v", data)
}
