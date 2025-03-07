# Audius GraphQL API

## Blocks

### Get Block by Height
```graphql
query {
  getBlock(height: 100) {
    height
    chainId
    hash
    proposer
    transactions {
      index
      hash
    }
  }
}
```

### Get Latest Block
```graphql
query {
  getLatestBlock {
    height
    chainId
    hash
    proposer
    transactions {
      index
      hash
    }
  }
}
```

### Get Latest Blocks
```graphql
query {
  getLatestBlocks(limit: 5) {
    height
    chainId
    hash
    proposer
    transactions {
      index
      hash
    }
  }
}
```

## Transactions

### Get Transaction by Hash
```graphql
query {
  getTransaction(hash: "0x123...") {
    index
    hash
    blockHeight
    data
    type
  }
}
```

### Get Latest Transactions
```graphql
query {
  getLatestTransactions(limit: 10) {
    index
    hash
    blockHeight
    data
    type
  }
}
```

### Get Transaction Stats
```graphql
query {
  getTransactionStats(hours: 24) {
    hour
    txCount
    txType
  }
}
```

## Decoded Transactions

### Get Decoded Transaction by Hash
```graphql
query {
  getDecodedTransaction(hash: "0x123...") {
    blockHeight
    txIndex
    txHash
    txType
    txData
    createdAt
  }
}
```

### Get Latest Decoded Transactions
```graphql
query {
  getLatestDecodedTransactions(limit: 10) {
    blockHeight
    txIndex
    txHash
    txType
    txData
    createdAt
  }
}
```

### Get Decoded Transactions by Type
```graphql
query {
  getDecodedTransactionsByType(txType: "Plays", limit: 10) {
    blockHeight
    txIndex
    txHash
    txType
    txData
    createdAt
  }
}
```

### Get Decoded Transactions by Block Height
```graphql
query {
  getDecodedTransactionsByBlock(height: 100) {
    blockHeight
    txIndex
    txHash
    txType
    txData
    createdAt
  }
}
```

### Get Decoded Plays
```graphql
query {
  getDecodedPlays(limit: 10) {
    txHash
    userId
    trackId
    playedAt
    signature
    city
    region
    country
    createdAt
  }
}
```

### Get Decoded Plays by User
```graphql
query {
  getDecodedPlaysByUser(userId: "123", limit: 10) {
    txHash
    userId
    trackId
    playedAt
    signature
    city
    region
    country
    createdAt
  }
}
```

### Get Decoded Plays by Track
```graphql
query {
  getDecodedPlaysByTrack(trackId: "456", limit: 10) {
    txHash
    userId
    trackId
    playedAt
    signature
    city
    region
    country
    createdAt
  }
}
```

### Get Decoded Plays by Time Range
```graphql
query {
  getDecodedPlaysByTimeRange(
    startTime: "2024-01-01T00:00:00Z"
    endTime: "2024-01-02T00:00:00Z"
    limit: 10
  ) {
    txHash
    userId
    trackId
    playedAt
    signature
    city
    region
    country
    createdAt
  }
}
```

### Get Decoded Plays by Location
```graphql
query {
  getDecodedPlaysByLocation(
    location: {
      city: "San Francisco"      # Optional
      region: "California"       # Optional
      country: "United States"   # Optional
    }
    limit: 10
  ) {
    txHash
    userId
    trackId
    playedAt
    signature
    city
    region
    country
    createdAt
  }
}
```

### Get Available Cities
```graphql
query {
  # Get cities with custom limit
  getAvailableCities(limit: 1000) {
    city
    region
    country
    playCount
  }

  # Get cities with filters
  getAvailableCitiesWithFilters: getAvailableCities(
    filter: {
      country: "United States"  # Optional: Filter cities by country
      region: "California"      # Optional: Filter cities by region
    },
    limit: 1000                 # Optional: Increase limit to get more results (no unlimited option)
  ) {
    city
    region
    country
    playCount
  }
}
```

### Get Available Regions
```graphql
query {
  # Get regions with custom limit
  getAvailableRegions(limit: 1000) {
    region
    country
    playCount
  }

  # Get regions with filters
  getAvailableRegionsWithFilters: getAvailableRegions(
    filter: {
      country: "United States"  # Optional: Filter regions by country
    },
    limit: 1000                 # Optional: Increase limit to get more results (no unlimited option)
  ) {
    region
    country
    playCount
  }
}
```

