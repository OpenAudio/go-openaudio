package common

import (
	"fmt"

	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	"github.com/AudiusProject/audiusd/pkg/core/gen/models"
	"github.com/go-openapi/strfmt"
)

// converts a proto signed tx into an oapi one, mainly used for tx broadcasting
// only covers entitymanager and plays
func SignedTxProtoIntoSignedTxOapi(tx *core_proto.SignedTransaction) *models.ProtocolSignedTransaction {
	oapiTx := &models.ProtocolSignedTransaction{
		RequestID: tx.RequestId,
		Signature: tx.Signature,
	}

	switch innerTx := tx.Transaction.(type) {
	case *core_proto.SignedTransaction_Plays:
		plays := []*models.ProtocolTrackPlay{}

		for _, play := range innerTx.Plays.GetPlays() {
			plays = append(plays, &models.ProtocolTrackPlay{
				UserID:    play.UserId,
				TrackID:   play.TrackId,
				Signature: play.Signature,
				Timestamp: strfmt.DateTime(play.Timestamp.AsTime()),
				City:      play.City,
				Country:   play.Country,
				Region:    play.Region,
			})
		}

		oapiTx.Plays = &models.ProtocolTrackPlays{
			Plays: plays,
		}
	case *core_proto.SignedTransaction_ManageEntity:
		oapiTx.ManageEntity = &models.ProtocolManageEntityLegacy{
			Action:     innerTx.ManageEntity.Action,
			EntityID:   fmt.Sprint(innerTx.ManageEntity.EntityId),
			EntityType: innerTx.ManageEntity.EntityType,
			Metadata:   innerTx.ManageEntity.Metadata,
			UserID:     fmt.Sprint(innerTx.ManageEntity.UserId),
			Signature:  innerTx.ManageEntity.Signature,
			Signer:     innerTx.ManageEntity.Signer,
			Nonce:      fmt.Sprint(innerTx.ManageEntity.Nonce),
		}
	case *core_proto.SignedTransaction_Attestation:
		oapiTx.Attestation = &models.ProtocolAttestation{
			Signatures: innerTx.Attestation.Signatures,
		}
		switch innerTx.Attestation.Body.(type) {
		case *core_proto.Attestation_ValidatorRegistration:
			oapiTx.Attestation.ValidatorRegistration = ValidatorRegistrationIntoOapi(innerTx.Attestation.GetValidatorRegistration())
		case *core_proto.Attestation_ValidatorDeregistration:
			oapiTx.Attestation.ValidatorDeregistration = ValidatorDeregistrationIntoOapi(innerTx.Attestation.GetValidatorDeregistration())
		}
	case *core_proto.SignedTransaction_ValidatorRegistration:
		oapiTx.ValidatorRegistration = &models.ProtocolValidatorRegistrationLegacy{
			CometAddress: innerTx.ValidatorRegistration.CometAddress,
			Endpoint:     innerTx.ValidatorRegistration.Endpoint,
			EthBlock:     innerTx.ValidatorRegistration.EthBlock,
			NodeType:     innerTx.ValidatorRegistration.NodeType,
			SpID:         innerTx.ValidatorRegistration.SpId,
			Power:        fmt.Sprint(innerTx.ValidatorRegistration.Power),
			PubKey:       innerTx.ValidatorRegistration.PubKey,
		}
	case *core_proto.SignedTransaction_ValidatorDeregistration:
		oapiTx.ValidatorDeregistration = &models.ProtocolValidatorMisbehaviorDeregistration{
			CometAddress: innerTx.ValidatorDeregistration.CometAddress,
			PubKey:       innerTx.ValidatorDeregistration.PubKey,
		}
	case *core_proto.SignedTransaction_StorageProof:
		oapiTx.StorageProof = &models.ProtocolStorageProof{
			Height:          fmt.Sprint(innerTx.StorageProof.Height),
			Address:         innerTx.StorageProof.Address,
			Cid:             innerTx.StorageProof.Cid,
			ProverAddresses: innerTx.StorageProof.ProverAddresses,
			ProofSignature:  innerTx.StorageProof.ProofSignature,
		}
	case *core_proto.SignedTransaction_StorageProofVerification:
		oapiTx.StorageProofVerification = &models.ProtocolStorageProofVerification{
			Height: fmt.Sprint(innerTx.StorageProofVerification.Height),
			Proof:  innerTx.StorageProofVerification.Proof,
		}
	}

	return oapiTx
}

func ValidatorRegistrationIntoOapi(vr *core_proto.ValidatorRegistration) *models.ProtocolValidatorRegistration {
	return &models.ProtocolValidatorRegistration{
		DelegateWallet: vr.DelegateWallet,
		Endpoint:       vr.Endpoint,
		NodeType:       vr.NodeType,
		EthBlock:       fmt.Sprint(vr.EthBlock),
		SpID:           vr.SpId,
		CometAddress:   vr.CometAddress,
		Power:          fmt.Sprint(vr.Power),
		PubKey:         vr.PubKey,
		Deadline:       fmt.Sprint(vr.Deadline),
	}
}

func ValidatorDeregistrationIntoOapi(vr *core_proto.ValidatorDeregistration) *models.ProtocolValidatorDeregistration {
	return &models.ProtocolValidatorDeregistration{
		CometAddress: vr.CometAddress,
		PubKey:       vr.PubKey,
		Deadline:     fmt.Sprint(vr.Deadline),
	}
}
