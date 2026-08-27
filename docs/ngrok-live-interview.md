# ngrok live-interview runbook

This service can receive real webhook calls through an ngrok HTTPS tunnel. The
tunnel exposes order-sync only; PostgreSQL, the helper dashboard, and the SAP
target remain private/local unless explicitly configured otherwise.

## 1. Prepare the service

From PowerShell, configure ngrok once on the machine if needed:

```powershell
ngrok config add-authtoken <your-ngrok-authtoken>
```

Start order-sync and confirm it is healthy:

```powershell
docker compose up -d --build
Invoke-WebRequest -UseBasicParsing http://localhost:3000/health
```

For an interview or demo, set secrets and overrides in a local `.env` file.
Do not commit this file or share its contents:

```dotenv
WEBHOOK_SECRET=replace-with-a-local-secret
SAP_API_URL=http://mock-sap:4000/api/orders
SAP_TIMEOUT_MS=3000
SAP_ATTEMPTS_BEFORE_WAITING=3
SAP_MAX_ATTEMPTS=5
```

Recreate order-sync after changing Compose environment values:

```powershell
docker compose up -d --force-recreate order-sync
```

`SAP_API_URL` may be replaced with an accessible SAP or test endpoint. It is the
complete target URL used by the service for outbound order synchronization; it
is independent of the public ngrok URL.

## 2. Start the tunnel

Use the repository helper:

```powershell
.\scripts\ngrok.ps1
```

Or choose a different local service port:

```powershell
.\scripts\ngrok.ps1 -Port 3000
```

Copy the `https://...ngrok...` forwarding URL shown by ngrok. The public
webhook URLs are:

```text
https://<ngrok-host>/api/webhooks/shop
https://<ngrok-host>/api/webhooks/payment
```

Use HTTPS for external webhook providers. The default ngrok hostname is
ephemeral, so update provider configuration whenever the tunnel is restarted
unless a reserved domain is configured.

## 3. Configure webhook providers

For both shop and payment providers:

1. Set the provider callback URL to the corresponding public URL above.
2. Send JSON with `Content-Type: application/json`.
3. If `WEBHOOK_SECRET` is non-empty, send the exact value in the
   `X-Webhook-Secret` header.
4. Keep provider retries enabled for network failures, but do not duplicate
   events manually; webhook processing is idempotent.

Example local requests through the tunnel:

```powershell
$baseUrl = "https://<ngrok-host>"
$headers = @{ "X-Webhook-Secret" = "replace-with-a-local-secret" }

Invoke-WebRequest -Method Post -Uri "$baseUrl/api/webhooks/shop" `
  -Headers $headers -ContentType "application/json" `
  -InFile .\examples\shop-order.json

Invoke-WebRequest -Method Post -Uri "$baseUrl/api/webhooks/payment" `
  -Headers $headers -ContentType "application/json" `
  -InFile .\examples\payment.json
```

If `WEBHOOK_SECRET` is empty, omit the header. Never put the secret in the URL,
the request body, screenshots, or recorded interview notes.

## 4. Verify during the interview

Keep these commands available in separate terminals:

```powershell
docker compose logs -f order-sync
$baseUrl = "https://<ngrok-host>"
Invoke-WebRequest -UseBasicParsing "$baseUrl/api/orders/<order-id>"
```

Check the ngrok inspector at `http://127.0.0.1:4040` to confirm request paths,
response codes, and payload shape without exposing credentials.

Expected webhook responses are `201` for a new shop order, `200` for a replayed
event or normal processing, `202` for a payment awaiting its shop order, and
`409` for a business conflict. Hardware orders
may wait for `HARDWARE_SYNC_DELAY_SECONDS` before SAP delivery.

## 5. Stop and clean up

Press Ctrl+C in the ngrok terminal, then stop the local stack when finished:

```powershell
docker compose down
```

The ngrok hostname stops routing as soon as the tunnel exits. Rotate any
temporary `WEBHOOK_SECRET` used for the interview afterward.
