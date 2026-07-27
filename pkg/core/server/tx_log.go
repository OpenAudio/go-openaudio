package server

import (
	"fmt"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// txPayloadPreviewCap bounds the payload bytes attached to log lines that ship
// to Axiom. Rejected txs persist nowhere, so the preview is the only record of
// their contents — but full payloads at consensus rates flooded ingest during
// the July 2026 outage, so the preview must stay bounded.
const txPayloadPreviewCap = 1024

// txTypeName returns the oneof case of a signed transaction (e.g. "Plays",
// "ManageEntity") so logs carry what kind of tx this is without the payload.
func txTypeName(tx *v1.SignedTransaction) string {
	if tx == nil || tx.GetTransaction() == nil {
		return "none"
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", tx.GetTransaction()), "*v1.SignedTransaction_")
}

// txPayloadPreview renders a tx as protojson truncated to txPayloadPreviewCap
// bytes, for error logs where the full payload would be unbounded.
func txPayloadPreview(msg proto.Message) string {
	if msg == nil {
		return ""
	}
	js, err := protojson.Marshal(msg)
	if err != nil {
		return fmt.Sprintf("marshal error: %v", err)
	}
	if len(js) > txPayloadPreviewCap {
		return string(js[:txPayloadPreviewCap]) + "...(truncated)"
	}
	return string(js)
}
