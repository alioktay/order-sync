[CmdletBinding()]
param(
    [int]$Port = 3000
)

$ngrok = Get-Command ngrok -ErrorAction SilentlyContinue
if ($null -eq $ngrok) {
    throw "ngrok was not found on PATH. Install it and configure an authtoken first: https://ngrok.com/download"
}

if ($Port -lt 1 -or $Port -gt 65535) {
    throw "Port must be between 1 and 65535."
}

Write-Host "Forwarding public HTTPS traffic to http://localhost:$Port"
Write-Host "Webhook URLs will be:"
Write-Host "  https://oversweet-oaf-drainage.ngrok-free.dev/api/webhooks/shop"
Write-Host "  https://oversweet-oaf-drainage.ngrok-free.dev/api/webhooks/payment"
Write-Host "Press Ctrl+C to stop the tunnel."

& $ngrok.Source http $Port
exit $LASTEXITCODE
