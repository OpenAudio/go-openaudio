package server

import (
	"fmt"

	"github.com/cockroachdb/pebble"
	"go.uber.org/zap"
)

func (s *Server) CompactStateDB() error {
	dbBase := s.cometbftConfig.BaseConfig.DBDir()
	pebbleStatePath := dbBase + "/state.db"
	return s.compactDB(pebbleStatePath)
}

func (s *Server) CompactBlockstoreDB() error {
	dbBase := s.cometbftConfig.BaseConfig.DBDir()
	pebbleBlockstorePath := dbBase + "/blockstore.db"
	return s.compactDB(pebbleBlockstorePath)
}

func (s *Server) compactDB(path string) error {
	s.logger.Info("pebble compacting", zap.String("path", path))

	opts := &pebble.Options{
		ReadOnly:         false,
		ErrorIfNotExists: true,
	}

	db, err := pebble.Open(path, opts)
	if err != nil {
		return fmt.Errorf("could not open pebbledb: %v", err)
	}
	defer db.Close()

	start := []byte{}
	end := []byte{}

	iter, err := db.NewIter(nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	if iter.First() {
		start = append(start, iter.Key()...)
	}
	if iter.Last() {
		end = append(end, iter.Key()...)
	}

	if err := db.Compact(start, end, true); err != nil {
		return err
	}
	s.logger.Info("manual pebble compaction succeeded")
	return nil
}
