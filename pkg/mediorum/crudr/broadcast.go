package crudr

// pushes serialized crudr Op to peer via POST request
// if the peerClient queue is full, it will drop the message
// it is ok if message is dropped or POST fails
// because the sweeper will consume op.
func (c *Crudr) broadcast(payload []byte) {
	c.mu.Lock()
	peers := make([]*PeerClient, len(c.peerClients))
	copy(peers, c.peerClients)
	c.mu.Unlock()
	for _, p := range peers {
		p.Send(payload)
	}
}
