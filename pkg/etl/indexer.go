package etl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/OpenAudio/go-openaudio/etl/processors"
	em "github.com/OpenAudio/go-openaudio/etl/processors/entity_manager"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// ChallengeStats represents storage proof challenge statistics for a validator
type ChallengeStats struct {
	ChallengesReceived int32
	ChallengesFailed   int32
}

// StorageProofState tracks storage proof challenges and their resolution
type StorageProofState struct {
	Height          int64
	Proofs          map[string]*StorageProofEntry // address -> proof entry
	ProverAddresses map[string]int                // address -> vote count for who should be provers
	Resolved        bool
}

type StorageProofEntry struct {
	Address         string
	ProverAddresses []string
	ProofSignature  []byte
	Cid             string
	SignatureValid  bool // determined during verification
}

func (e *Indexer) Run() error {
	dbUrl := e.dbURL
	if dbUrl == "" {
		return fmt.Errorf("dbUrl environment variable not set")
	}

	err := db.RunMigrations(e.logger, dbUrl, e.runDownMigrations)
	if err != nil {
		return fmt.Errorf("error running migrations: %v", err)
	}

	pgConfig, err := pgxpool.ParseConfig(dbUrl)
	if err != nil {
		return fmt.Errorf("error parsing database config: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), pgConfig)
	if err != nil {
		return fmt.Errorf("error creating database pool: %v", err)
	}

	e.pool = pool
	e.db = db.New(pool)

	// Initialize entity manager dispatcher and register handlers
	e.dispatcher = em.NewDispatcher(e.logger)
	if e.config.IsDataTypeEnabled(em.EntityTypeUser) {
		e.dispatcher.Register(em.UserCreate())
		e.dispatcher.Register(em.UserUpdate())
		e.dispatcher.Register(em.UserVerify())
		e.dispatcher.Register(em.MuteUser())
		e.dispatcher.Register(em.UnmuteUser())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeTrack) {
		e.dispatcher.Register(em.TrackCreate())
		e.dispatcher.Register(em.TrackUpdate())
		e.dispatcher.Register(em.TrackDelete())
		e.dispatcher.Register(em.TrackDownload())
		e.dispatcher.Register(em.TrackMute())
		e.dispatcher.Register(em.TrackUnmute())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypePlaylist) {
		e.dispatcher.Register(em.PlaylistCreate())
		e.dispatcher.Register(em.PlaylistUpdate())
		e.dispatcher.Register(em.PlaylistDelete())
	}
	// Social features use wildcard entity type — actual txs carry the entity
	// being acted on (User for Follow, Track/Playlist for Save/Repost).
	e.dispatcher.Register(em.Follow())
	e.dispatcher.Register(em.Unfollow())
	e.dispatcher.Register(em.Save())
	e.dispatcher.Register(em.Unsave())
	e.dispatcher.Register(em.Repost())
	e.dispatcher.Register(em.Unrepost())
	e.dispatcher.Register(em.Subscribe())
	e.dispatcher.Register(em.Unsubscribe())
	e.dispatcher.Register(em.Share())
	if e.config.IsDataTypeEnabled(em.EntityTypeDeveloperApp) {
		e.dispatcher.Register(em.DeveloperAppCreate())
		e.dispatcher.Register(em.DeveloperAppUpdate())
		e.dispatcher.Register(em.DeveloperAppDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeGrant) {
		e.dispatcher.Register(em.GrantCreate())
		e.dispatcher.Register(em.GrantDelete())
		e.dispatcher.Register(em.GrantApprove())
		e.dispatcher.Register(em.GrantReject())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeEvent) {
		e.dispatcher.Register(em.EventCreate())
		e.dispatcher.Register(em.EventUpdate())
		e.dispatcher.Register(em.EventDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeAssociatedWallet) {
		e.dispatcher.Register(em.AssociatedWalletCreate())
		e.dispatcher.Register(em.AssociatedWalletDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeTip) {
		e.dispatcher.Register(em.TipReaction())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeDashboardWalletUser) {
		e.dispatcher.Register(em.DashboardWalletCreate())
		e.dispatcher.Register(em.DashboardWalletDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeEncryptedEmail) {
		e.dispatcher.Register(em.EncryptedEmailCreate())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeEmailAccess) {
		e.dispatcher.Register(em.EmailAccessUpdate())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeNotification) {
		e.dispatcher.Register(em.NotificationCreate())
		e.dispatcher.Register(em.NotificationView())
		e.dispatcher.Register(em.PlaylistSeenView())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeComment) {
		e.dispatcher.Register(em.CommentCreate())
		e.dispatcher.Register(em.CommentUpdate())
		e.dispatcher.Register(em.CommentDelete())
		e.dispatcher.Register(em.CommentReact())
		e.dispatcher.Register(em.CommentUnreact())
		e.dispatcher.Register(em.CommentPin())
		e.dispatcher.Register(em.CommentUnpin())
		e.dispatcher.Register(em.CommentReport())
		e.dispatcher.Register(em.CommentMute())
		e.dispatcher.Register(em.CommentUnmute())
	}

	if e.dispatcher.HandlerCount() > 0 {
		e.logger.Info("entity manager enabled", zap.Int("handlers", e.dispatcher.HandlerCount()))
	} else {
		e.logger.Info("entity manager disabled (no data types enabled)")
	}

	// Initialize pubsub instances
	e.blockPubsub = NewPubsub[*db.EtlBlock]()
	e.playPubsub = NewPubsub[*db.EtlPlay]()

	// Initialize materialized view refresher
	e.mvRefresher = NewMaterializedViewRefresher(e.pool, e.logger)

	// Initialize chain ID from core service
	err = e.InitializeChainID(context.Background())
	if err != nil {
		e.logger.Error("error initializing chain ID", zap.Error(err))
	}

	// Initialize lastEmBlock: the last assigned blocks.number value.
	// Python increments this sequentially, only for blocks with EM transactions.
	// We continue the sequence from wherever the production DB left off.
	err = e.pool.QueryRow(context.Background(),
		"SELECT COALESCE(MAX(number), 0) FROM blocks").Scan(&e.lastEmBlock)
	if err != nil {
		e.logger.Warn("could not determine last em_block, starting from 0", zap.Error(err))
		e.lastEmBlock = 0
	} else {
		e.logger.Info("last em_block (blocks.number) determined",
			zap.Int64("last_em_block", e.lastEmBlock),
		)
	}

	e.logger.Info("starting etl service")

	if e.checkReadiness {
		err = e.awaitReadiness()
		if err != nil {
			e.logger.Error("error awaiting readiness", zap.Error(err))
		}
	}

	ctx := context.Background()
	g, gCtx := errgroup.WithContext(ctx)

	if e.config.EnableMaterializedViewRefresh {
		g.Go(func() error {
			return e.mvRefresher.Start(gCtx)
		})
	}

	if e.config.EnablePgNotifyListener {
		g.Go(func() error {
			return e.startPgNotifyListener(gCtx)
		})
	}

	g.Go(func() error {
		if err := e.indexBlocks(); err != nil {
			return fmt.Errorf("error indexing blocks: %v", err)
		}

		return nil
	})

	g.Go(func() error {
		return e.syncValidatorsFromCore(gCtx)
	})

	return g.Wait()
}

func (e *Indexer) awaitReadiness() error {
	e.logger.Info("awaiting readiness")
	attempts := 0

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		attempts++
		if attempts > 60 {
			return fmt.Errorf("timed out waiting for readiness")
		}

		res, err := e.core.GetStatus(context.Background(), connect.NewRequest(&corev1.GetStatusRequest{}))
		if err != nil {
			continue
		}

		if res.Msg.Ready {
			return nil
		}
	}

	return nil
}

func (e *Indexer) indexBlocks() error {
	var blocksProcessed int64
	var txsProcessed int64
	indexStart := time.Now()
	lastLog := time.Now()

	// Determine start height (chain height, not blocks.number).
	// 1. Check etl_blocks (our own tracking table) for the last chain height we indexed.
	// 2. If empty and --start was given, use that.
	// 3. Otherwise, fall back to core_indexed_blocks which tracks what chain height
	//    the production indexer last processed.
	latestHeight, err := e.db.GetLatestIndexedBlock(context.Background())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if e.startingBlockHeight > 0 {
				latestHeight = e.startingBlockHeight - 1
			} else {
				var maxHeight *int64
				err = e.pool.QueryRow(context.Background(),
					"SELECT MAX(height) FROM core_indexed_blocks WHERE chain_id = $1",
					e.ChainID).Scan(&maxHeight)
				if err != nil || maxHeight == nil {
					latestHeight = 0
				} else {
					latestHeight = *maxHeight
					e.logger.Info("resuming from core_indexed_blocks",
						zap.String("chain_id", e.ChainID),
						zap.Int64("last_chain_height", latestHeight),
					)
				}
			}
		} else {
			return fmt.Errorf("error getting latest indexed block: %w", err)
		}
	}
	startHeight := latestHeight + 1

	// Start prefetcher goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pf := newPrefetcher(e.core, e.logger)
	go pf.run(ctx, startHeight)

	e.logger.Info("prefetcher started", zap.Int64("start_height", startHeight), zap.Int("buffer_size", pf.bufSz))

	for pb := range pf.C() {
		block := pb.Block

		// Insert into etl_blocks
		err := e.db.InsertBlock(context.Background(), db.InsertBlockParams{
			ProposerAddress: block.Proposer,
			BlockHeight:     block.Height,
			BlockTime:       pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
		})
		if err != nil {
			e.logger.Error("error inserting block", zap.Int64("height", block.Height), zap.Error(err))
			continue
		}

		// Check if this block has any ManageEntity transactions.
		// Python only assigns a blocks.number (em_block) for blocks with EM txs.
		hasEM := false
		for _, tx := range block.Transactions {
			if _, ok := tx.Transaction.Transaction.(*corev1.SignedTransaction_ManageEntity); ok {
				hasEM = true
				break
			}
		}

		// Assign em_block only for blocks with EM transactions (matching Python behavior).
		// Python: marks previous block is_current=false, then inserts new block is_current=true.
		// The blocks table has a unique partial index on (is_current) WHERE is_current IS TRUE,
		// so we must update the previous block BEFORE inserting the new one.
		var emBlock int64
		if hasEM {
			e.lastEmBlock++
			emBlock = e.lastEmBlock

			// Get the previous current block's hash (for parenthash).
			var prevHash *string
			_ = e.pool.QueryRow(context.Background(),
				"SELECT blockhash FROM blocks WHERE is_current = true").Scan(&prevHash)

			// Mark previous block as not current.
			_, err = e.pool.Exec(context.Background(),
				"UPDATE blocks SET is_current = false WHERE is_current = true")
			if err != nil {
				e.logger.Error("error marking previous block not current", zap.Error(err))
			}

			// Insert new block as current.
			_, err = e.pool.Exec(context.Background(),
				`INSERT INTO blocks (blockhash, parenthash, number, is_current)
				 VALUES ($1, $2, $3, true)`,
				block.Hash, prevHash, emBlock)
			if err != nil {
				e.logger.Error("error inserting into blocks table", zap.Int64("height", block.Height), zap.Error(err))
				e.lastEmBlock-- // roll back
				continue
			}
		}

		// Update core_indexed_blocks to track what we've indexed.
		// em_block is NULL for blocks without EM transactions (matching Python).
		var emBlockParam any
		if hasEM {
			emBlockParam = emBlock
		}
		_, err = e.pool.Exec(context.Background(),
			`INSERT INTO core_indexed_blocks (blockhash, chain_id, height, em_block)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (chain_id, height) DO UPDATE SET em_block = $4, blockhash = $1`,
			block.Hash, e.ChainID, block.Height, emBlockParam)
		if err != nil {
			e.logger.Error("error inserting into core_indexed_blocks", zap.Int64("height", block.Height), zap.Error(err))
		}

		var emTxCount, emRejectCount int
		blockStart := time.Now()

		for index, tx := range block.Transactions {
			insertTxParams := db.InsertTransactionParams{
				TxHash:      tx.Hash,
				BlockHeight: block.Height,
				TxIndex:     int32(index),
				TxType:      "",
				Address:     pgtype.Text{Valid: false},
				CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
			}

			switch signedTx := tx.Transaction.Transaction.(type) {
			case *corev1.SignedTransaction_Plays:
				txCtx := &processors.TxContext{
					Block:     block,
					TxHash:    tx.Hash,
					TxIndex:   index,
					BlockTime: pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					InsertTx:  insertTxParams,
				}
				res, err := processors.Play().Process(context.Background(), tx.Transaction, txCtx, e.db)
				if err != nil {
					e.logger.Error("error processing plays", zap.Error(err))
				} else {
					insertTxParams = res.InsertTx
				}

			case *corev1.SignedTransaction_ManageEntity:
				me := signedTx.ManageEntity
				emTxCount++

				txCtx := &processors.TxContext{
					Block:     block,
					TxHash:    tx.Hash,
					TxIndex:   index,
					BlockTime: pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					InsertTx:  insertTxParams,
				}
				res, err := processors.ManageEntity().Process(context.Background(), tx.Transaction, txCtx, e.db)
				if err != nil {
					e.logger.Error("error processing manage entity", zap.Error(err))
				} else {
					insertTxParams = res.InsertTx
				}

				txStart := time.Now()
				emParams := em.NewParams(me, emBlock, block.Timestamp.AsTime(), block.Hash, tx.Hash, e.pool, e.logger)
				if dErr := e.dispatcher.Dispatch(context.Background(), emParams); dErr != nil {
					emRejectCount++
					if em.IsValidationError(dErr) {
						e.logger.Warn("entity manager validation rejected",
							zap.String("entity_type", me.GetEntityType()),
							zap.String("action", me.GetAction()),
							zap.Int64("entity_id", me.GetEntityId()),
							zap.Int64("user_id", me.GetUserId()),
							zap.String("reason", dErr.Error()),
							zap.String("hash", tx.Hash),
						)
					} else {
						e.logger.Error("entity manager dispatch error", zap.Error(dErr))
					}
				} else {
					e.logger.Debug("tx indexed",
						zap.String("type", "manage_entity"),
						zap.String("hash", tx.Hash),
						zap.Duration("elapsed", time.Since(txStart)),
					)
				}

				case *corev1.SignedTransaction_ValidatorRegistration:
					insertTxParams.TxType = TxTypeValidatorRegistrationLegacy
					// Legacy validator registration - no specific table insert needed
				case *corev1.SignedTransaction_ValidatorDeregistration:
					insertTxParams.TxType = TxTypeValidatorMisbehaviorDereg
					vd := signedTx.ValidatorDeregistration
					// For deregistration we only have comet address, we'll need to look up eth address
					// For now use comet address, can be improved later
					insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
					err = e.db.InsertValidatorMisbehaviorDeregistration(context.Background(), db.InsertValidatorMisbehaviorDeregistrationParams{
						CometAddress: vd.CometAddress,
						PubKey:       vd.PubKey,
						BlockHeight:  block.Height,
						TxHash:       tx.Hash,
						CreatedAt:    pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						e.logger.Error("error inserting validator misbehavior deregistration", zap.Error(err))
					}
				case *corev1.SignedTransaction_SlaRollup:
					insertTxParams.TxType = TxTypeSlaRollup
					sr := signedTx.SlaRollup
					// SLA rollups affect multiple validators, so we leave address as null

					// Use the number of reports in the rollup as the validator count
					// This matches what the original core system does
					validatorCount := int32(len(sr.Reports))

					// Calculate block quota (total blocks divided by number of validators)
					var blockQuota int32 = 0
					if sr.BlockEnd > sr.BlockStart && validatorCount > 0 {
						blockQuota = int32(sr.BlockEnd-sr.BlockStart) / validatorCount
					}

					// Calculate BPS and TPS for this rollup period
					blockRange := sr.BlockEnd - sr.BlockStart
					var bps, tps float64 = 0.0, 0.0

					if blockRange > 0 {
						// Get transaction count for this block range
						txCount := int64(0)
						for blockHeight := sr.BlockStart; blockHeight <= sr.BlockEnd; blockHeight++ {
							blockTxCount, err := e.db.GetBlockTransactionCount(context.Background(), blockHeight)
							if err != nil {
								e.logger.Debug("failed to get transaction count for block", zap.Int64("height", blockHeight), zap.Error(err))
								continue
							}
							txCount += blockTxCount
						}

						// Calculate time duration from the rollup timestamp and previous rollup
						rollupTime := sr.Timestamp.AsTime()
						var duration float64 = 0

						// Try to get the previous rollup to calculate time difference
						if latestRollup, err := e.db.GetLatestSlaRollup(context.Background()); err == nil {
							if latestRollup.CreatedAt.Valid {
								duration = rollupTime.Sub(latestRollup.CreatedAt.Time).Seconds()
							}
						}

						// If we couldn't get duration from previous rollup, estimate from block count
						// Assuming average block time of 2 seconds
						if duration <= 0 {
							duration = float64(blockRange) * 2.0
						}

						// Calculate BPS and TPS
						if duration > 0 {
							bps = float64(blockRange) / duration
							tps = float64(txCount) / duration
						}
					}

					// Insert SLA rollup and get the ID
					rollupId, err := e.db.InsertSlaRollupReturningId(context.Background(), db.InsertSlaRollupReturningIdParams{
						BlockStart:     sr.BlockStart,
						BlockEnd:       sr.BlockEnd,
						BlockHeight:    block.Height,
						ValidatorCount: validatorCount,
						BlockQuota:     blockQuota,
						Bps:            bps,
						Tps:            tps,
						TxHash:         tx.Hash,
						CreatedAt:      pgtype.Timestamp{Time: sr.Timestamp.AsTime(), Valid: true}, // Use rollup timestamp, not block timestamp
					})
					if err != nil {
						e.logger.Error("error inserting SLA rollup", zap.Error(err))
					} else {
						// Get storage proof challenge statistics for this SLA period
						challengeStats, err := e.calculateChallengeStatistics(sr.BlockStart, sr.BlockEnd)
						if err != nil {
							e.logger.Error("error calculating challenge statistics", zap.Error(err))
							challengeStats = make(map[string]ChallengeStats) // fallback to empty map
						}

						// Insert SLA node reports with the actual rollup ID and challenge data
						for _, report := range sr.Reports {
							stats := challengeStats[report.Address] // Get challenge stats for this validator

							err = e.db.InsertSlaNodeReport(context.Background(), db.InsertSlaNodeReportParams{
								SlaRollupID:        rollupId, // Use the actual rollup ID
								Address:            report.Address,
								NumBlocksProposed:  report.NumBlocksProposed,
								ChallengesReceived: stats.ChallengesReceived,
								ChallengesFailed:   stats.ChallengesFailed,
								BlockHeight:        block.Height,
								TxHash:             tx.Hash,
								CreatedAt:          pgtype.Timestamp{Time: sr.Timestamp.AsTime(), Valid: true}, // Use rollup timestamp
							})
							if err != nil {
								e.logger.Error("error inserting SLA node report", zap.Error(err))
							}
						}
					}
				case *corev1.SignedTransaction_StorageProof:
					insertTxParams.TxType = TxTypeStorageProof
					sp := signedTx.StorageProof
					insertTxParams.Address = pgtype.Text{String: sp.Address, Valid: true}
					err = e.db.InsertStorageProof(context.Background(), db.InsertStorageProofParams{
						Height:          sp.Height,
						Address:         sp.Address,
						ProverAddresses: sp.ProverAddresses,
						Cid:             sp.Cid,
						ProofSignature:  sp.ProofSignature,
						Proof:           nil, // Will be set during verification
						Status:          "unresolved",
						BlockHeight:     block.Height,
						TxHash:          tx.Hash,
						CreatedAt:       pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						e.logger.Error("error inserting storage proof", zap.Error(err))
					}
				case *corev1.SignedTransaction_StorageProofVerification:
					insertTxParams.TxType = TxTypeStorageProofVerification
					spv := signedTx.StorageProofVerification
					// Storage proof verification doesn't have a specific address, leave as null
					err = e.db.InsertStorageProofVerification(context.Background(), db.InsertStorageProofVerificationParams{
						Height:      spv.Height,
						Proof:       spv.Proof,
						BlockHeight: block.Height,
						TxHash:      tx.Hash,
						CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
					})
					if err != nil {
						e.logger.Error("error inserting storage proof verification", zap.Error(err))
					} else {
						// Process consensus for this storage proof challenge
						err = e.processStorageProofConsensus(spv.Height, spv.Proof, block.Height, tx.Hash, block.Timestamp.AsTime())
						if err != nil {
							e.logger.Error("error processing storage proof consensus", zap.Error(err))
						}
					}
				case *corev1.SignedTransaction_Attestation:
					at := signedTx.Attestation
					if vr := at.GetValidatorRegistration(); vr != nil {
						insertTxParams.TxType = TxTypeValidatorRegistration
						insertTxParams.Address = pgtype.Text{String: vr.DelegateWallet, Valid: true}
						err = e.db.InsertValidatorRegistration(context.Background(), db.InsertValidatorRegistrationParams{
							Address:      vr.DelegateWallet,
							Endpoint:     vr.Endpoint,
							CometAddress: vr.CometAddress,
							EthBlock:     fmt.Sprintf("%d", vr.EthBlock),
							NodeType:     vr.NodeType,
							Spid:         vr.SpId,
							CometPubkey:  vr.PubKey,
							VotingPower:  vr.Power,
							BlockHeight:  block.Height,
							TxHash:       tx.Hash,
						})
						if err != nil {
							e.logger.Error("error inserting validator registration", zap.Error(err))
						}
						// insert RegisteredValidator record
						err = e.db.RegisterValidator(context.Background(), db.RegisterValidatorParams{
							Address:        vr.DelegateWallet,
							Endpoint:       vr.Endpoint,
							CometAddress:   vr.CometAddress,
							NodeType:       vr.NodeType,
							Spid:           vr.SpId,
							VotingPower:    vr.Power,
							Status:         "active",
							RegisteredAt:   block.Height,
							DeregisteredAt: pgtype.Int8{Valid: false},
							CreatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
							UpdatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
						})
						if err != nil {
							e.logger.Error("error registering validator", zap.Error(err))
						}
					}
					if vd := at.GetValidatorDeregistration(); vd != nil {
						insertTxParams.TxType = TxTypeValidatorDeregistration
						// For attestation deregistration we only have comet address, need to look up eth address
						// For now use comet address, can be improved later
						insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
						err = e.db.InsertValidatorDeregistration(context.Background(), db.InsertValidatorDeregistrationParams{
							CometAddress: vd.CometAddress,
							CometPubkey:  vd.PubKey,
							BlockHeight:  block.Height,
							TxHash:       tx.Hash,
						})
						if err != nil {
							e.logger.Error("error inserting validator deregistration", zap.Error(err))
						}
						// insert DeregisteredValidator record
						err = e.db.DeregisterValidator(context.Background(), db.DeregisterValidatorParams{
							DeregisteredAt: pgtype.Int8{Int64: block.Height, Valid: true},
							UpdatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
							Status:         "deregistered",
							CometAddress:   vd.CometAddress,
						})
						if err != nil {
							e.logger.Error("error deregistering validator", zap.Error(err))
						}
					}
				}

			err = e.db.InsertTransaction(context.Background(), insertTxParams)
			if err != nil {
				e.logger.Error("error inserting transaction", zap.String("tx", tx.Hash), zap.Error(err))
			}
		}

		blockElapsed := time.Since(blockStart)
		blocksProcessed++
		txsProcessed += int64(len(block.Transactions))

		if time.Since(lastLog) >= 10*time.Second {
			elapsed := time.Since(indexStart).Seconds()
			blocksPerSec := float64(blocksProcessed) / elapsed
			txsPerSec := float64(txsProcessed) / elapsed
			blocksBehind := pb.CurrentHeight - block.Height

			e.logger.Info("indexing progress",
				zap.Int64("block", block.Height),
				zap.Int64("blocks_behind", blocksBehind),
				zap.Int("txs_in_block", len(block.Transactions)),
				zap.Int("em_txs", emTxCount),
				zap.Int("em_rejected", emRejectCount),
				zap.Duration("block_time", blockElapsed),
				zap.Float64("blocks_per_sec", blocksPerSec),
				zap.Float64("txs_per_sec", txsPerSec),
				zap.Int64("total_blocks", blocksProcessed),
				zap.Int64("total_txs", txsProcessed),
				zap.Int("prefetch_buffered", len(pf.C())),
			)
			lastLog = time.Now()
		}

		// TODO: use pgnotify to publish block and play events to pubsub

		if e.endingBlockHeight > 0 && block.Height >= e.endingBlockHeight {
			e.logger.Info("ending block height reached, stopping etl service")
			return nil
		}
	}

	return nil
}

func (e *Indexer) startPgNotifyListener(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, e.dbURL)
	if err != nil {
		return fmt.Errorf("failed to connect for notifications: %w", err)
	}
	defer conn.Close(ctx)

	// Listen to both channels
	_, err = conn.Exec(ctx, "LISTEN new_block")
	if err != nil {
		return fmt.Errorf("failed to listen to new_block: %w", err)
	}

	_, err = conn.Exec(ctx, "LISTEN new_plays")
	if err != nil {
		return fmt.Errorf("failed to listen to new_plays: %w", err)
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("error waiting for notification: %w", err)
		}

		switch notification.Channel {
		case "new_block":
			block := &db.EtlBlock{}
			err = json.Unmarshal([]byte(notification.Payload), block)
			if err != nil {
				e.logger.Error("error unmarshalling block", zap.Error(err))
				continue
			}
			if e.blockPubsub.HasSubscribers(BlockTopic) {
				e.blockPubsub.Publish(context.Background(), BlockTopic, block)
			}
		case "new_plays":
			play := &db.EtlPlay{}
			err = json.Unmarshal([]byte(notification.Payload), play)
			if err != nil {
				e.logger.Error("error unmarshalling play", zap.Error(err))
				continue
			}
			if e.playPubsub.HasSubscribers(PlayTopic) {
				e.playPubsub.Publish(context.Background(), PlayTopic, play)
			}
		}
	}
}

// calculateChallengeStatistics aggregates storage proof challenge data for validators within a block range
// NOTE: This function may be called before all storage proof data for the block range is available,
// leading to potentially inaccurate pre-calculated statistics. Consider calculating these dynamically
// in the UI instead of storing them in the database.
func (e *Indexer) calculateChallengeStatistics(blockStart, blockEnd int64) (map[string]ChallengeStats, error) {
	ctx := context.Background()
	stats := make(map[string]ChallengeStats)

	// Use the ETL database method to get challenge statistics with proper status tracking
	results, err := e.db.GetChallengeStatisticsForBlockRange(ctx, db.GetChallengeStatisticsForBlockRangeParams{
		Height:   blockStart,
		Height_2: blockEnd,
	})
	if err != nil {
		return stats, fmt.Errorf("error querying challenge statistics: %v", err)
	}

	// Convert results to our ChallengeStats map
	for _, result := range results {
		stats[result.Address] = ChallengeStats{
			ChallengesReceived: int32(result.ChallengesReceived),
			ChallengesFailed:   int32(result.ChallengesFailed),
		}
	}

	return stats, nil
}

func (e *Indexer) processStorageProofConsensus(height int64, proof []byte, blockHeight int64, txHash string, blockTime time.Time) error {
	ctx := context.Background()

	// Get all storage proofs for this height
	storageProofs, err := e.db.GetStorageProofsForHeight(ctx, height)
	if err != nil {
		return fmt.Errorf("error getting storage proofs for height %d: %v", height, err)
	}

	if len(storageProofs) == 0 {
		// No storage proofs submitted for this height
		return nil
	}

	// In the ETL context, we can't do cryptographic verification like the core system does,
	// but we can implement simplified consensus logic based on majority agreement.

	// Count consensus on who the expected provers were
	expectedProvers := make(map[string]int)
	for _, sp := range storageProofs {
		for _, proverAddr := range sp.ProverAddresses {
			expectedProvers[proverAddr]++
		}
	}

	// Determine majority threshold (more than half of submitted proofs)
	majorityThreshold := len(storageProofs) / 2

	// Mark proofs as 'pass' if they submitted and were part of majority consensus
	passedProvers := make(map[string]bool)
	for _, sp := range storageProofs {
		if sp.Address != "" && sp.ProofSignature != nil {
			// This prover submitted a proof - mark as passed
			err = e.db.UpdateStorageProofStatus(ctx, db.UpdateStorageProofStatusParams{
				Status:  "pass",
				Proof:   proof,
				Height:  height,
				Address: sp.Address,
			})
			if err != nil {
				e.logger.Error("error updating storage proof status to pass", zap.Error(err))
			} else {
				passedProvers[sp.Address] = true
			}
		}
	}

	// Insert failed storage proofs for validators who were expected by majority but didn't submit
	for expectedProver, voteCount := range expectedProvers {
		if voteCount > majorityThreshold && !passedProvers[expectedProver] {
			// This validator was expected by majority consensus but didn't submit a proof
			err = e.db.InsertFailedStorageProof(ctx, db.InsertFailedStorageProofParams{
				Height:      height,
				Address:     expectedProver,
				BlockHeight: blockHeight,
				TxHash:      txHash,
				CreatedAt:   pgtype.Timestamp{Time: blockTime, Valid: true},
			})
			if err != nil {
				e.logger.Error("error inserting failed storage proof for", zap.String("address", expectedProver), zap.Error(err))
			}
		}
	}

	e.logger.Debug(
		"Processed storage proof consensus",
		zap.Int64("height", height),
		zap.Int("passed", len(passedProvers)),
		zap.Int("expected", len(expectedProvers)),
	)

	return nil
}
