package server

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/storage/v1/v1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ v1connect.StorageServiceHandler = (*StorageService)(nil)

type StorageService struct {
	mediorum *MediorumServer
}

func NewStorageService() *StorageService {
	return &StorageService{}
}

func (s *StorageService) SetMediorum(mediorum *MediorumServer) {
	s.mediorum = mediorum
}

// InvalidateTrackAccessCacheForTrack clears cached track access info when management_keys change.
func (s *StorageService) InvalidateTrackAccessCacheForTrack(trackID string) {
	if s.mediorum != nil {
		s.mediorum.InvalidateTrackAccessCacheForTrack(trackID)
	}
}

// GetHealth implements v1connect.StorageServiceHandler.
func (s *StorageService) GetHealth(context.Context, *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	return connect.NewResponse(&v1.GetHealthResponse{}), nil
}

// Ping implements v1connect.StorageServiceHandler.
func (s *StorageService) Ping(context.Context, *connect.Request[v1.PingRequest]) (*connect.Response[v1.PingResponse], error) {
	return connect.NewResponse(&v1.PingResponse{Message: "pong"}), nil
}

// GetUpload implements v1connect.StorageServiceHandler.
func (s *StorageService) GetUpload(ctx context.Context, req *connect.Request[v1.GetUploadRequest]) (*connect.Response[v1.GetUploadResponse], error) {
	if s.mediorum == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("storage service not initialized"))
	}
	dbUpload, err := s.mediorum.serveUpload(ctx, req.Msg.Id, req.Msg.Fix, req.Msg.Analyze)
	if err != nil {
		return nil, err
	}

	// Convert FFProbeResult to proto FFProbeResult
	var probe *v1.FFProbeResult
	if dbUpload.FFProbe != nil {
		probe = &v1.FFProbeResult{
			Format: &v1.FFProbeResult_Format{
				Filename:       dbUpload.FFProbe.Format.Filename,
				FormatName:     dbUpload.FFProbe.Format.FormatName,
				FormatLongName: dbUpload.FFProbe.Format.FormatLongName,
				Duration:       dbUpload.FFProbe.Format.Duration,
				Size:           dbUpload.FFProbe.Format.Size,
				BitRate:        dbUpload.FFProbe.Format.BitRate,
			},
		}
	}

	// Convert AudioAnalysisResult to proto AudioAnalysisResult
	var audioAnalysisResults *v1.AudioAnalysisResult
	if dbUpload.AudioAnalysisResults != nil {
		audioAnalysisResults = &v1.AudioAnalysisResult{
			Bpm: dbUpload.AudioAnalysisResults.BPM,
			Key: dbUpload.AudioAnalysisResults.Key,
		}
	}

	upload := &v1.Upload{
		Id:                      dbUpload.ID,
		UserWallet:              dbUpload.UserWallet.String,
		Template:                string(dbUpload.Template),
		OrigFilename:            dbUpload.OrigFileName,
		OrigFileCid:             dbUpload.OrigFileCID,
		SelectedPreview:         dbUpload.SelectedPreview.String,
		Probe:                   probe,
		Error:                   dbUpload.Error,
		ErrorCount:              int32(dbUpload.ErrorCount),
		Mirrors:                 dbUpload.Mirrors,
		TranscodedMirrors:       dbUpload.TranscodedMirrors,
		Status:                  dbUpload.Status,
		PlacementHosts:          dbUpload.PlacementHosts,
		CreatedBy:               dbUpload.CreatedBy,
		CreatedAt:               timestamppb.New(dbUpload.CreatedAt),
		UpdatedAt:               timestamppb.New(dbUpload.UpdatedAt),
		TranscodedBy:            dbUpload.TranscodedBy,
		TranscodeProgress:       dbUpload.TranscodeProgress,
		TranscodedAt:            timestamppb.New(dbUpload.TranscodedAt),
		TranscodeResults:        dbUpload.TranscodeResults,
		AudioAnalysisStatus:     dbUpload.AudioAnalysisStatus,
		AudioAnalysisError:      dbUpload.AudioAnalysisError,
		AudioAnalysisErrorCount: int32(dbUpload.AudioAnalysisErrorCount),
		AudioAnalyzedBy:         dbUpload.AudioAnalyzedBy,
		AudioAnalyzedAt:         timestamppb.New(dbUpload.AudioAnalyzedAt),
		AudioAnalysisResults:    audioAnalysisResults,
	}

	return connect.NewResponse(&v1.GetUploadResponse{
		Upload: upload,
	}), nil
}

