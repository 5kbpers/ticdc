package sink

import (
	"encoding/json"
	"errors"
	"fmt"
	timodel "github.com/pingcap/parser/model"
	"github.com/pingcap/ticdc/cdc/model"
	"github.com/pingcap/ticdc/cdc/sink/encoding"
	"github.com/pingcap/tidb/types"
	"hash"
	"hash/crc32"
)

const (
	// MagicIndex 4 bytes at the head of message.
	MagicIndex = 0xBAAAD700

	// Version represents version of message.
	Version = 1
)

type TxnOp byte
const (
	_  = iota
	// DmlType represents of message.
	DmlOp TxnOp = 1 + iota

	// DdlType represents of message.
	DdlOp
)

type writer struct {
	content []byte

	// Reusable memory.
	buf1    encoding.Encbuf
	buf2    encoding.Encbuf

	version byte
	msgType MsgType
	cdcId string
}

func (w *writer) write(bufs ...[]byte) error {
	for _, b := range bufs {
		w.content = append(w.content, b...)
	}
	return nil
}

func (w *writer) writeMeta() error{
	w.buf1.Reset()
	w.buf1.PutBE32(MagicIndex)
	w.buf1.PutByte(w.version)
	w.buf1.PutByte(byte(w.msgType))
	w.buf1.PutUvarintStr(w.cdcId)

	return w.write(w.buf1.Get())
}

func (w *writer) flush() []byte {
	return w.content
}

type resolveTsWriter struct {
	*writer
	ts int64
}

func NewResloveTsWriter(cdcId string, ts int64)  *resolveTsWriter{
	return &resolveTsWriter{
		writer: &writer{
			version: Version,
			msgType: ResolveTsType,
			content: make([]byte, 0, 1 << 22),
			buf1:    encoding.Encbuf{B: make([]byte, 0, 1<<22)},
			buf2:    encoding.Encbuf{B: make([]byte, 0, 1<<22)},
			cdcId: cdcId,
		},
		ts: ts,
	}
}

func (w *resolveTsWriter) Write() ([]byte, error) {
	w.writeMeta()
	w.buf1.Reset()
	w.buf1.PutBE64int64(w.ts)
	err := w.write(w.buf1.Get())
	if err != nil {
		return nil, err
	}
	
	return w.flush(), nil
}

type txnWriter struct {
	*writer
	crc32 hash.Hash
	txn model.Txn
	infoGetter TableInfoGetter
}

func NewTxnWriter(cdcId string, txn model.Txn, infoGetter TableInfoGetter) *txnWriter{
	return &txnWriter{
		writer: &writer{
			version: Version,
			msgType: TxnType,
			content: make([]byte, 0, 1 << 22),
			buf1:    encoding.Encbuf{B: make([]byte, 0, 1<<22)},
			buf2:    encoding.Encbuf{B: make([]byte, 0, 1<<22)},
			cdcId:   cdcId,
		},
		crc32: crc32.New(castagnoliTable),
		txn: txn,
		infoGetter: infoGetter,
	}
}

var castagnoliTable *crc32.Table

func init() {
	castagnoliTable = crc32.MakeTable(crc32.Castagnoli)
}

type Datum struct {
	Value interface{} `json:"value"`
}

func (w *txnWriter) Write() ([]byte, error){
	w.writeMeta()
	w.buf1.Reset()
	w.buf1.PutBE64(w.txn.Ts)

	if w.txn.IsDDL() {
		w.buf1.PutByte(byte(DdlOp))

		w.buf2.Reset()
		w.buf2.PutUvarintStr(w.txn.DDL.Database)
		w.buf2.PutUvarintStr(w.txn.DDL.Table)

		job, err := json.Marshal(w.txn.DDL.Job)
		if err != nil {
			return nil, err
		}

		w.buf2.PutUvarintStr(string(job))

		w.buf1.PutBE32int(w.buf2.Len())
		w.buf2.PutHash(w.crc32)

		if err := w.write(w.buf1.Get(), w.buf2.Get()); err != nil {
			return nil, err
		}

		return w.flush(), nil
	}

	if err := w.writeDML(w.txn.DMLs, w.infoGetter); err != nil {
		return nil, err
	}

	return w.flush(), nil
}

func (w *txnWriter) writeDML(dmls []*model.DML, infoGetter TableInfoGetter) error{
	w.buf1.PutByte(byte(DmlOp))
	w.buf1.PutBE32int(len(dmls))

	w.buf2.Reset()
	for _, dml := range dmls {
		// write dml meta
		w.buf2.PutUvarintStr(dml.Database)
		w.buf2.PutUvarintStr(dml.Table)
		w.buf2.PutBE32int(int(dml.Tp))

		//write values
		w.buf2.PutBE32int(len(dml.Values))
		for key, value := range dml.Values {
			w.buf2.PutUvarintStr(key)

			v, err := json.Marshal(Datum{
				Value: value.GetValue(),
			})
			if err != nil {
				return err
			}

			w.buf2.PutUvarintStr(string(v))
		}

		tblID, ok := infoGetter.GetTableIDByName(dml.Database, dml.Table)
		if !ok {
			return errors.New("get table id failed")
		}

		tableInfo, ok := infoGetter.TableByID(tblID)
		if !ok {
			return errors.New("get table by id failed")
		}

		//write columns
		columns := writableColumns(tableInfo)
		w.buf2.PutBE32int(len(columns))
		for _, c := range columns {
			cbytes, err := json.Marshal(*c)
			if err != nil {
				return err
			}

			w.buf2.PutUvarintStr(string(cbytes))
		}
	}

	w.buf1.PutBE32int(w.buf2.Len())
	w.buf2.PutHash(w.crc32)

	return w.write(w.buf1.Get(), w.buf2.Get())
}


