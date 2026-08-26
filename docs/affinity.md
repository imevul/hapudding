# Affinity

## Lookup order

1. **Token** (hashed) → backend. Always wins.
2. Else **DeviceId** → backend.
3. Else, for **header-less media** (images, video/audio streams, subtitles, attachments, trickplay):
   - `hap_backend` cookie, only if that backend already has a live token or DeviceId row (not sole affinity).
   - Else **session IP glue**: hash(client IP) written on login and token-bearing requests.
4. Else **anon glue**: hash(client IP + User-Agent), short TTL.
5. Else pick among backends eligible for new clients (consistent-hash DeviceId when present; otherwise least-loaded). `hap_backend` cookie is a preference among eligible backends only.

A token issued by Server A is never sent to Server B. Store lookup errors fail closed (HAP 503) instead of looking like a cache miss.

Password / Quick Connect login is the exception: if the chosen backend times out or returns 401, HAP retries other eligible backends with the same body (no existing token is forwarded). A 200 binds DeviceId and token to the server that issued them.

`affinity.user_affinity` (`- username: backend`) is a **login hint**, not a pin. On `AuthenticateByName`, if there is no live token or DeviceId binding, HAP tries that user's preferred backend first among **eligible** backends. Token/device pins, operator disable, health eligibility, and fail-closed still win. Anon/cookie/least-loaded placement does not block the hint. `POST /hap/users/by-name/{username}/unpin` on the status bind clears that username's stored token pins and those tokens' DeviceId rows so the next login can take the hint (or least-loaded). See [config.md](config.md).

The default store is **Postgres**. SQLite remains available and uses WAL, a 5s busy timeout, and a single writer connection so a home-page image burst does not lock the affinity DB. Switching drivers does not migrate bindings.

## Policies

Consulted when the bound backend is **unhealthy**. Happy path is identical for all policies.

### `force_reauth` (default)

Drop that client's token and DeviceId bindings. Do not forward the old token. New unauthenticated traffic may go to another eligible backend. The client must log in again and will see a different Server Id.

### `fail_closed`

Keep the binding. Return HAP 503. When A recovers, the same token works. New clients (no live binding) may still use other healthy backends.

### `pin_unhealthy`

Like `fail_closed` for bound clients. New clients that hashed or glued to A stay on A even if A is down.

## Logout

Logout drops the **token** row only. DeviceId and anon glue stay. Re-login on the same client stays on the same backend while A is healthy. See SPEC.

## Auth flow

HAP never logs in as the user. It chooses a backend and forwards. See the sequence in the project plan: discovery binds DeviceId, login peeks `AccessToken`, authenticated requests follow the token, logout forgets the token only.

## Gray-list

`affinity.graylist` selects a **different policy** for matching clients. Default: built-in Infuse (`Client` contains `Infuse`, plus `/InfuseSync`) uses `fail_closed` while everyone else uses global `affinity.policy` (`force_reauth`).

Classification (any hit):

1. This request’s `Client=` or path prefix
2. Stored token row `client` (token-only `/socket?api_key=`)
3. Stored DeviceId `client` (survives logout)

Not classified by `userId`. A later header-less request does not clear a stamped Infuse DeviceId/token.

Gray-listed hops also disable upstream keep-alive and flush immediately. See [clients.md](clients.md).

## Image cache

Optional (`performance.cache.enabled`, on by default after `Load`). Not an affinity source. After a backend is chosen, HAP may reuse a prior `200` image for that **same backend** + path + query + `Accept`. Memory is the hot LRU; `performance.cache.disk` (on by default) persists the same key on disk so a restart does not refetch. It does not share posters across backends and does not cache `/Users/…/Images`.

## Library JSON cache

Optional (`performance.library.enabled`, on by default after `Load`). Keyed by **backend + token hash + URL**. Allowlisted home-page GETs only (`Views`, `Resume`, `NextUp`, `Latest`). A token issued on server-b is never used to serve server-a JSON. Mutations (`/Sessions/Playing`, played/favorite/UserData/rating) drop that token’s entries.

Identical in-flight image/library GETs may be coalesced (`performance.coalesce`). See [config.md](config.md).

## Infuse vs failover

There is no policy that keeps Infuse Library Mode cache valid *and* silently fails over. Gray-list `fail_closed` only avoids a surprise Server Id. See [clients.md](clients.md).
