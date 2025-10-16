# Validator Setup

This guide provides migration instructions for existing nodes that are currently using `audiusd`.

Create an `override.env` file.

```bash
# /home/ubuntu/override.env

nodeEndpoint=https://cn1.operator.xyz
delegateOwnerWallet=0x01234567890abcdef01234567890abcdef012345
delegatePrivateKey=01234567890abcdef01234567890abcdef01234567890abcdef01234567890ab
spOwnerWallet=0x01234567890abcdef01234567890abcdef012345

# uncomment if using s3 for blob storage
#AUDIUS_STORAGE_DRIVER_URL=s3://my-s3-bucket
#AWS_REGION=us-west-2
#AWS_ACCESS_KEY_ID=XXXXXXXXXXXXXXXXXXXX
#AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX

# uncomment if using gcs for blob storage
#   you will need an additional mount for the credentials
#   i.e. `-v google-application-credentials.json:/tmp/google-application-credentials.json`
#AUDIUS_STORAGE_DRIVER_URL=gs://my-gcs-bucket
#GOOGLE_APPLICATION_CREDENTIALS=/tmp/google-application-credentials.json

# uncomment if using cloudflare proxy
#OPENAUDIO_TLS_SELF_SIGNED=true
```

Run your node.

```bash
docker run -d \
  --name openaudio-cn1.operator.xyz \
  --restart unless-stopped \
  --env-file /home/ubuntu/override.env \
  -v /var/k8s/creator-node-db-15:/data/creator-node-db-15 \
  -v /var/k8s/bolt:/data/bolt \
  -v /var/k8s/mediorum:/tmp/mediorum \
  -p 80:80 \
  -p 443:443 \
  -p 26656:26656 \
  audius/openaudio:v1.0.0
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
cat << 'EOF' > /home/ubuntu/openaudio-update.sh
#!/bin/bash

OPENAUDIO_HOSTNAME="PLACEHOLDER_HOSTNAME"
OVERRIDE_ENV_PATH="PLACEHOLDER_ENV_PATH"
OPENAUDIO_VERSION="${OPENAUDIO_VERSION:-v1.0.0}"

FORCE_UPDATE=false
if [[ "$1" == "--force" ]]; then
    FORCE_UPDATE=true
fi

PULL_STATUS=$(docker pull audius/openaudio:${OPENAUDIO_VERSION} | tee /dev/stderr)
if $FORCE_UPDATE || ! echo "$PULL_STATUS" | grep -q 'Status: Image is up to date'; then
    echo "New version found or force update requested, updating container..."
    docker stop openaudio-${OPENAUDIO_HOSTNAME}
    docker rm openaudio-${OPENAUDIO_HOSTNAME}
    docker run -d \
        --name openaudio-${OPENAUDIO_HOSTNAME} \
        --restart unless-stopped \
        --env-file ${OVERRIDE_ENV_PATH} \
        -v /var/k8s:/data \
        -p 80:80 \
        -p 443:443 \
        -p 26656:26656 \
        audius/openaudio:${OPENAUDIO_VERSION}
    echo "Update complete"
else
    echo "Already running latest version"
fi
EOF
```

Copy the below command and replace `node.operator.xyz` with your actual node hostname.

```bash
sed -i "s/PLACEHOLDER_HOSTNAME/node.operator.xyz/" /home/ubuntu/openaudio-update.sh
```

Copy the below command and replace `/home/ubuntu/override.env` with the full path to your node's override.env file that you created earlier.

```bash
sed -i "s|PLACEHOLDER_ENV_PATH|/home/ubuntu/override.env|" /home/ubuntu/openaudio-update.sh
```

Make the script executable and install it system-wide.

```bash
chmod +x /home/ubuntu/openaudio-update.sh
sudo mv /home/ubuntu/openaudio-update.sh /usr/local/bin/openaudio-update
```

Add a cron to run the update script on a random minute of each hour (staggers updates across network):

```bash
(crontab -l | grep -v "# openaudio auto-update"; echo "$(shuf -i 0-59 -n 1) * * * * /usr/local/bin/openaudio-update >> /home/ubuntu/openaudio-update.log 2>&1 # openaudio auto-update") | crontab -
```

Check the cron job was added successfully.

```bash
crontab -l
```

The output should look something like...
```bash
54 * * * * /usr/local/bin/openaudio-update >> /home/ubuntu/openaudio-update.log 2>&1 # openaudio auto-update
```

**Done!**

Additionally, you can run the update script manually:

```bash
openaudio-update [--force]
```

Check the auto-update logs:

```bash
tail -f /home/ubuntu/openaudio-update.log
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
OPENAUDIO_TLS_SELF_SIGNED=true
```

**Option 3: Custom SSL Setup**
```bash
# Disable built-in TLS if using your own SSL termination
OPENAUDIO_TLS_DISABLED=true

# Optional: Configure custom ports
OPENAUDIO_HTTP_PORT=80     # Default
OPENAUDIO_HTTPS_PORT=443   # Default
```

### Blob Storage

Validators support cloud blob storage such as s3, gcs, and azure as an efficient, scalable, and reliable alternative to local disk storage.

If using s3-compatible blob storage with a cloud provider other than AWS, please note that the `AWS_REGION`, `AWS_ACCESS_KEY_ID`, and `AWS_SECRET_ACCESS_KEY` environment variables are still required to be set in your config and will function similarly using your custom provider's application access key and region.

In addition, please ensure the `AUDIUS_STORAGE_DRIVER_URL` environment variable is set using the following format:

```bash
AUDIUS_STORAGE_DRIVER_URL=s3://<bucket-name>?endpoint=https://<provider-hostname>
```

### Node Participation Levels

**Validators**
- Participates in consensus, block proposals, and transaction relay
- Requires peering over port `26656`
- Requires [registration](https://docs.openaudio.org/tutorials/run-a-node#node-registration)

**Peers**
- Does not participate in consensus, able to download blocks and serve RPC queries
- Does not require registration
- Does not require peering over port `26656`
