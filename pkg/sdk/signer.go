package sdk

import (
	"crypto/ecdsa"
	"errors"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func (s *AudiusdSDK) ReadPrivKey(path string) error {
	privKey, err := common.LoadPrivateKey(path)
	if err != nil {
		return err
	}

	s.privKey = privKey
	return nil
}

func (s *AudiusdSDK) SetPrivKey(privKey *ecdsa.PrivateKey) {
	s.privKey = privKey
}

func (s *AudiusdSDK) Sign(msg []byte) (string, error) {
	if s.privKey == nil {
		return "", errors.New("private key not set")
	}

	signature, err := common.EthSign(s.privKey, msg)
	if err != nil {
		return "", err
	}

	return signature, nil
}

func (s *AudiusdSDK) RecoverSigner(msg []byte, signature string) (string, error) {
	_, address, err := common.EthRecover(signature, msg)
	if err != nil {
		return "", err
	}

	return address, nil
}

func (s *AudiusdSDK) Address() string {
	if s.privKey == nil {
		return ""
	}
	return crypto.PubkeyToAddress(s.privKey.PublicKey).Hex()
}

func (s *AudiusdSDK) PrivKey() *ecdsa.PrivateKey {
	return s.privKey
}

func (s *AudiusdSDK) Pubkey() *ecdsa.PublicKey {
	if s.privKey == nil {
		return nil
	}
	return &s.privKey.PublicKey
}
