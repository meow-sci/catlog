/**
 * Splits prose from the data modules on backticks so `like_this` renders as
 * code. The data files are plain TypeScript strings, not Markdown, so this is
 * the one bit of formatting they are allowed to carry.
 */
export interface Segment {
  code: boolean;
  text: string;
}

export function segments(text: string): Segment[] {
  return text
    .split("`")
    .map((part, i) => ({ code: i % 2 === 1, text: part }))
    .filter((s) => s.text !== "");
}
