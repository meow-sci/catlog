import type { BoardResponse, BoardsResponse, FeedResponse, PlayerResponse } from './types.ts';

/**
 * The catlog read API client.
 *
 * Deliberately small: four typed calls over one `fetch` wrapper. There is no
 * caching layer here — caching is the server's job (§4.8 ships
 * `s-maxage=30, stale-while-revalidate=300` on every response) and duplicating
 * it in the browser would only add a second, wronger answer.
 */

/**
 * Origin of the read API, without a trailing slash.
 *
 * Empty string means same-origin, which is what `pnpm dev` uses so the Vite
 * proxy can stand in for the server. `undefined` (no `.env` at all) falls back
 * to the §5.3 dev address, so a bare `vite build` produces something that works
 * against `make dev`.
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

// --- the four endpoints ------------------------------------------------------

export function getBoards(signal?: AbortSignal): Promise<BoardsResponse> {
  return apiGet<BoardsResponse>('/v1/leaderboards', signal);
}

export function getBoard(
  stat: string,
  limit: number,
  offset: number,
  signal?: AbortSignal,
): Promise<BoardResponse> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  return apiGet<BoardResponse>(
    `/v1/leaderboards/${encodeURIComponent(stat)}?${query.toString()}`,
    signal,
  );
}

export function getPlayer(handle: string, signal?: AbortSignal): Promise<PlayerResponse> {
  return apiGet<PlayerResponse>(`/v1/players/${encodeURIComponent(handle)}`, signal);
}

export function getFeed(limit: number, signal?: AbortSignal): Promise<FeedResponse> {
  return apiGet<FeedResponse>(`/v1/feed?limit=${String(limit)}`, signal);
}

/** URL of the JSON activity stream. Consumed by EventSource, not by fetch. */
export function feedStreamUrl(): string {
  return apiUrl('/v1/feed/stream');
}
