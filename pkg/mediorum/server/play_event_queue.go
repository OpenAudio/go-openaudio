package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/mediorum/server/signature"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const playBatch = 500

type PlayEventQueue struct {
	mu    sync.Mutex
	plays []*PlayEvent
}

func NewPlayEventQueue() *PlayEventQueue {
	return &PlayEventQueue{
		plays: []*PlayEvent{},
	}
}

func (p *PlayEventQueue) pushPlayEvent(play *PlayEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plays = append(p.plays, play)
}

func (p *PlayEventQueue) popPlayEventBatch() []*PlayEvent {
	p.mu.Lock()
	defer p.mu.Unlock()

	batchSize := playBatch
	if len(p.plays) < playBatch {
		batchSize = len(p.plays)
	}

	batch := p.plays[:batchSize]
	p.plays = p.plays[batchSize:]

	return batch
}

var playQueueInterval = 20 * time.Second

type PlayEvent struct {
	RowID     int
	UserID    string
	TrackID   string
	PlayTime  time.Time
	Signature string
	City      string
	Region    string
	Country   string
}

func (ss *MediorumServer) startPlayEventQueue() {
	ticker := time.NewTicker(playQueueInterval)
	defer ticker.Stop()

	for range ticker.C {
		if err := ss.processPlayRecordBatch(); err != nil {
			ss.logger.Error("error recording play batch", "error", err)
		}
	}
}

func (ss *MediorumServer) processPlayRecordBatch() error {
	// require all operations in process batch take at most 30 seconds
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	plays := ss.playEventQueue.popPlayEventBatch()
	ss.logger.Info("popped plays off event queue: %v", plays)
	if len(plays) == 0 {
		return nil
	}

	corePlays := []*v1.TrackPlay{}
	for _, play := range plays {
		corePlays = append(corePlays, &v1.TrackPlay{
			UserId:    play.UserID,
			TrackId:   play.TrackID,
			Timestamp: timestamppb.New(play.PlayTime),
			Signature: play.Signature,
			City:      play.City,
			Country:   play.Country,
			Region:    play.Region,
		})
	}

	playsTx := &v1.TrackPlays{
		Plays: corePlays,
	}

	// sign plays event payload with mediorum priv key
	signedPlaysEvent, err := signature.SignCoreBytes(playsTx, ss.Config.privateKey)
	if err != nil {
		ss.logger.Error("core error signing plays proto event", "err", err)
		return err
	}

	// construct proto listen signedTx alongside signature of plays signedTx
	signedTx := &v1.SignedTransaction{
		Signature: signedPlaysEvent,
		Transaction: &v1.SignedTransaction_Plays{
			Plays: playsTx,
		},
	}

	// submit to configured core node
	var res *connect.Response[v1.SendTransactionResponse]
	func() {
		defer func() {
			if r := recover(); r != nil {
				ss.logger.Error("panic recovered in SendTransaction", "recover", r)
				err = fmt.Errorf("panic in SendTransaction: %v", r)
			}
		}()
		res, err = ss.core.SendTransaction(ctx, connect.NewRequest(&v1.SendTransactionRequest{
			Transaction: signedTx,
		}))

		if err != nil {
			ss.logger.Error("core error submitting plays event", "err", err)
		}
	}()

	if err != nil {
		ss.logger.Error("core error submitting plays event", "err", err)
		return err
	}

	ss.logger.Info("core %d plays recorded", "tx", len(corePlays), res.Msg.Transaction.Hash)
	return nil
}
