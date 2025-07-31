package integration_tests

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	corev1beta1 "github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	v1storage "github.com/AudiusProject/audiusd/pkg/api/storage/v1"
	"github.com/AudiusProject/audiusd/pkg/integration_tests/utils"
	auds "github.com/AudiusProject/audiusd/pkg/sdk"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUploadStream(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, utils.WaitForDevnetHealthy(30*time.Second))

	serverAddr := "node3.audiusd.devnet"
	privKeyPath := "./assets/demo_key.txt"
	// privKeyPath2 := "./assets/demo_key2.txt"

	sdk := auds.NewAudiusdSDK(serverAddr)
	if err := sdk.ReadPrivKey(privKeyPath); err != nil {
		require.Nil(t, err, "failed to read private key: %w", err)
	}

	audioFile, err := os.Open("./assets/anxiety-upgrade.mp3")
	require.Nil(t, err, "failed to open file")
	defer audioFile.Close()
	audioFileBytes, err := io.ReadAll(audioFile)
	require.Nil(t, err, "failed to read file")

	// upload the track
	uploadFileRes, err := sdk.Storage.UploadFiles(ctx, &connect.Request[v1storage.UploadFilesRequest]{
		Msg: &v1storage.UploadFilesRequest{
			UserWallet: sdk.Address(),
			Template:   "audio",
			Files: []*v1storage.File{
				{
					Filename: "anxiety-upgrade.mp3",
					Data:     audioFileBytes,
				},
			},
		},
	})
	require.Nil(t, err, "failed to upload file")
	require.EqualValues(t, 1, len(uploadFileRes.Msg.Uploads), "failed to upload file")

	upload := uploadFileRes.Msg.Uploads[0]

	// get the upload info
	uploadRes, err := sdk.Storage.GetUpload(ctx, &connect.Request[v1storage.GetUploadRequest]{
		Msg: &v1storage.GetUploadRequest{
			Id: upload.Id,
		},
	})
	require.Nil(t, err, "failed to get upload")
	require.EqualValues(t, upload.Id, uploadRes.Msg.Upload.Id, "failed to get upload")
	require.EqualValues(t, upload.UserWallet, uploadRes.Msg.Upload.UserWallet, "failed to get upload")
	require.EqualValues(t, upload.OrigFileCid, uploadRes.Msg.Upload.OrigFileCid, "failed to get upload")
	require.EqualValues(t, upload.OrigFilename, uploadRes.Msg.Upload.OrigFilename, "failed to get upload")

	// release the track
	title := "Anxiety Upgrade"
	genre := "Electronic"

	// create ERN track release with upload cid
	envelope := &corev1beta1.Envelope{
		Header: &corev1beta1.EnvelopeHeader{
			ChainId:    "audius-devnet",
			From:       sdk.Address(),
			Nonce:      uuid.New().String(),
			Expiration: time.Now().Add(time.Hour).Unix(),
		},
		Messages: []*corev1beta1.Message{
			{
				Message: &corev1beta1.Message_Ern{
					Ern: &ddexv1beta1.NewReleaseMessage{
						MessageHeader: &ddexv1beta1.MessageHeader{
							MessageId: fmt.Sprintf("upload_%s", uuid.New().String()),
							MessageSender: &ddexv1beta1.MessageSender{
								PartyId: &ddexv1beta1.Party_PartyId{
									Dpid: sdk.Address(),
								},
							},
							MessageCreatedDateTime: timestamppb.Now(),
							MessageControlType:     ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_NEW_MESSAGE.Enum(),
						},
						PartyList: []*ddexv1beta1.Party{
							{
								PartyReference: "P_UPLOADER",
								PartyId: &ddexv1beta1.Party_PartyId{
									Dpid: sdk.Address(),
								},
								PartyName: &ddexv1beta1.Party_PartyName{
									FullName: "Test Uploader",
								},
							},
						},
						ResourceList: []*ddexv1beta1.Resource{
							{
								Resource: &ddexv1beta1.Resource_SoundRecording_{
									SoundRecording: &ddexv1beta1.Resource_SoundRecording{
										ResourceReference:     "A1",
										Type:                  "MusicalWorkSoundRecording",
										DisplayTitleText:      title,
										DisplayArtistName:     "Test Artist",
										VersionType:           "OriginalVersion",
										LanguageOfPerformance: "en",
										SoundRecordingEdition: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition{
											Type: "NonImmersiveEdition",
											ResourceId: &ddexv1beta1.Resource_ResourceId{
												ProprietaryId: []*ddexv1beta1.Resource_ProprietaryId{
													{
														Namespace:     "audius",
														ProprietaryId: upload.OrigFileCid,
													},
												},
											},
											TechnicalDetails: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails{
												TechnicalResourceDetailsReference: "T1",
												DeliveryFile: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile{
													Type:                 "AudioFile",
													AudioCodecType:       "MP3",
													NumberOfChannels:     2,
													SamplingRate:         48.0, // 48kHz as per transcoding
													BitsPerSample:        16,
													IsProvidedInDelivery: true,
													File: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile_File{
														Uri: upload.TranscodeResults["320"], // Use transcoded file CID as URI
														HashSum: &ddexv1beta1.Resource_SoundRecording_SoundRecordingEdition_TechnicalDetails_DeliveryFile_File_HashSum{
															Algorithm:    "IPFS",
															HashSumValue: upload.TranscodeResults["320"],
														},
														FileSize: 1000000, // Placeholder file size
													},
												},
												IsClip: false,
											},
										},
									},
								},
							},
						},
						ReleaseList: []*ddexv1beta1.Release{
							{
								Release: &ddexv1beta1.Release_MainRelease{
									MainRelease: &ddexv1beta1.Release_Release{
										ReleaseReference:      "R1",
										ReleaseType:           "Single",
										DisplayTitleText:      title,
										DisplayArtistName:     "Test Artist",
										ReleaseLabelReference: "P_UPLOADER",
										OriginalReleaseDate:   time.Now().Format("2006-01-02"),
										ParentalWarningType:   "NotExplicit",
										Genre: &ddexv1beta1.Release_Release_Genre{
											GenreText: genre,
										},
										ResourceGroup: &ddexv1beta1.Release_Release_ResourceGroup{
											ResourceGroup: []*ddexv1beta1.Release_Release_ResourceGroup_ResourceGroup{
												{
													ResourceGroupType: "Audio",
													SequenceNumber:    "1",
													ResourceGroupContentItem: []*ddexv1beta1.Release_Release_ResourceGroup_ResourceGroup_ResourceGroupContentItem{
														{
															ResourceGroupContentItemType: "Track",
															ResourceGroupContentItemText: "A1",
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	transaction := &corev1beta1.Transaction{
		Envelope: envelope,
	}

	submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transactionv2: transaction,
	}))
	require.NoError(t, err)

	ernReceipt := submitRes.Msg.TransactionReceipt.MessageReceipts[0].GetErnAck()
	require.NotNil(t, ernReceipt, "failed to get ern ack")

	// TODO: fix streaming test with signed stream URLs
	// // use ern address as track id for now
	// trackID := ernReceipt.ErnAddress

	// // create stream signature
	// data := &v1storage.StreamTrackSignatureData{
	// 	TrackId:   trackID,
	// 	Timestamp: time.Now().Unix(),
	// }
	// sig, sigData, err := common.GeneratePlaySignature(sdk.PrivKey(), data)
	// require.Nil(t, err, "failed to generate stream signature")

	// // stream the file
	// stream, err := sdk.Storage.StreamTrack(ctx, &connect.Request[v1storage.StreamTrackRequest]{
	// 	Msg: &v1storage.StreamTrackRequest{
	// 		Signature: &v1storage.StreamTrackSignature{
	// 			Signature: sig,
	// 			DataHash:  sigData,
	// 			Data:      data,
	// 		},
	// 	},
	// })
	// require.Nil(t, err, "failed to stream file")

	// var fileData bytes.Buffer
	// for stream.Receive() {
	// 	res := stream.Msg()
	// 	if len(res.Data) > 0 {
	// 		fileData.Write(res.Data)
	// 	}
	// }
	// if err := stream.Err(); err != nil {
	// 	log.Fatalf("stream error: %v", err)
	// }

	// // verify another user can't stream the file
	// sdk2 := auds.NewAudiusdSDK(serverAddr)
	// if err := sdk2.ReadPrivKey(privKeyPath2); err != nil {
	// 	require.Nil(t, err, "failed to read private key: %w", err)
	// }

	// // use same data as first user
	// sig2, sigData2, err := common.GeneratePlaySignature(sdk2.PrivKey(), data)
	// require.Nil(t, err, "failed to generate stream signature")

	// stream2, err := sdk2.Storage.StreamTrack(ctx, &connect.Request[v1storage.StreamTrackRequest]{
	// 	Msg: &v1storage.StreamTrackRequest{
	// 		Signature: &v1storage.StreamTrackSignature{
	// 			Signature: sig2,
	// 			DataHash:  sigData2,
	// 			Data:      data,
	// 		},
	// 	},
	// })

	// // consume the stream
	// for stream2.Receive() {
	// }
	// require.Nil(t, err)
	// require.NotNil(t, stream2.Err(), "could not stream file")

	// // get stream url
	// streamURLRes, err := sdk.Storage.GetStreamURL(ctx, &connect.Request[v1storage.GetStreamURLRequest]{
	// 	Msg: &v1storage.GetStreamURLRequest{
	// 		Cid:         upload.OrigFileCid,
	// 		ShouldCache: 1,
	// 		TrackId:     1,
	// 		UserId:      1,
	// 	},
	// })
	// require.Nil(t, err, "failed to get stream url")
	// require.EqualValues(t, 3, len(streamURLRes.Msg.Urls), "failed to get stream url")

	// // stream via grpc a few more times
	// for range 3 {
	// 	// stream the file from the same node
	// 	stream, err := sdk.Storage.StreamTrack(ctx, &connect.Request[v1storage.StreamTrackRequest]{
	// 		Msg: &v1storage.StreamTrackRequest{
	// 			Signature: &v1storage.StreamTrackSignature{
	// 				Signature: sig,
	// 				DataHash:  sigData,
	// 				Data:      data,
	// 			},
	// 		},
	// 	})
	// 	require.Nil(t, err, "failed to stream file")

	// 	var fileData bytes.Buffer
	// 	for stream.Receive() {
	// 		res := stream.Msg()
	// 		if len(res.Data) > 0 {
	// 			fileData.Write(res.Data)
	// 		}
	// 	}
	// 	if err := stream.Err(); err != nil {
	// 		log.Fatalf("stream error: %v", err)
	// 	}
	// }
}
