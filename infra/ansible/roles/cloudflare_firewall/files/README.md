# Vendored Cloudflare ranges — a fallback, not the source

`roles/cloudflare_firewall` fetches https://www.cloudflare.com/ips-v4 and
`/ips-v6` on every run. These files are used only when that fetch fails, and the
role says loudly which of the two it used.

**They will go stale, and a stale list is not harmless.** A range Cloudflare has
released is a range this box still trusts to set `CF-Connecting-IP` — which is
the header every rate limit and every access log entry is keyed on. A range
Cloudflare has added is legitimate edge traffic this box drops.

Refresh with:

    curl -s https://www.cloudflare.com/ips-v4 > cloudflare-ips-v4.txt
    curl -s https://www.cloudflare.com/ips-v6 > cloudflare-ips-v6.txt

Captured 2026-08-08.