// UploadFiles implements v1connect.StorageServiceHandler.
func (s *StorageService) UploadFiles(ctx context.Context, req *connect.Request[v1.UploadFilesRequest]) (*connect.Response[v1.UploadFilesResponse], error) {
	if s.mediorum == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("storage service not initialized"))
	}
	placeHosts := strings.Join(req.Msg.PlacementHosts, ",")
	files := make([]*multipart.FileHeader, len(req.Msg.Files))
	for i, file := range req.Msg.Files {
		formFile, err := s.mediorum.createMultipartFileHeader(file.Filename, file.Data)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to prepare file %s: %w", file.Filename, err))
		}
		files[i] = formFile
	}

	uploads, err := s.mediorum.uploadFile(ctx, req.Msg.Signature, req.Msg.UserWallet, req.Msg.Template, req.Msg.PreviewStart, placeHosts, files)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to upload file: %w", err))
	}

	res := make([]*v1.Upload, len(uploads))
	for i, upload := range uploads {
		var probe *v1.FFProbeResult
		if upload.FFProbe != nil {
			probe = &v1.FFProbeResult{
				Format: &v1.FFProbeResult_Format{
					Filename:       upload.FFProbe.Format.Filename,
					FormatName:     upload.FFProbe.Format.FormatName,
					FormatLongName: upload.FFProbe.Format.FormatLongName,
					Duration:       upload.FFProbe.Format.Duration,
					Size:           upload.FFProbe.Format.Size,
					BitRate:        upload.FFProbe.Format.BitRate,
				},
			}
		}

		// Convert AudioAnalysisResult to proto AudioAnalysisResult
		var audioAnalysisResults *v1.AudioAnalysisResult
		if upload.AudioAnalysisResults != nil {
			audioAnalysisResults = &v1.AudioAnalysisResult{
				Bpm: upload.AudioAnalysisResults.BPM,
				Key: upload.AudioAnalysisResults.Key,
			}
		}

		res[i] = &v1.Upload{
			Id:                      upload.ID,
			UserWallet:              upload.UserWallet.String,
			Template:                string(upload.Template),
			OrigFilename:            upload.OrigFileName,
			OrigFileCid:             upload.OrigFileCID,
			SelectedPreview:         upload.SelectedPreview.String,
			Probe:                   probe,
			Error:                   upload.Error,
			ErrorCount:              int32(upload.ErrorCount),
			Mirrors:                 upload.Mirrors,
			TranscodedMirrors:       upload.TranscodedMirrors,
			Status:                  upload.Status,
			PlacementHosts:          upload.PlacementHosts,
			CreatedBy:               upload.CreatedBy,
			CreatedAt:               timestamppb.New(upload.CreatedAt),
			UpdatedAt:               timestamppb.New(upload.UpdatedAt),
			TranscodedBy:            upload.TranscodedBy,
			TranscodeProgress:       upload.TranscodeProgress,
			TranscodedAt:            timestamppb.New(upload.TranscodedAt),
			TranscodeResults:        upload.TranscodeResults,
			AudioAnalysisStatus:     upload.AudioAnalysisStatus,
			AudioAnalysisError:      upload.AudioAnalysisError,
			AudioAnalysisErrorCount: int32(upload.AudioAnalysisErrorCount),
			AudioAnalyzedBy:         upload.AudioAnalyzedBy,
			AudioAnalyzedAt:         timestamppb.New(upload.AudioAnalyzedAt),
			AudioAnalysisResults:    audioAnalysisResults,
		}
	}

	return connect.NewResponse(&v1.UploadFilesResponse{Uploads: res}), nil
}

// StreamTrack implements v1connect.StorageServiceHandler.
func (s *StorageService) StreamTrack(ctx context.Context, req *connect.Request[v1.StreamTrackRequest], stream *connect.ServerStream[v1.StreamTrackResponse]) error {
	return connect.NewError(connect.CodeNotFound, errors.New("unimplemented"))
}

// GetStreamURL implements v1connect.StorageServiceHandler.
func (s *StorageService) GetStreamURL(ctx context.Context, req *connect.Request[v1.GetStreamURLRequest]) (*connect.Response[v1.GetStreamURLResponse], error) {
	return nil, connect.NewError(connect.CodeNotFound, errors.New("unimplemented"))
}

