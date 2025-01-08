# Validator Setup

Create a directory on your node for persistent data and configuration.

```bash
ssh user@node.ip.addr.ess
mkdir ~/audiusd
```

## Node Configuration

Create an `override.env` file to configure your node.

> The values below are examples. Replace them with your own. **Never share your private keys.**

#### Content Node
```bash
# ~/audiusd/override.env
creatorNodeEndpoint=https://cn1.operator.xyz
delegateOwnerWallet=0x07bC80Cc29bb15a5CA3D9DB9D80AcA25eB967aFc
delegatePrivateKey=2ef5a28ab4c39199085eb4707d292c980fef3dcc9dc854ba8736a545c11e81c4
spOwnerWallet=0x92d3ff660158Ec716f1bA28Bc65a7a0744E26A98
```

#### Discovery Node
```bash
# ~/audiusd/override.env
audius_discprov_url=https://dn1.operator.xyz
audius_delegate_owner_wallet=0x07bC80Cc29bb15a5CA3D9DB9D80AcA25eB967aFc
audius_delegate_private_key=2ef5a28ab4c39199085eb4707d292c980fef3dcc9dc854ba8736a545c11e81c4
```

## Run

```bash
docker run -d \
  --restart unless-stopped \
  --env-file ~/audiusd/override.env \
  -v ~/audiusd/data:/data \
  -p 80:80 \
  -p 443:443 \
  -p 26656:26656 \
  audius/audiusd:current
```

If you are migrating from an **existing registered node**, you will want to pay attention to the persistent volume mount point. Which will likely look something more like this.

```bash
  -v /var/k8s:/data
```

## Network Configuration

### Required Ports

Your node requires three open ports to function properly:

| Port  | Purpose | Required For           |
|-------|---------|------------------------|
| 80    | HTTP    | TLS certificate setup  |
| 443   | HTTPS   | Secure API access      |
| 26656 | P2P     | Network consensus      |

### TLS Certificate Setup

By default, nodes use LetsEncrypt for TLS certification. On first boot:
1. The certification process takes ~60 seconds
2. Your node must be publicly accessible
3. Both ports 80 and 443 must be open

For more information, see [SSL Configuration Options](#ssl-configuration-options).

### Node Participation Levels

The level of node participation depends on port accessibility:

**Validator Participation**
- Requires port 26656 to be open
- Enables: block proposals, consensus voting, and transaction relay
- Full network participant
- Requires [registration](https://docs.audius.org/node-operator/setup/registration/)

**Peer Participation**
- Without port 26656
- Limited to: downloading blocks and RPC queries
- Cannot participate in consensus
- Does not require registration

### SSL Configuration Options

**Option 1: Default Setup**
- Uses LetsEncrypt automatic certification
- No additional configuration needed

**Option 2: Cloudflare Proxy**

```bash
# LetsEncrypt challenges cannot pass under Cloudflare's strict proxy mode
AUDIUSD_TLS_SELF_SIGNED=true
```

**Option 3: Custom SSL Setup**
```bash
# Disable built-in TLS if using your own SSL termination
AUDIUSD_TLS_DISABLED=true

# Optional: Configure custom ports
AUDIUSD_HTTP_PORT=80     # Default
AUDIUSD_HTTPS_PORT=443   # Default
```

## Health Check

```bash
curl https://your-node.example.com/health-check | jq .

# values are examples
{
  "core": {
    "chainId": "audius-mainnet",
    "cometAddress": "1A2B3C4D5E6F7G8H9I0J1K2L3M4N5O6P7Q8R",
    "errors": [],
    "ethAddress": "0x1234567890123456789012345678901234567890",
    "healthy": true,
    "totalBlocks": 123456,
    "totalTransactions": 789012
  },
  "git": "abcdef0123456789abcdef0123456789abcdef01",
  "hostname": "your-node.example.com", 
  "storage": {
    # discovery nodes will have storage disabled
    "enabled": false,
    # content nodes will have storage enabled + additional fields
    "enabled": true,
    "...": "..."
  },
  "timestamp": "2024-01-01T12:00:00.000000000Z",
  "uptime": "24h0m0.000000000s"
}
```
