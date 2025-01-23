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
# TODO: add example config.yaml
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
  "hostname": "node.operator.xyz", 
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

## Autonomous Update

To create the update script, copy and paste this.

```bash
cat << 'EOF' > /home/ubuntu/audiusd-update.sh
#!/bin/bash

AUDIUSD_HOSTNAME="PLACEHOLDER_HOSTNAME"
OVERRIDE_ENV_PATH="PLACEHOLDER_ENV_PATH"

FORCE_UPDATE=false
if [[ "$1" == "--force" ]]; then
    FORCE_UPDATE=true
fi

PULL_STATUS=$(docker pull audius/audiusd:current | tee /dev/stderr)
if $FORCE_UPDATE || ! echo "$PULL_STATUS" | grep -q 'Status: Image is up to date'; then
    echo "New version found or force update requested, updating container..."
    docker stop audiusd-${AUDIUSD_HOSTNAME}
    docker rm audiusd-${AUDIUSD_HOSTNAME}
    docker run -d \
        --name audiusd-${AUDIUSD_HOSTNAME} \
        --restart unless-stopped \
        -v ${OVERRIDE_ENV_PATH}:/env/override.env \
        -v /var/k8s:/data \
        -p 80:80 \
        -p 443:443 \
        -p 26656:26656 \
        audius/audiusd:current
    echo "Update complete"
else
    echo "Already running latest version"
fi
EOF
```

Copy the below command and replace `node.operator.xyz` with your actual node hostname.

```bash
sed -i "s/PLACEHOLDER_HOSTNAME/node.operator.xyz/" /home/ubuntu/audiusd-update.sh
```

Copy the below command and replace `/home/ubuntu/override.env` with the full path to your node's override.env file that you created earlier.

```bash
sed -i "s|PLACEHOLDER_ENV_PATH|/home/ubuntu/override.env|" /home/ubuntu/audiusd-update.sh
```

Make the script executable and install it system-wide.

```bash
chmod +x /home/ubuntu/audiusd-update.sh
sudo mv /home/ubuntu/audiusd-update.sh /usr/local/bin/audiusd-update
```

Add a cron to run the update script on a random minute of each hour (staggers updates across network):

```bash
(crontab -l | grep -v "# audiusd auto-update"; echo "$(shuf -i 0-59 -n 1) * * * * /usr/local/bin/audiusd-update >> /home/ubuntu/audiusd-update.log 2>&1 # audiusd auto-update") | crontab -
```

Check the cron job was added successfully.

```bash
crontab -l
```

The output should look something like...
```bash
54 * * * * /usr/local/bin/audiusd-update >> /home/ubuntu/audiusd-update.log 2>&1 # audiusd update
```

**Done!**

Additionally, you can run the update script manually:

```bash
audiusd-update [--force]
```

Check the auto-update logs:

```bash
tail -f /home/ubuntu/audiusd-update.log
```

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
