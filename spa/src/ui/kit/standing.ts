/**
 * How far up a board a rank sits, as a whole percentage.
 *
 * A port of `web/templates.go`'s `standing`, so the bar on a profile row is the
 * same length on both frontends.
 *
 * **The two inputs do not count the same population, and the arithmetic has to
 * respect that.** `rank` is ban-filtered — a banned account is removed from the
 * board rather than leaving a hole in the numbering — while `players` counts
 * rows, banned included. So a rank can be *better* than the denominator implies
 * and never worse, which is why this clamps rather than trusting the division:
 * a percentile above 100 % is arithmetic nobody should ever be shown.
 */
export function standing(rank: number, players: number): number {
  if (players <= 0 || rank < 1) return 0;
  const behind = Math.round((1 - (rank - 1) / players) * 100);
  return Math.min(Math.max(behind, 0), 100);
}
