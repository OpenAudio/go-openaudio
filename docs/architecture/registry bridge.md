
# overview
Audius validators register themselves on ethereum mainnet. This is the source of truth for who has put down enough stake to receive rewards by engaging in block production and serving files. 

The registry bridge is a goroutine that maintains an accurate picture on the audius L1 of those that have registered over on ethereum.

## registration
Core has it's own processes and purpose for registration. When the term "registration" is used for the remainder of this doc it is within the context of core, not ethereum. Ethereum registration will be made explicit.

The onus is on the node operator to register themselves with core. The registry bridge process provides this as a convenience but for alternate implementations of audiusd, not strictly required. The registry bridge on node startup will check ethereum mainnet for audius validators. Once it has received the current list of validators it will check this list against it's running key. Should there be a match and the running node is not already present in the chain state, the node will submit a `ValidatorRegistration` transaction to the audius L1. This will go through the consensus process and should the other nodes recognize this node exists on ethereum too, the node is then persisted in the chain state as a new validator. It is also included in the cometbft validators list so it can participate in block production as well as validation. 

## deregistration
Deregistration has not been fully implemented yet but it can still happen under certain circumstances. The deregistration process is a code-wise reversal of the registration process mentioned above. The validator is removed from the cometbft validators list as well as the chain state.

A node can be deregistered when cometbft provides evidence of misbehavior related to a validator's pubkey. When misbehavior evidence is presented the onus is on the next block proposer to introduce a `ValidatorDeregister` transaction within the block they create in the `PrepareBlock` abci step.

