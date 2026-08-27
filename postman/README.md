# Postman collection

Import `order-sync.postman_collection.json` and `order-sync.local.postman_environment.json` from this directory. This collection covers the order-sync API only.

Run `docker compose up --build`, set `orderId` in the local environment, then call the shop and payment webhooks in either order. Hardware synchronization defaults to a 30-second delay. Set `webhookSecret` if `WEBHOOK_SECRET` is configured; the webhook requests send it as `X-Webhook-Secret`. Helper requests are in the sibling `order-sync-helper/postman` collection.
