package server

import (
	"context"
	"errors"
	"io"
	"time"

	"connectrpc.com/connect"
	v1storage "github.com/AudiusProject/audiusd/pkg/api/storage/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/mediorum/cidutil"
	"github.com/AudiusProject/audiusd/pkg/mediorum/server/signature"
	"gocloud.dev/gcerrors"
)

const (
	// Default chunk size if client doesn't specify
	defaultChunkSize = 128 * 1024
	// Minimum chunk size to prevent too small requests
	minChunkSize = 4 * 1024
	// Maximum chunk size to prevent too large requests
	maxChunkSize = 1024 * 1024
)

func (s *MediorumServer) streamTrackGRPC(ctx context.Context, req *v1storage.StreamTrackRequest, stream *connect.ServerStream[v1storage.StreamTrackResponse]) error {
	if s.Config.Env != "dev" {
		return connect.NewError(connect.CodeNotFound, errors.New("not found"))
	}

	reqSig := req.Signature
	_, ethAddress, err := common.RecoverPlaySignature(reqSig.Signature, reqSig.Data)
	if err != nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("invalid signature"))
	}

	trackId := reqSig.Data.TrackId

	var cid string
	s.crud.DB.Raw("SELECT cid FROM sound_recordings WHERE track_id = ?", trackId).Scan(&cid)
	if cid == "" {
		return connect.NewError(connect.CodeNotFound, errors.New("track not found"))
	}

	var count int
	s.crud.DB.Raw("SELECT COUNT(*) FROM management_keys WHERE track_id = ? AND address = ?", trackId, ethAddress).Scan(&count)
	if count == 0 {
		s.logger.Debug("sig no match", "signed by", ethAddress)
		return connect.NewError(connect.CodePermissionDenied, errors.New("signer not authorized to access"))
	}

	s.logger.Info("streamTrackGRPC", "ethAddress", ethAddress, "count", count, "trackId", trackId, "cid", cid)

	key := cidutil.ShardCID(cid)

	blob, err := s.bucket.NewReader(ctx, key, nil)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			// If we don't have the file, find a different node
			host := s.findNodeToServeBlob(ctx, cid)
			if host == "" {
				return err
			}
			// TODO: Implement redirect to other node
			return err
		}
		return err
	}
	defer func() {
		if blob != nil {
			blob.Close()
		}
	}()

	go func() {
		// record play event to chain
		signatureData, err := signature.GenerateListenTimestampAndSignature(s.Config.privateKey)
		if err != nil {
			s.logger.Error("unable to build request", "err", err)
			return
		}

		ip := common.GetClientIP(ctx)
		geoData, err := s.getGeoFromIP(ip)
		if err != nil {
			s.logger.Error("core plays bad ip: %v", err)
			return
		}

		parsedTime, err := time.Parse(time.RFC3339, signatureData.Timestamp)
		if err != nil {
			s.logger.Error("core error parsing time:", "err", err)
			return
		}

		s.playEventQueue.pushPlayEvent(&PlayEvent{
			UserID:    ethAddress,
			TrackID:   reqSig.Data.TrackId,
			PlayTime:  parsedTime,
			Signature: signatureData.Signature,
			City:      geoData.City,
			Country:   geoData.Country,
			Region:    geoData.Region,
		})
	}()

	// Determine chunk size
	chunkSize := defaultChunkSize
	reqChunkSize := req.ChunkSize
	if reqChunkSize > 0 {
		// Clamp chunk size between min and max
		if reqChunkSize < minChunkSize {
			chunkSize = minChunkSize
		} else if reqChunkSize > maxChunkSize {
			chunkSize = maxChunkSize
		} else {
			chunkSize = int(reqChunkSize)
		}
	}

	// Stream the file in chunks
	buffer := make([]byte, chunkSize)
	for {
		n, err := blob.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if err := stream.Send(&v1storage.StreamTrackResponse{
			Data: buffer[:n],
		}); err != nil {
			return err
		}
	}

	return nil
}
