package server

import (
	"time"

	"github.com/AudiusProject/audiusd/pkg/core/sdk"
)

func (ss *MediorumServer) getCoreSdk() (*sdk.Sdk, error) {
	// await core sdk ready
	<-ss.coreSdkReady
	return ss.coreSdk, nil
}

func (ss *MediorumServer) initCoreSdk() error {
	grpc1 := "audiusd:50051"
	grpc2 := "0.0.0.0:50051"
	maxAttempts := 3
	retryDelay := 5 * time.Second

	for _, endpoint := range []string{grpc1, grpc2} {
		for attempt := 0; attempt < maxAttempts; attempt++ {
			coreSdk, err := sdk.NewSdk(sdk.WithGrpcendpoint(endpoint))
			if err == nil {
				ss.coreSdk = coreSdk
				close(ss.coreSdkReady)
				return nil
			}
			time.Sleep(retryDelay)
		}
	}

	// if exhausted all attempts on both endpoints, keep trying grpc1 indefinitely
	for {
		time.Sleep(retryDelay)
		coreSdk, err := sdk.NewSdk(sdk.WithGrpcendpoint(grpc1))
		if err != nil {
			continue
		}
		ss.coreSdk = coreSdk
		close(ss.coreSdkReady)
		return nil
	}
}
