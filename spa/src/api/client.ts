import type {
  BoardResponse,
  BoardsResponse,
  CompareResponse,
  EventsResponse,
  FeedResponse,
  PlayerResponse,
  SearchResponse,
} from './types.ts';

/**
 * The catlog read API client.
 *
 * Deliberately small: one typed call per endpoint over one `fetch` wrapper.
 * There is no caching layer *here* — the split is that the server owns
 * freshness (§4.8 ships `s-maxage=30, stale-while-revalidate=300` on every
 * response, which is aimed at a shared cache and ignored by browsers), while
 * `useResource` keeps a client-side memory of the same 30-second window: it
 * dedupes concurrent identical requests and reuses a settled answer within the
 * window the server already declared fresh. Nothing in this file remembers
 * anything, so a caller outside `useResource` always hits the network.
 */

/**
 * Origin of the read API, without a trailing slash.
 *
 * Empty string means same-origin, which is what `pnpm dev` uses so the Vite
 * proxy can stand in for the server. `undefined` (no `.env` at all) falls back
 * to the §5.3 dev address, so a bare `pnpm build` produces something that works
 * against a locally running catlogd.
 */
export const API_BASE = (import.meta.env.VITE_CATLOG_API_BASE ?? 'http://127.0.0.1:8080').replace(
  /\/+$/,
  '',
);

/** Absolute URL for a read-API path. Exported for the SSE stream, which needs a URL rather than a fetch. */
export function apiUrl(path: string): string {
  return API_BASE + path;
}

/**
 * A read-API call that did not return a usable body.
 *
 * `status` is the HTTP status when there was one, and 0 when the request never
 * completed (offline, DNS, CORS refusal — the browser deliberately does not tell
 * JavaScript which). `code` is the server's machine-readable `error` field
 * (§4.9) when the body carried one.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }

  /**
   * True for the one status this app treats as content rather than failure.
   *
   * §4.8 returns 404 for a handle that is unknown, retired *or* banned — one
   * answer for all three on purpose, so the endpoint is not a ban oracle. The UI
   * must not try to tell them apart, and this getter is the only place that
   * decision surfaces.
   */
  get notFound(): boolean {
    return this.status === 404;
  }
}

/** Coerces anything thrown by `fetch` or by us into an ApiError. */
export function asApiError(cause: unknown): ApiError {
  if (cause instanceof ApiError) return cause;
  if (cause instanceof Error) return new ApiError(0, 'network', cause.message);
  return new ApiError(0, 'network', String(cause));
}

/** The §4.9 error body, when the server managed to send one. */
interface ErrorBody {
  error?: string;
  detail?: string;
}

async function readError(res: Response): Promise<ApiError> {
  let code = 'http_' + String(res.status);
  let detail = res.statusText || 'request failed';
  try {
    const body = (await res.json()) as ErrorBody;
    if (typeof body.error === 'string' && body.error !== '') code = body.error;
    if (typeof body.detail === 'string' && body.detail !== '') detail = body.detail;
  } catch {
    // A non-JSON error body is normal — a proxy 502 is HTML, and a CORS
    // preflight failure has no body at all. The status is the real signal.
  }
  return new ApiError(res.status, code, detail);
}

/**
 * GETs `path` and decodes it as JSON.
 *
 * Every non-2xx becomes an ApiError, and so does a 2xx whose body is not JSON —
 * which is what a misconfigured proxy or a captive portal actually sends, and
 * which would otherwise surface as an unhandled parse error deep inside a
 * component.
 */
export async function apiGet<T>(path: string, signal?: AbortSignal): Promise<T> {
  let res: Response;
  try {
    res = await fetch(apiUrl(path), {
      signal: signal ?? null,
      headers: { Accept: 'application/json' },
      // The read API is anonymous (§4.8) and the server never sends
      // Access-Control-Allow-Credentials. Saying so explicitly keeps a future
      // `credentials: 'include'` from being added by habit.
      credentials: 'omit',
    });
  } catch (cause) {
    // An abort is the caller's own cleanup and must stay recognisable.
    if (signal?.aborted === true) throw cause;
    // Everything else — offline, DNS, a refused CORS preflight — reaches
    // JavaScript as an indistinguishable TypeError. Normalising it here means
    // every caller handles exactly one error type.
    throw asApiError(cause);
  }
  if (!res.ok) throw await readError(res);
  try {
    return (await res.json()) as T;
  } catch (cause) {
    throw new ApiError(
      res.status,
      'bad_response',
      `expected JSON from ${path}: ${cause instanceof Error ? cause.message : String(cause)}`,
    );
  }
}

// --- the endpoints -----------------------------------------------------------

export function getBoards(signal?: AbortSignal): Promise<BoardsResponse> {
  return apiGet<BoardsResponse>('/v1/leaderboards', signal);
}

/**
 * One page of one board.
 *
 * `period` selects a rolling window (`daily`, `weekly`, `monthly`, `yearly`);
 * `alltime` is omitted from the query so the default URL stays the one a CDN
 * already holds.
 */
export function getBoard(
  stat: string,
  limit: number,
  offset: number,
  period: string = ALLTIME,
  signal?: AbortSignal,
): Promise<BoardResponse> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  if (period !== ALLTIME && period !== '') query.set('period', period);
  return apiGet<BoardResponse>(
    `/v1/leaderboards/${encodeURIComponent(stat)}?${query.toString()}`,
    signal,
  );
}

/** The window a board reads over when nothing asked for another one. */
export const ALLTIME = 'alltime';

