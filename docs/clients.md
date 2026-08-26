# Clients

HAP does not special-case apps except the **gray-list** (default: Infuse). These notes are for operators.

## Infuse (tvOS / iOS / macOS)

Library Mode caches item ids and InfuseSync checkpoints. Those are backend-local. Global `force_reauth` onto another server looks like a different library. HAP’s default **gray-list** matches `Client` containing `Infuse` and `/InfuseSync`, and applies `affinity.graylist.policy` (`fail_closed` by default): keep the binding, HAP 503 if that backend is down. Token-only and post-logout DeviceId requests still count if the stored client was Infuse.

There is **no neat way** to get Infuse Library Mode *and* automatic failover. `fail_closed` avoids a surprise Server Id; users still error until A is back or they re-add the share.

HAP hop tweaks for gray-listed requests: no upstream keep-alive (`Connection: close`), immediate response flush. HAP does **not** rewrite `Range` (including open-ended `bytes=0-`). Public listen is HTTP/1.1 only.

**Ingress in front of HAP** (not configurable from HAP):

- Disable HTTP/2 on the Infuse/Jellyfin vhost if open-ended Range + h2 causes 500/502.
- Allow TLS 1.2 (old Infuse failed TLS 1.3-only). HAP’s hop already allows 1.2+.
- Raise header buffer limits (`large_client_header_buffers` / equivalent). HAP’s public `MaxHeaderBytes` is 1MiB.

## Swiftfin

Official iOS/tvOS. Persists Server Id. Same `force_reauth` warning. Prefer `Authorization`; HAP still parses legacy headers.

## Jellyfin Web

`GET /web` is proxied like any other path. HAP does not yet preserve the public origin: hop `Host` is the backend URL host and `Location` is not rewritten, so a browser often 302s to `https://<backend>/web/…` and leaves HAP (image/library cache no longer apply). Native API clients are unaffected. See roadmap **P2-11**.

WebSocket is often `/socket?api_key=…&deviceId=…` — token only on the query string. HAP parses `ApiKey` / `api_key` and `deviceId` / `device_id`.

`<img>` and some stream URLs send no `Authorization`. HAP sets `hap_backend` on proxied responses and keeps those paths on a live session (cookie if that backend has a token/DeviceId, else IP glue from login). Token still wins when `api_key` is present.

## Android / Android TV

One TV, many DeviceIds (hashed with username). Token still wins. Do not idle-timeout WebSockets in tens of seconds.

## Delfin

Open source Linux GTK client (libsoup). Persistent UUID DeviceId, no cookies, Quick Connect, Intro Skipper and Trickplay plugin paths. HAP accepts `Expect: 100-continue` before the hop so password login cannot deadlock. Login that times out or 401s on the pinned backend is retried on other eligible servers. HAP proxies unknown plugin paths.

## CLIamp

Open source terminal player. **No cookies.** Username/password or a long-lived API token. Token-only requests still route by token.

## Finamp

May send `X-Emby-Token` and an extra `UserId=` MediaBrowser key (ignored). Offline cache of Server Id.

## Plugin paths

Intro Skipper, Jellyscrub, InfuseSync, and other unknown APIs are proxied with the same affinity. HAP does not allowlist-404 them.
