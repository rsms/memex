package main

import (
	// "bytes"
	// "encoding/gob"
	// "time"

	"github.com/rsms/go-log"
	"github.com/syndtr/goleveldb/leveldb"
)

type IndexDB struct {
	db     *leveldb.DB
	logger *log.Logger
}

type DocType struct {
	Id       string `json:"id"`
	ParentId string `json:"parent_id"`
	Name     string `json:"name"`
}

type Doc struct {
	Id      string      `json:"id"`
	Version string      `json:"version"`
	Type    string      `json:"type"`
	Tags    []string    `json:"tags"`
	Body    interface{} `json:"body"`
}

func IndexDBOpen(path string, logger *log.Logger) (*IndexDB, error) {
	db, err := leveldb.OpenFile(path, nil)
	return &IndexDB{
		db:     db,
		logger: logger,
	}, err
}

func (db *IndexDB) Close() error {
	if db.db == nil {
		return nil
	}
	err := db.db.Close()
	db.db = nil
	return err
}

func (db *IndexDB) PutDoc(doc ...*Doc) error {
	// TODO
	// consider returning conflicts
	return nil
}

func (db *IndexDB) DefineTypes(types ...DocType) error {
	batch := new(leveldb.Batch)
	for _, dt := range types {
		key := []byte("dtype:" + dt.Id)
		val := []byte(dt.Name + "\n" + dt.ParentId)
		batch.Put(key, val)
	}
	return db.db.Write(batch, nil)
}