// writableColumns returns all columns which can be written. This excludes
// generated and non-public columns.
func writableColumns(table *timodel.TableInfo) []*timodel.ColumnInfo {
	cols := make([]*timodel.ColumnInfo, 0, len(table.Columns))
	for _, col := range table.Columns {
		if col.State == timodel.StatePublic && !col.IsGenerated() {
			cols = append(cols, col)
		}
	}
	return cols
}

type reader struct {
	data []byte
}

func NewReader(data []byte) *reader{
	return &reader{
		data: data,
	}
}

func (r *reader) Decode() (*Message, error) {
	d := &encoding.Decbuf{B: r.data}
	if d.Be32() != MagicIndex {
		return nil, errors.New("invalid message format")
	}

	// version
	d.Byte()

	//type
	t := d.Byte()
	switch MsgType(t) {
	case ResolveTsType:
		return r.decodeResloveTsMsg(d), nil
	case TxnType:
		return r.decodeTxnMsg(d)
	default:
		return nil, errors.New(fmt.Sprintf("unsupport type - %d", t))
	}
}

func (r *reader) decodeResloveTsMsg(d *encoding.Decbuf) *Message{
	return &Message{
		CdcID: d.UvarintStr(),
		MsgType: ResolveTsType,
		ResloveTs: d.Be64int64(),
	}
}

func (r *reader) decodeTxnMsg(d *encoding.Decbuf) (*Message, error){
	m := &Message{
		CdcID: d.UvarintStr(),
		MsgType: TxnType,
	}

	ts := d.Be64()
	switch TxnOp(d.Byte()) {
	case DmlOp:
		txn, col, err := r.decodeDML(d, ts)
		if err != nil {
			return nil, err
		}
		m.Txn = txn
		m.Columns = col
		return m, nil
	case DdlOp:
		txn, err := r.decodeDDL(d, ts)
		if err != nil {
			return nil, err
		}

		m.Txn = txn
		return m, nil
	default:
		return nil, errors.New(fmt.Sprintf("unsupport txn operator"))
	}
}

func (r *reader) decodeDDL(d *encoding.Decbuf, ts uint64) (*model.Txn, error){
	txn := &model.Txn{
		Ts: ts,
	}

	// TOOD check checksum
	d.Be32int()

	txn.DDL = &model.DDL{
		Database: d.UvarintStr(),
		Table: d.UvarintStr(),
		Job: &timodel.Job{},
	}

	job := d.UvarintStr()

	err := json.Unmarshal([]byte(job), txn.DDL.Job)
	if err != nil {
		return nil, err
	}

	return txn, nil
}

func (r *reader) decodeDML(d *encoding.Decbuf, ts uint64) (*model.Txn, map[string][]*timodel.ColumnInfo, error){
	txn := &model.Txn{
		Ts: ts,
	}

	dmlLen := d.Be32int()
	// TOOD check checksum
	d.Be32int()

	columnsMap := make(map[string][]*timodel.ColumnInfo)
	dmls := make([]*model.DML, dmlLen)
	for i := 0 ; i < dmlLen; i++ {
		database := d.UvarintStr()
		table := d.UvarintStr()
		dml := &model.DML{
			Database: database,
			Table: table,
			Tp: model.DMLType(d.Be32int()),
		}

		values, err := r.decodeDMLValues(d)
		if err != nil {
			return nil, nil, err
		}
		dml.Values = values
		dmls[i] = dml

		// column info
		columns := make([]*timodel.ColumnInfo, 0)
		columnsLen := d.Be32int()
		for i := 0; i < columnsLen; i++ {
			var col timodel.ColumnInfo

			err := json.Unmarshal([]byte(d.UvarintStr()), &col)
			if err != nil {
				return nil, nil, err
			}

			columns = append(columns, &col)
		}

		columnsMap[FormColumnKey(database, table)] = columns
	}

	txn.DMLs = dmls

	return txn, columnsMap, nil
}

func FormColumnKey(database, table string) string {
	return fmt.Sprintf("%s-%s", database, table)
}

func (r *reader) decodeDMLValues(d *encoding.Decbuf) (map[string]types.Datum, error){
	valueLen := d.Be32int()
	m := make(map[string]types.Datum, valueLen)
	for i := 0 ; i < valueLen; i++ {
		key := d.UvarintStr()

		var dt Datum
		err := json.Unmarshal([]byte(d.UvarintStr()), &dt)
		if err != nil {
			return nil, err
		}

		td := &types.Datum{}
		td.SetInterface(dt.Value)
		m[key] = *td
	}

	return m, nil
}