### Get Available Countries
```graphql
query {
  # Get countries with custom limit
  getAvailableCountries(limit: 1000) {
    country
    playCount
  }
}
```

## Analytics

### Get Protocol Analytics
```graphql
query {
  getAnalytics {
    totalBlocks
    totalTransactions
    totalPlays
    totalValidators
    totalManageEntities
  }
}
```

## Nodes

### Get All Nodes
```graphql
query {
  getAllNodes {
    address
    endpoint
    ethAddress
    cometAddress
    cometPubKey
    nodeType
    spId
  }
}
```

### Get Node by Address
```graphql
query {
  getNode(address: "0x123...") {
    address
    endpoint
    ethAddress
    cometAddress
    cometPubKey
    nodeType
    spId
  }
}
```

### Get Nodes by Type
```graphql
query {
  getNodesByType(nodeType: "validator") {
    address
    endpoint
    ethAddress
    cometAddress
    cometPubKey
    nodeType
    spId
  }
}
```

## Node Uptime

### Get Node Uptime
```graphql
query {
  getNodeUptime(address: "0x123...", rollupId: 1) {
    address
    endpoint
    isValidator
    activeReport {
      rollupId
      txHash
      blockStart
      blockEnd
      blocksProposed
      quota
      posChallengesFailed
      posChallengesTotal
      timestamp
    }
    reportHistory {
      rollupId
      txHash
      blockStart
      blockEnd
      blocksProposed
      quota
      posChallengesFailed
      posChallengesTotal
      timestamp
    }
  }
}
```

### Get All Validator Uptimes
```graphql
query {
  getAllValidatorUptimes(rollupId: 1) {
    address
    endpoint
    isValidator
    activeReport {
      rollupId
      txHash
      blockStart
      blockEnd
      blocksProposed
      quota
      posChallengesFailed
      posChallengesTotal
      timestamp
    }
  }
}
```

## SLA Rollups

### Get Latest SLA Rollup
```graphql
query {
  getLatestSLARollup {
    id
    txHash
    blockStart
    blockEnd
    timestamp
    nodeReports {
      address
      blocksProposed
      quota
      posChallengesFailed
      posChallengesTotal
    }
  }
}
```

### Get SLA Rollup by ID
```graphql
query {
  getSLARollup(id: 1) {
    id
    txHash
    blockStart
    blockEnd
    timestamp
    nodeReports {
      address
      blocksProposed
      quota
      posChallengesFailed
      posChallengesTotal
    }
  }
}
```

## Storage Proofs

### Get Storage Proofs by Range
```graphql
query {
  getStorageProofs(
    startBlock: 100
    endBlock: 200
    address: "0x123..."
  ) {
    blockHeight
    proverAddress
    cid
    status
    proofSignature
  }
}
```

### Get Storage Proofs by Block
```graphql
query {
  getStorageProofsByBlock(height: 100) {
    blockHeight
    proverAddress
    cid
    status
    proofSignature
  }
}
```

## Combined Queries

You can combine multiple queries into a single request. Here are some examples:

### Get Latest Block and Analytics
```graphql
query {
  latestBlock: getLatestBlock {
    height
    chainId
    hash
    proposer
  }
  analytics: getAnalytics {
    totalBlocks
    totalTransactions
    totalValidators
  }
}
```

### Get Node Info with Latest SLA Report
```graphql
query {
  node: getNode(address: "0x123...") {
    address
    nodeType
    endpoint
  }
  uptime: getNodeUptime(address: "0x123...") {
    isValidator
    activeReport {
      blocksProposed
      quota
    }
  }
}
```

### Get Network Overview
```graphql
query {
  analytics: getAnalytics {
    totalBlocks
    totalValidators
  }
  latestBlocks: getLatestBlocks(limit: 3) {
    height
    proposer
  }
  latestTxs: getLatestTransactions(limit: 3) {
    hash
    type
  }
  nodes: getAllNodes {
    address
    nodeType
  }
}
```

## Notes

- All queries that accept a `limit` parameter default to 10 if not specified
- For timestamp fields, the format is ISO 8601
- Node types include: "validator", "content-node", "discovery-node"
- Storage proof status can be: "pending", "valid", "invalid"
- Transaction types include: "TrackPlays", "ManageEntity" and others
- Some fields like `quota`, `posChallengesFailed`, and `posChallengesTotal` are planned for future implementation 