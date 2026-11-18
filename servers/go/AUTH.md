# Access control for `/view` (`AUTH.md`)

`/view` opens a NeXus file for a caller. Since it can read files from the
server's filesystem, it requires a **token** and takes its input in a **POST
body**, not in the URL (URLs end up in logs, browser history and referrers).

```bash
curl -X POST http://host:8081/view \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"file": "/nfs/chess/id4b/scan7.nxs"}'
```

The token can also be sent as `"token"` in the body; if both are present the
`Authorization` header wins. The server decodes the body into a *payload*
struct, calls its `Validate` function, and only if that returns `nil` goes on: the server answers **303 See Other** to
`/ui/#key=<file>`, so a browser lands on the web UI showing the file's tree and
data (curl shows the redirect; add `-L` to follow it). `/view` itself returns no
file content.

## `/viewform`: the same with a web form

For services that find a form easier than JSON, `POST /viewform` does exactly
what `/view` does (same payload struct, `Validate`, statuses and redirect), but
reads the payload from form fields named like the payload's JSON names. The
token is a form field, `Authorization` headers are ignored, and fields in the
URL query are ignored too, so keep the token in the body:

```bash
curl -X POST http://host:8081/viewform -d file=/nfs/chess/id4b/scan7.nxs -d token="$TOKEN"
curl -X POST http://host:8081/viewform -F file=@- ...   # multipart/form-data works too
# foxden payload: add  -d did=<did>
```

Fields are single strings, so a custom payload used with `/viewform` should only
have string fields.

| status | meaning |
|---|---|
| 303 | token and payload valid, redirect to the UI (`Location: /ui/#key=...`) |
| 400 | body is not valid JSON, `file` (or, for `foxden`, `did`) is missing; or the store refuses the path |
| 401 | no token, malformed, bad signature or expired (response has `WWW-Authenticate: Bearer`) |
| 403 | token lacks the `read` scope, or FOXDEN denies the dataset, or a custom `Validate` says no |
| 405 | any method other than POST (`Allow: POST`) |
| 502 | FOXDEN could not be asked, or answered unexpectedly (access is denied) |

## Tokens

Tokens are JWTs:

```json
{ "iss": "Some Authz server", "sub": "…", "aud": ["…"], "exp": 1795012320, "iat": 1795008720,
  "custom_claims": { "user": "some-user", "scope": "read", "kind": "client_credentials",
                     "application": "FOXDEN frontend", "roles": null,
                     "btrs": ["test-demo", "test-987-b"], "groups": ["group-a"],
                     "scopes": ["read", "delete", "write"] } }
```

A token is accepted when it is well formed, its signature is valid (see below),
`exp` is in the future (and `nbf`, if present, in the past), and the user has
the **`read`** scope, meaning `custom_claims.scope == "read"` or `"read"` is in
`custom_claims.scopes`.

### Signature verification: configure it

| setting | env | |
|---|---|---|
| `token_secret` | `NEXUS_TOKEN_SECRET` | shared secret, tokens signed with HS256 |
| `token_public_key_file` | `NEXUS_TOKEN_PUBLIC_KEY_FILE` | PEM public key or certificate, tokens signed with RS256 |

The algorithm is decided by which key you configure, never by the token's own
header, and `alg: none` is always refused. **If neither is set, signatures are
not checked**: only `exp` and the scopes are, so anyone can write a token that
passes. The server logs a warning at start-up in that case. Only set neither
when something in front of the server (or FOXDEN, for the `foxden` payload)
already guarantees the token is genuine.

## Choosing the payload struct

```yaml
view_payload: default          # NEXUS_VIEW_PAYLOAD: default | foxden | <your own name>
```

| `view_payload` | body | `Validate` does |
|---|---|---|
| `default` | `{"file", "token"}` (`ViewPayload`) | checks the token and the `read` scope |
| `foxden` | `{"file", "token", "did"}` (`FoxdenViewPayload`) | the same, then asks FOXDEN whether the user may access `did` |

For `foxden` also set:

```yaml
foxden_url: https://foxden.example.org/authz/access    # NEXUS_FOXDEN_URL (required)
foxden_timeout: 10                                     # NEXUS_FOXDEN_TIMEOUT, seconds
```

The server takes `user`, `scope`, `roles` and `btrs` from the token and sends
FOXDEN a `POST` to `foxden_url` with `Authorization: Bearer <token>` and

```json
{"user": "some-user", "scope": "read", "roles": [], "btrs": ["test-demo"],
 "did": "<did>", "file": "<file>"}
```