// GetRendezvousNodes implements v1connect.StorageServiceHandler.
func (s *StorageService) GetRendezvousNodes(ctx context.Context, req *connect.Request[v1.GetRendezvousNodesRequest]) (*connect.Response[v1.GetRendezvousNodesResponse], error) {
	if s.mediorum == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("storage service not initialized"))
	}
	cid := req.Msg.Cid
	if cid == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cid is required"))
	}

	replicationFactor := int(req.Msg.ReplicationFactor)
	if replicationFactor <= 0 {
		replicationFactor = s.mediorum.Config.ReplicationFactor
	}

	// Use the existing rendezvous hasher to get all nodes in ranked order
	orderedHosts := s.mediorum.rendezvousHasher.Rank(cid)

	// Return the top N nodes based on replication factor
	topNodes := orderedHosts
	if replicationFactor < len(orderedHosts) {
		topNodes = orderedHosts[:replicationFactor]
	}

	return connect.NewResponse(&v1.GetRendezvousNodesResponse{
		Nodes: topNodes,
	}), nil
}

// GetIPData implements v1connect.StorageServiceHandler.
func (s *StorageService) GetIPData(ctx context.Context, req *connect.Request[v1.GetIPDataRequest]) (*connect.Response[v1.GetIPDataResponse], error) {
	if s.mediorum == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("storage service not initialized"))
	}
	ip := req.Msg.Ip
	if ip == "" {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("must send IP"))
	}

	ipData, err := s.mediorum.getGeoFromIP(ip)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	res := &v1.GetIPDataResponse{
		Country:     ipData.Country,
		CountryCode: ipData.CountryCode,
		Region:      ipData.Region,
		RegionCode:  ipData.RegionCode,
		City:        ipData.City,
		Latitude:    float32(ipData.Latitude),
		Longitude:   float32(ipData.Longitude),
	}
	return connect.NewResponse(res), nil
}

// GetStatus implements v1connect.StorageServiceHandler.
func (s *StorageService) GetStatus(context.Context, *connect.Request[v1.GetStatusRequest]) (*connect.Response[v1.GetStatusResponse], error) {
	storageExpectation := int64(0)
	if s.mediorum != nil {
		storageExpectation = int64(s.mediorum.storageExpectation)
	}
	return connect.NewResponse(&v1.GetStatusResponse{
		StorageExpectation: storageExpectation,
	}), nil
}

// GetMediorumHealth returns the health check data for the mediorum process
func (s *StorageService) GetMediorumHealth() (HealthData, error) {
	if s.mediorum == nil {
		return HealthData{}, errors.New("mediorum not initialized")
	}

	data := s.mediorum.getHealth()
	return data, nil
}

