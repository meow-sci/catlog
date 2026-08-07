/**
 * Dashboard "new handle" wizard (INITIAL_IMPL_PLAN §5.7).
 *
 * Generates a P-256 keypair with WebCrypto, POSTs only the public JWK to
 * /api/handles, then assembles catlog-credential.json (§4.6) client-side and offers
 * it as a download — the private key never reaches the server.
 *
 * WP0 scaffolding: this module exists so the site build pipeline has real input.
 * WP5 implements it.
 */

/** Marker export so the bundle is non-empty and importable. */
export const CATLOG_KEYGEN_READY = false;
