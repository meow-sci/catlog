/// <reference types="vite/client" />

interface ImportMetaEnv {
  /**
   * Origin of the catlog read API, without a trailing slash.
   *
   * Empty means "same origin" — that is what `.env.development` sets so the dev
   * server's `/v1` proxy handles it. A production build gets the real API origin
   * from the deploy workflow.
   */
  readonly VITE_CATLOG_API_BASE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
