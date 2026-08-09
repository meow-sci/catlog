# Vendored Cloudflare ranges — a fallback, not the source

`roles/catlog_nginx` fetches https://www.cloudflare.com/ips-v4 and `/ips-v6` on
every run, for nginx's `set_real_ip_from` **and nothing else**. Nothing on the
box filters packets by these ranges; that is the DigitalOcean cloud firewall's
job and it is maintained by hand.

These files are used only when the fetch fails, and the role says loudly which
of the two it used.

**They will go stale, and a stale list is not harmless.** A range Cloudflare has
released is a range nginx still trusts to set `CF-Connecting-IP` — the header
every rate limit and every access log entry is keyed on. A range Cloudflare has
added is edge traffic whose real client address nginx will not learn, so those
visitors all share one rate-limit bucket.

The same list is what the **DigitalOcean firewall** should allow inbound on 443.
When the role reports that the published list has moved, update it there too.

Refresh with:

    curl -s https://www.cloudflare.com/ips-v4 > cloudflare-ips-v4.txt
    curl -s https://www.cloudflare.com/ips-v6 > cloudflare-ips-v6.txt

Captured 2026-08-08, and confirmed against the owner's copy the same day.
