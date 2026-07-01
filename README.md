# egate

A small application-to-application email gateway backed by SQLite and Postmark.

## Run

```sh
go run . --init-env .env
# Edit .env, then:
go run . --env ./.env
```

The initializer refuses to overwrite an existing file and creates it with owner-only permissions. Its Postmark token is intentionally invalid and must be replaced before email delivery will work.

The database and all tables are created automatically. The configured admin account is synchronized on every startup. Open `http://127.0.0.1:8080`, sign in, and create an API key.

In production, put egate behind a TLS reverse proxy. By design, login rate limiting uses the direct peer IP and does not trust `X-Forwarded-For`; ensure the proxy itself limits abusive clients or restrict access to the admin UI.

## Send an email

```sh
curl http://127.0.0.1:8080/v1/email \
  -H 'Authorization: Bearer eg_YOUR_KEY' \
  -H 'Content-Type: application/json' \
  -d '{
    "from": "sender@example.com",
    "to": "recipient@example.com",
    "subject": "Hello",
    "text_body": "Sent through egate"
  }'
```

Postmark's response and HTTP status are passed through to the caller. Failed login records older than 24 hours, expired bans, and expired sessions are removed during login attempts. Five failed attempts from an IP in a rolling 24-hour window produce a 24-hour ban by default.

## Go SDK

Install the module and import its SDK package:

```sh
go get github.com/englandsystems/egate
```

```go
client, err := sdk.NewClient("https://egate.example.com", os.Getenv("EGATE_API_KEY"))
if err != nil {
	log.Fatal(err)
}

_, err = client.SendEmail(context.Background(), sdk.Email{
	From:     "sender@example.com",
	To:       "recipient@example.com",
	Subject:  "Hello",
	TextBody: "Sent through egate",
})
```

The host is always supplied by the caller; the SDK has no assumed server address. Import `github.com/englandsystems/egate/sdk` in the source file.
