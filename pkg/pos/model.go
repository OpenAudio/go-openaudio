// These data structures allow core and mediorum to communicate for Proof of Storage challenges.
package pos

type PoSResponse struct {
	CID      string
	Replicas []string
	Proof    []byte
}

type PoSRequest struct {
	Hash     []byte
	Height   int64
	Response chan PoSResponse
}
