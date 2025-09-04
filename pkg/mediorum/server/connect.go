package server

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/storage/v1"
	"github.com/AudiusProject/audiusd/pkg/api/storage/v1/v1connect"
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

// GetIPData implements v1connect.StorageServiceHandler.
func (s *StorageService) GetIPData(ctx context.Context, req *connect.Request[v1.GetIPDataRequest]) (*connect.Response[v1.GetIPDataResponse], error) {
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