// GetStorageDiagnostics implements v1connect.StorageServiceHandler.
func (s *StorageService) GetStorageDiagnostics(ctx context.Context, _ *connect.Request[v1.GetStorageDiagnosticsRequest]) (*connect.Response[v1.GetStorageDiagnosticsResponse], error) {
	if s.mediorum == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("mediorum not initialized"))
	}
	ss := s.mediorum

	blobStorePrefix, _, _ := strings.Cut(ss.Config.BlobStoreDSN, "://")
	archiveBlobStorePrefix, _, _ := strings.Cut(ss.Config.ArchiveBlobStoreDSN, "://")
	archiveConfigured := ss.archiveBucket != nil

	resp := &v1.GetStorageDiagnosticsResponse{
		SelfHost:                ss.Config.Self.Host,
		DiskUsedBytes:           int64(ss.mediorumPathUsed),
		DiskTotalBytes:          int64(ss.mediorumPathSize),
		StorageExpectationBytes: int64(ss.storageExpectation),
		DiskHasSpace:            ss.diskHasSpace(),
		PrimaryDiskHasSpace:     ss.dsnHasSpace(ss.Config.BlobStoreDSN, ss.mediorumPathFree),
		ReplicationFactor:       int32(ss.Config.ReplicationFactor),
		UploadsCount:            ss.uploadsCount,
		BlobStorePrefix:         blobStorePrefix,
		ArchiveConfigured:       archiveConfigured,
		ArchiveDiskUsedBytes:    int64(ss.archivePathUsed),
		ArchiveDiskTotalBytes:   int64(ss.archivePathSize),
		ArchiveBlobStorePrefix:  archiveBlobStorePrefix,
		ArchiveDiskHasSpace:     archiveConfigured && ss.dsnHasSpace(ss.Config.ArchiveBlobStoreDSN, ss.archivePathFree),
		StoreAll:                ss.Config.StoreAll,
		LastSuccessfulRepair:    repairRunToProto(ss.lastSuccessfulRepair),
		LastSuccessfulCleanup:   repairRunToProto(ss.lastSuccessfulCleanup),
		Waveforms:               ss.waveformStatusProto(ctx),
	}

	// recent repair runs + in-progress run
	var recent []RepairTracker
	if err := ss.crud.DB.Order("started_at desc").Limit(20).Find(&recent).Error; err == nil {
		for i := range recent {
			if recent[i].FinishedAt.IsZero() && resp.InProgressRepair == nil {
				resp.InProgressRepair = repairRunToProto(recent[i])
				continue
			}
			resp.RecentRepairRuns = append(resp.RecentRepairRuns, repairRunToProto(recent[i]))
		}
	}

	// peers
	ss.peerHealthsMutex.RLock()
	for host, ph := range ss.peerHealths {
		if ph == nil {
			continue
		}
		resp.Peers = append(resp.Peers, &v1.PeerStatus{
			Host:               host,
			LastReachable:      timeToProto(ph.LastReachable),
			LastHealthy:        timeToProto(ph.LastHealthy),
			ReachablePeerCount: int32(len(ph.ReachablePeers)),
		})
	}
	ss.peerHealthsMutex.RUnlock()

	// served counts: last 30 days
	thirty := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -30)
	var metrics []DailyMetrics
	if err := ss.crud.DB.Where("timestamp >= ?", thirty).Order("timestamp asc").Find(&metrics).Error; err == nil {
		for _, m := range metrics {
			resp.ServedCounts = append(resp.ServedCounts, &v1.DailyServedCount{
				Day:    timestamppb.New(m.Timestamp),
				Action: m.Action,
				Count:  m.Count,
			})
		}
	}

	// process-local counters & ring buffers
	if ss.metrics != nil {
		resp.ReadRepair = &v1.ReadRepairStats{
			LocalHits:     ss.metrics.readLocalHits.Load(),
			Misses:        ss.metrics.readMisses.Load(),
			PullAttempts:  ss.metrics.readPullAttempts.Load(),
			PullSuccesses: ss.metrics.readPullSuccesses.Load(),
			Proxied:       ss.metrics.readProxied.Load(),
			Redirected:    ss.metrics.readRedirected.Load(),
		}
		resp.Pos = &v1.PoSStats{
			Attempted: ss.metrics.posAttempted.Load(),
			Passed:    ss.metrics.posPassed.Load(),
			Failed:    ss.metrics.posFailed.Load(),
		}
		resp.Bandwidth = &v1.BandwidthStats{
			IngressBytes: ss.metrics.bytesIngress.Load(),
			EgressBytes:  ss.metrics.bytesEgress.Load(),
			Since:        timestamppb.New(ss.StartedAt),
		}
		for _, p := range ss.metrics.snapshotRecentPoS() {
			resp.RecentPosChallenges = append(resp.RecentPosChallenges, &v1.PoSChallenge{
				At:    timestamppb.New(p.At),
				Cid:   p.CID,
				Ok:    p.OK,
				Error: p.Error,
			})
		}
		for _, s := range ss.metrics.snapshotRecentServed() {
			resp.RecentServed = append(resp.RecentServed, &v1.ServedEvent{
				At:     timestamppb.New(s.At),
				Cid:    s.CID,
				Action: s.Action,
			})
		}
	}

	return connect.NewResponse(resp), nil
}

func repairRunToProto(r RepairTracker) *v1.RepairRun {
	if r.StartedAt.IsZero() {
		return nil
	}
	counters := make(map[string]int64, len(r.Counters))
	for k, v := range r.Counters {
		counters[k] = int64(v)
	}
	// Sum the per-CID successful actions this run took. Keys are produced
	// by the repair loop in pkg/mediorum/server/repair.go.
	filesChanged := int64(counters["pull_mine_success"]) +
		int64(counters["delete_invalid_success"]) +
		int64(counters["delete_resized_image_ok"]) +
		int64(counters["delete_over_replicated_success"])
	return &v1.RepairRun{
		StartedAt:        timeToProto(r.StartedAt),
		UpdatedAt:        timeToProto(r.UpdatedAt),
		FinishedAt:       timeToProto(r.FinishedAt),
		CleanupMode:      r.CleanupMode,
		ContentSizeBytes: r.ContentSize,
		DurationNs:       int64(r.Duration),
		AbortedReason:    r.AbortedReason,
		CursorI:          int32(r.CursorI),
		Counters:         counters,
		FilesChanged:     filesChanged,
	}
}

func timeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