export function getPlayer(handle: string, signal?: AbortSignal): Promise<PlayerResponse> {
  return apiGet<PlayerResponse>(`/v1/players/${encodeURIComponent(handle)}`, signal);
}

/**
 * The shortest query the search endpoint accepts.
 *
 * **Below this the server answers 400, not an empty 200.** A search box that
 * fires on the first keystroke therefore produces an error on every single
 * search, so the guard lives at the call sites *and* here: `searchHandles`
 * refuses rather than sending a request it knows will fail.
 */
export const MIN_QUERY_LENGTH = 2;

/** The longest query the server accepts — a handle's own cap. Truncate, do not send. */
export const MAX_QUERY_LENGTH = 150;

/**
 * Handle search.
 *
 * Returns an empty result for a query that is too short rather than throwing or
 * requesting: "not enough typed yet" is not an error the user must act on, and
 * §9.3's rule for the 400s is that the right fix is not to render them, it is
 * not to send the request.
 */
export function searchHandles(
  q: string,
  limit: number,
  signal?: AbortSignal,
): Promise<SearchResponse> {
  const query = q.trim().slice(0, MAX_QUERY_LENGTH);
  if (query.length < MIN_QUERY_LENGTH) {
    return Promise.resolve({ query, limit, handles: [] });
  }
  const params = new URLSearchParams({ q: query, limit: String(limit) });
  return apiGet<SearchResponse>(`/v1/players?${params.toString()}`, signal);
}

/**
 * Up to [MAX_COMPARE_HANDLES] handles, side by side.
 *
 * One request rather than N profile reads: N answers can disagree — a projection
 * commit between the first and the last shows one player's new record next to
 * another's stale rank — and this reads them all against one view.
 *
 * An empty list is a valid, empty comparison, which is exactly what a picker
 * with nobody in it should ask for.
 */
export function getCompare(
  handles: readonly string[],
  signal?: AbortSignal,
): Promise<CompareResponse> {
  const params = new URLSearchParams({ handles: handles.join(',') });
  return apiGet<CompareResponse>(`/v1/compare?${params.toString()}`, signal);
}

/**
 * The comparison cap, matching `readapi.MaxCompareHandles`.
 *
 * Extras are **dropped, not rejected**, and the effective list is echoed back —
 * so a picker that stops at eight is telling the truth, and one that does not
 * silently loses columns.
 */
export const MAX_COMPARE_HANDLES = 8;

/**
 * One page of a player's raw event log, newest first.
 *
 * `before` is the opaque cursor from the previous page's `next`. **Page until
 * `next` is absent, never until a page comes back short** — a `?type=`-filtered
 * page that hit the server's scan bound looks exactly like the end of the log
 * and is not.
 */
export function getPlayerEvents(
  handle: string,
  options: {
    readonly type?: string | undefined;
    readonly before?: string | undefined;
    readonly limit?: number | undefined;
  } = {},
  signal?: AbortSignal,
): Promise<EventsResponse> {
  const params = new URLSearchParams({ limit: String(options.limit ?? EVENT_PAGE_SIZE) });
  if (options.type !== undefined && options.type !== '') params.set('type', options.type);
  if (options.before !== undefined && options.before !== '') params.set('before', options.before);
  return apiGet<EventsResponse>(
    `/v1/players/${encodeURIComponent(handle)}/events?${params.toString()}`,
    signal,
  );
}

/** How many events one page of the raw log asks for. The server clamps above 200. */
export const EVENT_PAGE_SIZE = 50;

/**
 * One page of the global raw event log, newest first — every player mixed
 * together, each row naming its handle.
 *
 * The same envelope, cursor and rules as [getPlayerEvents]: `before` is the
 * opaque cursor from the previous page's `next`, and a client pages until
 * `next` is **absent**, never until a page comes back short. `handle` narrows
 * it to one player server-side (and 404s an unknown one with the same one
 * answer as everywhere else).
 */
export function getEvents(
  options: {
    readonly type?: string | undefined;
    readonly handle?: string | undefined;
    readonly before?: string | undefined;
    readonly limit?: number | undefined;
  } = {},
  signal?: AbortSignal,
): Promise<EventsResponse> {
  const params = new URLSearchParams({ limit: String(options.limit ?? EVENT_PAGE_SIZE) });
  if (options.type !== undefined && options.type !== '') params.set('type', options.type);
  if (options.handle !== undefined && options.handle !== '') params.set('handle', options.handle);
  if (options.before !== undefined && options.before !== '') params.set('before', options.before);
  return apiGet<EventsResponse>(`/v1/events?${params.toString()}`, signal);
}

/**
 * URL of the live half of the raw event log (`event: raw` frames of EventRow
 * JSON). Consumed by EventSource, not by fetch. The stream has no replay on
 * reconnect — a reconnecting client re-reads the paginated snapshot — and it
 * answers 429 once the server's subscriber cap is reached.
 */
export function eventsStreamUrl(
  options: { readonly type?: string; readonly handle?: string } = {},
): string {
  const params = new URLSearchParams();
  if (options.type !== undefined && options.type !== '') params.set('type', options.type);
  if (options.handle !== undefined && options.handle !== '') params.set('handle', options.handle);
  const query = params.size > 0 ? `?${params.toString()}` : '';
  return apiUrl('/v1/events/stream' + query);
}

export function getFeed(limit: number, signal?: AbortSignal): Promise<FeedResponse> {
  return apiGet<FeedResponse>(`/v1/feed?limit=${String(limit)}`, signal);
}

/** URL of the JSON activity stream. Consumed by EventSource, not by fetch. */
export function feedStreamUrl(): string {
  return apiUrl('/v1/feed/stream');
}
