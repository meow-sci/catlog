/**
 * Which boards the front page previews.
 *
 * Its own module because it is a pure function and `HomePage.tsx` may only
 * export components (fast refresh), and because it is the answer to a question
 * that used to be a hard-coded list: the board index is assembled from the data
 * — two board families take their keys from the event stream, since KSA's
 * celestial systems are game content the server treats as opaque — so a front
 * page pinned to three named boards can be pinned to a board that is not there.
 */

/**
 * The boards the front page would like to preview: one record, one speed record
 * and one counter, so a first-time visitor sees what all three kinds of board
 * look like without clicking.
 *
 * A *preference*, not a contract. The server-rendered site makes the same choice
 * independently in `server/internal/web/web.go`; the two are separate
 * applications that share an HTTP contract and nothing else, and deliberately
 * are not kept in sync. [pickFeatured] is what stops this rotting.
 */
export const PREFERRED_BOARDS = [
  'biggest_lithobrake_survived',
  'fastest_orbital_speed',
  'rud_total',
];

/** How many boards the front page previews. */
export const FEATURED_COUNT = 3;

/**
 * The stats to preview, given the boards the server actually listed.
 *
 * Preferred boards first, in their stated order, then whatever else exists, up
 * to [FEATURED_COUNT]. Pure and total: an empty index yields an empty list
 * rather than three requests for boards that are not there, and a preference the
 * server has stopped publishing costs a different choice of preview rather than
 * an error panel.
 */
export function pickFeatured(
  available: readonly string[],
  preferred: readonly string[] = PREFERRED_BOARDS,
): string[] {
  const have = new Set(available);
  const picked = preferred.filter((stat) => have.has(stat));
  for (const stat of available) {
    if (picked.length >= FEATURED_COUNT) break;
    if (!picked.includes(stat)) picked.push(stat);
  }
  return picked.slice(0, FEATURED_COUNT);
}
