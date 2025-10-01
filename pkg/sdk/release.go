package sdk

import (
	"context"
	"errors"
	"io"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	corev1beta1 "github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/hashes"
	"github.com/AudiusProject/audiusd/pkg/sdk/mediorum"
	"google.golang.org/protobuf/proto"
)

type UploadAndReleaseResult struct {
	// Upload results
	UploadID      string
	OriginalCID   string
	TranscodedCID string

	// ERN results
	ERNAddress        string
	ResourceAddresses []string
	ReleaseAddresses  []string

	// Transaction hash
	TxHash string

	// Stream URLs
	StreamURLs map[string]*corev1.GetStreamURLsResponse_EntityStreamURLs
}

func (s *AudiusdSDK) UploadAndRelease(
	ctx context.Context,
	file io.ReadSeeker,
	filename string,
	uploadOpts *mediorum.UploadOptions,
	ernMessage *ddexv1beta1.NewReleaseMessage,
) (*UploadAndReleaseResult, error) {
	if s.privKey == nil {
		return nil, errors.New("private key not set")
	}

	// 1. Compute CID and generate upload signature
	fileCID, err := hashes.ComputeFileCID(file)
	if err != nil {
		return nil, err
	}
	file.Seek(0, 0) // Reset file position

	uploadSigData := &corev1.UploadSignature{Cid: fileCID}
	uploadSigBytes, err := proto.Marshal(uploadSigData)
	if err != nil {
		return nil, err
	}

	uploadSignature, err := common.EthSign(s.privKey, uploadSigBytes)
	if err != nil {
		return nil, err
	}

	// 2. Upload file with parallel transcoding and FileUpload polling
	if uploadOpts == nil {
		uploadOpts = &mediorum.UploadOptions{}
	}
	uploadOpts.Signature = uploadSignature
	uploadOpts.WaitForTranscode = true
	uploadOpts.WaitForFileUpload = true
	uploadOpts.OriginalCID = fileCID

	uploads, err := s.Mediorum.UploadFile(ctx, file, filename, uploadOpts)
	if err != nil {
		return nil, err
	}
	if len(uploads) == 0 {
		return nil, errors.New("no uploads returned")
	}

	upload := uploads[0]
	if upload.Status != "done" {
		return nil, errors.New("upload failed: " + upload.Error)
	}

	transcodedCID := upload.GetTranscodedCID()

	// 4. Send ERN transaction
	envelope := &corev1beta1.Envelope{
		Header: &corev1beta1.EnvelopeHeader{
			ChainId:    s.ChainID(),
			From:       s.Address(),
			Nonce:      upload.ID, // Use upload ID as nonce
			Expiration: time.Now().Add(time.Hour).Unix(),
		},
		Messages: []*corev1beta1.Message{
			{
				Message: &corev1beta1.Message_Ern{
					Ern: ernMessage,
				},
			},
		},
	}

	transaction := &corev1beta1.Transaction{Envelope: envelope}

	submitRes, err := s.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transactionv2: transaction,
	}))
	if err != nil {
		return nil, err
	}

	ernReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[0].GetErnAck()
	if ernReceipt == nil {
		return nil, errors.New("failed to get ERN receipt")
	}

	result := &UploadAndReleaseResult{
		UploadID:          upload.ID,
		OriginalCID:       upload.OrigFileCID,
		TranscodedCID:     transcodedCID,
		ERNAddress:        ernReceipt.ErnAddress,
		ResourceAddresses: ernReceipt.ResourceAddresses,
		ReleaseAddresses:  ernReceipt.ReleaseAddresses,
		TxHash:            submitRes.Msg.TransactionReceipt.TxHash,
	}

	return result, nil
}
