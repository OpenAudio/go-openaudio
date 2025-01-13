# Validator Setup

This guide provides migration instructions for existing nodes that are currently using `audius-docker-compose` or `audius-ctl`. If you're setting up a new node, please refer to the [New Nodes](#new-nodes) section.

### Existing Content Nodes

```bash
cat << 'EOF' > /home/ubuntu/override.env
creatorNodeEndpoint="https://cn1.operator.xyz"
delegateOwnerWallet="0x01234567890abcdef01234567890abcdef012345"
delegatePrivateKey="01234567890abcdef01234567890abcdef01234567890abcdef01234567890ab"
spOwnerWallet="0x01234567890abcdef01234567890abcdef012345"

# uncomment if using s3 for blob storage
#AUDIUS_STORAGE_DRIVER_URL="s3://my-s3-bucket"
#AWS_REGION="us-west-2"
#AWS_ACCESS_KEY_ID="XXXXXXXXXXXXXXXXXXXX"
#AWS_SECRET_ACCESS_KEY="XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"

# uncomment if using gcs for blob storage
#   you will need an additional mount for the credentials
#   i.e. `-v google-application-credentials.json:/tmp/google-application-credentials.json`
#AUDIUS_STORAGE_DRIVER_URL="gs://my-gcs-bucket"
#GOOGLE_APPLICATION_CREDENTIALS="/tmp/google-application-credentials.json"

# uncomment if using cloudflare proxy
#AUDIUSD_TLS_SELF_SIGNED="true"
EOF

docker run -d \
  --name audiusd-cn1.operator.xyz \
  --restart unless-stopped \
  -v /var/k8s:/data \
  -v /home/ubuntu/override.env:/env/override.env \
  -p 80:80 \
  -p 443:443 \
  -p 26656:26656 \
  audius/audiusd:current
```

### Existing Discovery Nodes

```bash
cat << 'EOF' > /home/ubuntu/override.env
audius_discprov_url="https://dn1.operator.xyz"
audius_delegate_owner_wallet="0x01234567890abcdef01234567890abcdef012345"
audius_delegate_private_key="01234567890abcdef01234567890abcdef01234567890abcdef01234567890ab"

# uncomment if using cloudflare proxy
#AUDIUSD_TLS_SELF_SIGNED="true"
EOF

docker run -d \
  --name audiusd-dn1.operator.xyz \
  --restart unless-stopped \
  -v /var/k8s:/data \
  -v /home/ubuntu/override.env:/env/override.env \
  -p 80:80 \
  -p 443:443 \
  -p 26656:26656 \
  audius/audiusd:current
```

## New Nodes

Create a directory on your node for persistent data and configuration.

```bash
ssh user@node.ip.addr.ess
mkdir ~/audiusd
```

**TODO** test config setup from `/home/ubuntu/audiusd/config.yaml`

```bash
# TODO: untested
cat << 'EOF' > /home/ubuntu/audiusd/config.yaml
host: "cn1.operator.xyz"
wallet: "0x01234567890abcdef01234567890abcdef012345"
privateKey: "01234567890abcdef01234567890abcdef01234567890abcdef01234567890ab"
rewardsWallet: "0x01234567890abcdef01234567890abcdef012345"

# uncomment if using s3 for blob storage
#storage:
  #storageUrl: "s3://cn1-operator-xyz"
  #awsAccessKeyId: "XXXXXXXXXXXXXXXXXXXX"
  #awsSecretAccessKey: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
  #awsRegion: "us-west-2"

# uncomment if using gcs for blob storage
#   you will need an additional mount for the credentials
#   i.e. `-v google-application-credentials.json:/tmp/google-application-credentials.json`
#storage:
  #storageUrl: "gs://cn1-operator-xyz"
  #googleApplicationCredentials: "/tmp/google-application-credentials.json"
EOF

docker run -d \
  --name audiusd-cn1.operator.xyz \
  --restart unless-stopped \
  -v /home/ubuntu/audiusd/config.yaml:/env/config.yaml \
  -v /home/ubuntu/audiusd/data:/data \
  -p 80:80 \
  -p 443:443 \
  -p 26656:26656 \
  audius/audiusd:current
```

## Health Check

```bash
curl https://node.operator.xyz/health-check | jq .

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
    "service": "content-node",
    "...": "..."
  },
  "timestamp": "2024-01-01T12:00:00.000000000Z",
  "uptime": "24h0m0.000000000s"
}
```

## Auto-Update

**TODO:** untested

Use a simple [cron](https://en.wikipedia.org/wiki/Cron) job to automatically update your node to the latest [stable audiusd release](https://github.com/AudiusProject/audiusd/releases).

```bash
crontab -e
...

$(shuf -i 0-59 -n 1) * * * * if ! docker pull audius/audiusd:current | grep -q "Status: Image is up to date"; then \
  docker stop audiusd-cn1.operator.xyz && \
  docker rm audiusd-cn1.operator.xyz && \
  docker run -d \
    --name audiusd-cn1.operator.xyz \
    --restart unless-stopped \
    -v /home/ubuntu/audiusd/config.yaml:/env/config.yaml \
    -v /home/ubuntu/audiusd/data:/data \
    -p 80:80 \
    -p 443:443 \
    -p 26656:26656 \
    audius/audiusd:current; \
fi
```

---

## Additional Configuration

### Ports

Your node requires three open ports for full participation:

- 80
- 443
- 26656

### TLS

**Option 1: Default Setup**

On first boot, nodes will attempt to use LetsEncrypt for TLS certification.

1. The certification process takes ~60 seconds
2. Your node must be publicly accessible via its hostname
3. Both ports 80 and 443 must be open

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

### Node Participation Levels

**Validators**
- Participates in consensus, block proposals, and transaction relay
- Requires peering over port `26656`
- Requires [registration](https://docs.audius.org/node-operator/setup/registration/)

**Peers**
- Does not participate in consensus, able to download blocks and serve RPC queries
- Does not require registration
- Does not require peering over port `26656`
