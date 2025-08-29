package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/mediorum/server/signature"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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

	batchSize := min(len(p.plays), playBatch)
	batch := p.plays[:batchSize]
	p.plays = p.plays[batchSize:]

	return batch
}

var playQueueInterval = 4 * time.Second

type PlayEvent struct {
	RowID            int
	UserID           string
	TrackID          string
	PlayTime         time.Time
	Signature        string
	City             string
	Region           string
	Country          string
	RequestSignature string
}

// Marshal a single PlayEvent
func (e PlayEvent) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddInt("row_id", e.RowID)
	enc.AddString("user_id", e.UserID)
	enc.AddString("track_id", e.TrackID)
	enc.AddTime("play_time", e.PlayTime)
	enc.AddString("signature", e.Signature)
	enc.AddString("city", e.City)
	enc.AddString("region", e.Region)
	enc.AddString("country", e.Country)
	enc.AddString("request_signature", e.RequestSignature)
	return nil
}

func (ss *MediorumServer) startPlayEventQueue(ctx context.Context) error {
	ticker := time.NewTicker(playQueueInterval)
	for {
		select {
		case <-ticker.C:
			if err := ss.processPlayRecordBatch(ctx); err != nil {
				ss.logger.Error("error recording play batch", zap.Error(err))
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) processPlayRecordBatch(ctx context.Context) error {
	// require all operations in process batch take at most 30 seconds
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	plays := ss.playEventQueue.popPlayEventBatch()
	ss.logger.Info(
		"popped plays off event queue",
		zap.Array("plays", zapcore.ArrayMarshalerFunc(func(enc zapcore.ArrayEncoder) error {
			for _, p := range plays {
				enc.AppendObject(p)
			}
			return nil
		})),
	)

	if len(plays) == 0 {
		return nil
	}

	uniquePlays := make(map[string]*v1.TrackPlay)
	for _, play := range plays {
		// use incoming request signature to deduplicate plays
		uniquePlays[play.RequestSignature] = &v1.TrackPlay{
			UserId:    play.UserID,
			TrackId:   play.TrackID,
			Timestamp: timestamppb.New(play.PlayTime),
			Signature: play.Signature,
			City:      play.City,
			Country:   play.Country,
			Region:    play.Region,
		}
	}

	// Convert map values back to array
	corePlays := make([]*v1.TrackPlay, 0, len(uniquePlays))
	for _, play := range uniquePlays {
		corePlays = append(corePlays, play)
	}

	playsTx := &v1.TrackPlays{
		Plays: corePlays,
	}

	// sign plays event payload with mediorum priv key
	signedPlaysEvent, err := signature.SignCoreBytes(playsTx, ss.Config.privateKey)
	if err != nil {
		ss.logger.Error("core error signing plays proto event", zap.Error(err))
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
				ss.logger.Error("panic recovered in SendTransaction", zap.Any("recover", r))
				err = fmt.Errorf("panic in SendTransaction: %v", r)
			}
		}()
		res, err = ss.core.SendTransaction(ctx, connect.NewRequest(&v1.SendTransactionRequest{
			Transaction: signedTx,
		}))

		if err != nil {
			ss.logger.Error("core error submitting plays event", zap.Error(err))
		}
	}()

	if err != nil {
		ss.logger.Error("core error submitting plays event", zap.Error(err))
		return err
	}

	ss.logger.Info("core plays recorded", zap.Int("plays", len(corePlays)), zap.String("tx", res.Msg.Transaction.Hash))
	return nil
}
