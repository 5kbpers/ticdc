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

	dmysql "github.com/go-sql-driver/mysql"
	"github.com/pingcap/parser/mysql"
	"github.com/pingcap/tidb/infoschema"

	"github.com/pingcap/parser/model"
	"github.com/pingcap/parser/types"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pingcap/check"
	dbtypes "github.com/pingcap/tidb/types"
)

type EmitSuite struct{}

var _ = check.Suite(&EmitSuite{})

type dummyInspector struct {
	tableInspector
}

func (dummyInspector) Refresh(schema, table string) {
}

func (s EmitSuite) TestShouldExecDDL(c *check.C) {
	// Set up
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	c.Assert(err, check.IsNil)
	defer db.Close()

	sink := mysqlSink{
		db:           db,
		tblInspector: dummyInspector{},
	}

	txn := Txn{
		DDL: &DDL{
			Database: "test",
			Table:    "user",
			SQL:      "CREATE TABLE user (id INT PRIMARY KEY);",
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec("USE `test`;").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(txn.DDL.SQL).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	// Execute
	err = sink.Emit(context.Background(), txn)

	// Validate
	c.Assert(err, check.IsNil)
}

func (s EmitSuite) TestShouldIgnoreCertainDDLError(c *check.C) {
	// Set up
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	c.Assert(err, check.IsNil)
	defer db.Close()

	sink := mysqlSink{
		db:           db,
		tblInspector: dummyInspector{},
	}

	txn := Txn{
		DDL: &DDL{
			Database: "test",
			Table:    "user",
			SQL:      "CREATE TABLE user (id INT PRIMARY KEY);",
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec("USE `test`;").WillReturnResult(sqlmock.NewResult(1, 1))
	ignorable := dmysql.MySQLError{
		Number: uint16(infoschema.ErrTableExists.Code()),
	}
	mock.ExpectExec(txn.DDL.SQL).WillReturnError(&ignorable)
	mock.ExpectCommit()

	// Execute
	err = sink.Emit(context.Background(), txn)

	// Validate
	c.Assert(err, check.IsNil)
}

type tableHelper struct {
	tableInspector
	TableInfoGetter
}

func (h *tableHelper) Get(schema, table string) (*tableInfo, error) {
	return &tableInfo{
		columns: []string{"id", "name"},
		uniqueKeys: []indexInfo{
			{name: "pk", columns: []string{"id"}},
		},
	}, nil
}

func (h *tableHelper) TableByID(id int64) (info *model.TableInfo, ok bool) {
	return &model.TableInfo{
		Columns: []*model.ColumnInfo{
			{
				Name:  model.CIStr{O: "id"},
				State: model.StatePublic,
				FieldType: types.FieldType{
					Tp:      mysql.TypeLong,
					Flen:    types.UnspecifiedLength,
					Decimal: types.UnspecifiedLength,
				},
			},
			{
				Name:  model.CIStr{O: "name"},
				State: model.StatePublic,
				FieldType: types.FieldType{
					Tp:      mysql.TypeString,
					Flen:    types.UnspecifiedLength,
					Decimal: types.UnspecifiedLength,
				},
			},
		},
	}, true
}

func (h *tableHelper) GetTableIDByName(schema, table string) (int64, bool) {
	return 42, true
}

func (s EmitSuite) TestShouldExecReplaceInto(c *check.C) {
	// Set up
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	c.Assert(err, check.IsNil)
	defer db.Close()

	helper := tableHelper{}
	sink := mysqlSink{
		db:           db,
		tblInspector: &helper,
		infoGetter:   &helper,
	}

	txn := Txn{
		DMLs: []*DML{
			{
				Database: "test",
				Table:    "user",
				Tp:       InsertDMLType,
				Values: map[string]dbtypes.Datum{
					"id":   dbtypes.NewDatum(42),
					"name": dbtypes.NewDatum("tester1"),
				},
			},
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec("REPLACE INTO `test`.`user`(`id`,`name`) VALUES (?,?);").
		WithArgs(42, "tester1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	// Execute
	err = sink.Emit(context.Background(), txn)

	// Validate
	c.Assert(err, check.IsNil)
}

func (s EmitSuite) TestShouldExecDelete(c *check.C) {
	// Set up
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	c.Assert(err, check.IsNil)
	defer db.Close()

	helper := tableHelper{}
	sink := mysqlSink{
		db:           db,
		tblInspector: &helper,
		infoGetter:   &helper,
	}

	txn := Txn{
		DMLs: []*DML{
			{
				Database: "test",
				Table:    "user",
				Tp:       DeleteDMLType,
				Values: map[string]dbtypes.Datum{
					"id":   dbtypes.NewDatum(123),
					"name": dbtypes.NewDatum("tester1"),
				},
			},
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `test`.`user` WHERE `id` = ? LIMIT 1;").
		WithArgs(123).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	// Execute
	err = sink.Emit(context.Background(), txn)

	// Validate
	c.Assert(err, check.IsNil)
}

type FilterSuite struct{}

var _ = check.Suite(&FilterSuite{})

func (s *FilterSuite) TestFilterDMLs(c *check.C) {
	txn := Txn{
		DMLs: []*DML{
			{Database: "INFORMATIOn_SCHEmA"},
			{Database: "test"},
			{Database: "test_mysql"},
			{Database: "mysql"},
		},
		Ts: 213,
	}
	filterBySchemaAndTable(&txn)
	c.Assert(txn.Ts, check.Equals, uint64(213))
	c.Assert(txn.DDL, check.IsNil)
	c.Assert(txn.DMLs, check.HasLen, 2)
	c.Assert(txn.DMLs[0].Database, check.Equals, "test")
	c.Assert(txn.DMLs[1].Database, check.Equals, "test_mysql")
}

func (s *FilterSuite) TestFilterDDL(c *check.C) {
	txn := Txn{
		DDL: &DDL{Database: "performance_schema"},
		Ts:  10234,
	}
	filterBySchemaAndTable(&txn)
	c.Assert(txn.Ts, check.Equals, uint64((10234)))
	c.Assert(txn.DMLs, check.HasLen, 0)
	c.Assert(txn.DDL, check.IsNil)
}
