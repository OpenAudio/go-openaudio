// The mempool (memory pool) stores and broadcasts transactions that are prepared to be included in a block.
// There is no guarantee that when a transaction makes it into the mempool that it will be included in a block.
package server

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	corev1connect "github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	ErrFullMempool = errors.New("mempool full")
)

type Mempool struct {
	logger *zap.Logger
	config *config.Config

	deque *list.List
	txMap map[string]*list.Element
	mutex sync.Mutex

	db *db.Queries

	maxMempoolTransactions int
}

// signed tx with mempool related metadata
// deadline - the block MUST be included in a block prior to the deadline
type MempoolTransaction struct {
	Deadline int64
	Tx       *v1.SignedTransaction
	Txv2     *v1beta1.Transaction
}

func NewMempool(logger *zap.Logger, config *config.Config, db *db.Queries, maxTransactions int) *Mempool {
	return &Mempool{
		logger:                 logger.With(zap.String("service", "mempool")),
		config:                 config,
		deque:                  list.New(),
		txMap:                  make(map[string]*list.Element),
		db:                     db,
		maxMempoolTransactions: maxTransactions,
	}
}

// gathers a batch of transactions skipping those that have expired
func (m *Mempool) GetBatch(batchSize int, currentBlock int64) []*MempoolTransaction {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	batch := []*MempoolTransaction{}
	count := 0

	for e := m.deque.Front(); e != nil && count < batchSize; e = e.Next() {
		tx, ok := e.Value.(*MempoolTransaction)
		if !ok {
			continue
		}

		if tx.Deadline <= currentBlock {
			continue
		}

		batch = append(batch, tx)
		count++
	}

	return batch
}

func (m *Mempool) GetAll() []*MempoolTransaction {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	batch := []*MempoolTransaction{}

	for {
		e := m.deque.Front()
		if e.Next() != nil {
			tx, ok := e.Value.(*MempoolTransaction)
			if !ok {
				continue
			}
			batch = append(batch, tx)
			continue
		}
		break
	}

	return batch
}

func (m *Mempool) RemoveBatch(ids []string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, id := range ids {
		if element, exists := m.txMap[id]; exists {
			m.deque.Remove(element)
			delete(m.txMap, id)
			m.logger.Info("removed from mempools", zap.String("tx", id))
		}
	}
}

func (m *Mempool) RemoveExpiredTransactions(blockNum int64) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for id, element := range m.txMap {
		mptx, ok := element.Value.(*MempoolTransaction)
		if !ok {
			continue
		}
		deadline := mptx.Deadline
		if deadline <= blockNum {
			m.deque.Remove(element)
			delete(m.txMap, id)
		}
	}
}

func (m *Mempool) MempoolSize() (int, int) {
	return len(m.txMap), m.deque.Len()
}

func (s *Server) addMempoolTransaction(key string, tx *MempoolTransaction, broadcast bool) error {
	// TODO: check db if tx already exists

	// broadcast to peers before adding to our own mempool
	if broadcast {
		go s.broadcastMempoolTransaction(key, tx)
	}

	m := s.mempl
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if len(m.txMap) >= m.maxMempoolTransactions {
		return ErrFullMempool
	}

	_, exists := m.txMap[key]
	if exists {
		m.logger.Warn("duplicate tx tried to add to mempool", zap.String("tx", key))
		return nil
	}

	element := m.deque.PushBack(tx)
	m.txMap[key] = element

	m.logger.Info("added to mempool", zap.String("tx", key))
	return nil
}

func (s *Server) broadcastMempoolTransaction(key string, tx *MempoolTransaction) {
	// only broadcast certain types of txs, don't broadcast these ones
	if tx.Tx != nil {
		switch tx.Tx.Transaction.(type) {
		case *v1.SignedTransaction_SlaRollup:
			return
		}
	}
	// For v2 transactions, we don't have specific broadcast filtering yet
	// but we can add it here later if needed

	peers := s.connectRPCPeers.Values()
	for _, peer := range peers {
		go func(logger *zap.Logger, peer corev1connect.CoreServiceClient) {
			var err error
			if tx.Tx != nil {
				_, err = peer.ForwardTransaction(context.Background(), connect.NewRequest(&v1.ForwardTransactionRequest{
					Transaction: tx.Tx,
				}))
			} else if tx.Txv2 != nil {
				_, err = peer.ForwardTransaction(context.Background(), connect.NewRequest(&v1.ForwardTransactionRequest{
					Transactionv2: tx.Txv2,
				}))
			} else {
				logger.Error("mempool transaction has no content", zap.String("tx", key))
				return
			}

			if err != nil {
				logger.Error("could not broadcast tx", zap.String("tx", key), zap.Error(err))
				return
			}
			s.logger.Info("broadcasted tx to peer", zap.String("tx", key))
		}(s.logger, peer)
	}
}

func (s *Server) getMempl(c echo.Context) error {
	txs := s.mempl.GetAll()

	jsontxs := [][]byte{}
	for _, tx := range txs {
		var jsonData []byte
		var err error
		if tx.Txv2 != nil {
			jsonData, err = protojson.Marshal(tx.Txv2)
		} else {
			jsonData, err = protojson.Marshal(tx.Tx)
		}
		if err != nil {
			return fmt.Errorf("could not marshal proto into json: %v", err)
		}
		jsontxs = append(jsontxs, jsonData)
	}

	result := []map[string]interface{}{}
	for _, jsonData := range jsontxs {
		var obj map[string]interface{}
		if err := json.Unmarshal(jsonData, &obj); err != nil {
			return fmt.Errorf("invalid json")
		}
		result = append(result, obj)
	}

	return c.JSONPretty(200, result, "  ")
}

func (s *Server) startMempoolCache(ctx context.Context) error {
	s.StartProcess(ProcessStateMempoolCache)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.RunningProcessWithMetadata(ProcessStateMempoolCache, "Updating mempool statistics")
			func(s *Server) {
				mempl := s.mempl
				mempl.mutex.Lock()

				memplCount := mempl.deque.Len()
				txCount := int64(memplCount)
				maxTxCount := int64(mempl.maxMempoolTransactions)

				// Calculate total size of transactions in mempool
				var totalSize int64
				for e := mempl.deque.Front(); e != nil; e = e.Next() {
					if tx, ok := e.Value.(*MempoolTransaction); ok {
						if tx.Tx != nil {
							totalSize += int64(proto.Size(tx.Tx))
						} else if tx.Txv2 != nil {
							totalSize += int64(proto.Size(tx.Txv2))
						}
					}
				}

				// Calculate theoretical max mempool size using actual config value
				maxTxBytes := int64(s.cometbftConfig.Mempool.MaxTxBytes)
				maxTxSize := maxTxCount * maxTxBytes

				mempl.mutex.Unlock()

				upsertCache(s.cache.mempoolInfo, MempoolInfoKey, func(mempoolInfo *v1.GetStatusResponse_MempoolInfo) *v1.GetStatusResponse_MempoolInfo {
					return &v1.GetStatusResponse_MempoolInfo{
						TxCount:    txCount,
						MaxTxCount: maxTxCount,
						TxSize:     totalSize,
						MaxTxSize:  maxTxSize,
					}
				})
			}(s)
			s.SleepingProcessWithMetadata(ProcessStateMempoolCache, "Waiting for next update")
		case <-ctx.Done():
			s.CompleteProcess(ProcessStateMempoolCache)
			return ctx.Err()
		}
	}
}