Any 2xx answer grants access; 401 means FOXDEN rejects the token; 403 or 404
means the user may not access that dataset; anything else, or no answer, denies
access (502). **This request/response contract is an assumption**: it lives in
one function, `foxdenCheck` in `auth.go`, so adapt the URL, body and status
mapping there to what your FOXDEN endpoint really speaks.

## Writing your own payload

A payload is a struct that implements `UserPayload` (`auth.go`):

```go
type UserPayload interface {
	GetFile() string                                    // the file to open
	GetToken() string
	SetToken(token string)                              // used for the Authorization header
	Validate(ctx context.Context, env *AuthEnv) error   // nil = allowed
}
```

`Validate` returns `nil` to let the request proceed. To choose the HTTP status
return an `*apiError` (`unauthorized(...)` 401, `forbidden(...)` 403,
`badRequest(...)` 400, `internal(...)` 500); any other error becomes 403.

`env` offers `env.ParseToken(token)` (signature, expiration → `*TokenClaims`),
`env.authorizeRead(token)` (the same plus the `read` scope), `env.HTTP` (a
client with a timeout) and `env.FoxdenURL`. Never put the token in an error
message or a log line.

**Example**: allow only members of a group named in the request. Put it in a new
file of package `main`, for example `payload_group.go`:

```go
package main

import "context"

type GroupViewPayload struct {
	File  string `json:"file"`
	Token string `json:"token"`
	Group string `json:"group"`
}

func (p *GroupViewPayload) GetFile() string     { return p.File }
func (p *GroupViewPayload) GetToken() string    { return p.Token }
func (p *GroupViewPayload) SetToken(tok string) { p.Token = tok }

func (p *GroupViewPayload) Validate(ctx context.Context, env *AuthEnv) error {
	claims, err := env.authorizeRead(p.Token) // valid token + read scope
	if err != nil {
		return err
	}
	for _, g := range claims.Custom.Groups {
		if g == p.Group {
			return nil
		}
	}
	return forbidden("user %q is not in group %q", claims.Custom.User, p.Group)
}

func init() {
	RegisterPayload("group", func() UserPayload { return &GroupViewPayload{} })
}
```

Rebuild, then select it in the configuration:

```yaml
view_payload: group
```

and call `/view` with `{"file": "...", "group": "group-a"}`. An unknown
`view_payload` name stops the server at start-up with the list of known names.
Test a payload by calling `Validate` directly with an `AuthEnv{Now: ...}`;
`auth_test.go` has examples for building tokens.

## The other endpoints in `file_system_mode`

With `file_system_mode: true` the data endpoints are closed to everyone who has
not been through `/view` or `/viewform`:

* A successful `/view` or `/viewform` also sets a cookie (`gonexus_view`,
  `HttpOnly`, `SameSite=Lax`, `Secure` over HTTPS) that lists the files this
  client may read, each valid for `session_ttl` seconds (default 3600). The
  cookie is signed with a random key made at start-up, so restarting the server
  ends all sessions; it holds at most 10 files, the ones that expire first drop out.
* `/tree`, `/entries`, `/group` and `/field` answer 403 unless the cookie grants
  exactly the `key` asked for. The web UI that `/view` redirects to works
  unchanged, because the browser sends the cookie with its requests.
* `/files` is switched off (403): it would list file names to anybody.
  Typing a path in the UI's file box therefore shows an error; open files through
  `/view`.
* `/healthz` and the static UI under `/ui/` stay open.

```yaml
session_ttl: 3600      # NEXUS_SESSION_TTL, seconds
```

Scripts and the Python client that called `/tree?key=...` directly now have to
call `/view` first and send the cookie back, for example
`curl -c jar -d file=/nfs/a.nxs -d token=$TOKEN http://host:8081/viewform` and then
`curl -b jar -G http://host:8081/tree --data-urlencode key=/nfs/a.nxs`.
(Keep the server reachable only through the same host name, since the cookie is
per host.)

Limits: the cookie proves that someone passed `/view` for that file, it is not
bound to a person; anyone who holds the cookie can read the files in it until it
expires. Behind a load balancer with several instances, a cookie is only valid on
the instance that issued it.

In the other modes (S3, `data_dir`) the data endpoints are still not protected;
only `/view` and `/viewform` are.

`/view` no longer answers GET (405): the old `/view?file=...` link must become a
POST (for a browser, an HTML form posting to `/viewform`).
