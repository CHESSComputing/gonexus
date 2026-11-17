# Serving HTTPS

By default the server speaks plain HTTP. Give it a certificate and a private
key and it speaks HTTPS instead; nothing else changes (the API, the web UI and
the Python client work the same way).

```yaml
tls_cert_file: /etc/gonexus/server.crt   # PEM; may contain the intermediate chain
tls_key_file:  /etc/gonexus/server.key   # PEM
# tls_min_version: "1.2"                 # or "1.3"
```

| config key | environment variable | default | meaning |
|---|---|---|---|
| `tls_cert_file` | `NEXUS_TLS_CERT_FILE` | none | certificate (and chain) in PEM format |
| `tls_key_file` | `NEXUS_TLS_KEY_FILE` | none | private key in PEM format |
| `tls_min_version` | `NEXUS_TLS_MIN_VERSION` | `1.2` | lowest TLS version accepted: `1.2` or `1.3` |

Both files must be given together; giving only one is a configuration error.
With neither, the server uses plain HTTP exactly as before. The config file
overrides environment variables, as for all other settings, and relative paths
are relative to the directory the server is started from.

The startup log shows which mode is active and which certificate is served:

```
TLS: serving certificate subject="CN=localhost" dns=[localhost] ips=[127.0.0.1] not_after=2027-11-17T01:15:16Z
nexus-s3-server listening on :8081 (https, source=s3 bucket=...)
```

> These settings secure connections **to this server**. They are unrelated to
> `S3_TLS_CA_FILE` / `S3_TLS_INSECURE_SKIP_VERIFY`, which concern the
> connection from the server **to an S3 endpoint**.

## What is checked

* At startup the server loads the pair and refuses to start if a file is
  missing, is not valid PEM, or the key does not match the certificate; you
  get one clear error instead of failing connections later.
* It warns if the key file is readable by other users (use `chmod 600`, or
  `640` with a dedicated group), and if the certificate has expired or expires
  within 30 days.

## Renewing certificates (no restart needed)

When a TLS handshake arrives, the server checks (at most every 10 seconds)
whether the certificate or key file changed on disk, and if so loads the new
pair, so a renewal by certbot or another tool takes effect within seconds.
Symlinks are followed, so Let's Encrypt's `live/` layout works:

```yaml
tls_cert_file: /etc/letsencrypt/live/example.org/fullchain.pem
tls_key_file:  /etc/letsencrypt/live/example.org/privkey.pem
```

If a replacement cannot be loaded (for example a half-written file), the
server logs the error, keeps serving the previous certificate, and tries again
only when a file changes again. Make sure the server's user can read the key
(and the directories above it).

## Trying it with a self-signed certificate

```bash
openssl req -x509 -newkey rsa:4096 -nodes -sha256 -days 365 \
  -keyout server.key -out server.crt -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,DNS:myhost.example.org,IP:127.0.0.1"
chmod 600 server.key

NEXUS_TLS_CERT_FILE=server.crt NEXUS_TLS_KEY_FILE=server.key ./bin/nexus-s3-server -config config.yaml

curl --cacert server.crt https://localhost:8081/healthz     # verified
curl -k https://localhost:8081/healthz                       # skip verification (testing only)
```

Browsers show a warning for a self-signed certificate until you trust it; use
a certificate from your organization's CA or Let's Encrypt for real use. The
names a client connects with (host name or IP address) must appear in the
certificate's subject alternative names.

## Clients

* **Web UI:** open `https://host:8081/`. It uses relative URLs, so nothing
  needs configuring. A page served over HTTPS cannot call a plain-HTTP API
  given with `?api=http://...` (the browser blocks mixed content).
* **Python client:** `from_uri("https://host:8081")`. For a private or
  self-signed CA pass `verify="/path/ca.pem"` (or set `GONEXUS_CA_BUNDLE`);
  see `client/README.md`.
* **curl:** `--cacert ca.pem`, or `-k` for testing.

## Not included

* No redirect from a plain-HTTP port to HTTPS, and no HSTS header (HSTS makes
  browsers remember the site, which is unhelpful with self-signed
  certificates; add it in a reverse proxy if you want it).
* No client-certificate (mutual TLS) authentication.
* If you prefer to terminate TLS in a reverse proxy (nginx, Traefik, ...), leave
  these settings empty and keep the server on plain HTTP behind it.
