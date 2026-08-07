/**
 * The catlog UI kit, on react-aria-components.
 *
 * One place the pages import from, so a leaderboard, a comparison and a raw
 * event log are visibly the same application. §10 makes react-aria-components
 * *required* on this side of the bake-off: the SPA is the half that gets to be a
 * proper application, and a real kit — rather than primitives scattered through
 * five pages — is what that has to mean in practice.
 *
 * What is built on React Aria and why, in one list:
 *
 * | Component | React Aria | What it buys that hand-rolling does not |
 * |---|---|---|
 * | `DataTable` & co. | `Table` | a real grid: cell-wise arrow keys, row headers, focus that survives pagination |
 * | `HandleComboBox` | `ComboBox` | arrow keys, Escape, `aria-activedescendant`, popover placement, result-count announcements |
 * | `HandleTags` | `TagGroup` | focusable chips, Backspace/Delete to remove, per-chip remove labels |
 * | `PeriodTabs` | `Tabs` | roving tabindex over links, so a window is still a place |
 * | `Details` | `Disclosure` | `aria-expanded`/`aria-controls` on a real button, panel out of the tree while closed |
 * | `Button`, `ToggleButton` | `Button` | one press model across pointer/touch/keyboard/AT, and the `data-focus-visible` the single focus ring hangs off |
 *
 * `HandleTags` is deliberately **not** re-exported here even though it is part
 * of the kit. Only the comparison page uses it, and this barrel is imported by
 * the app shell — so re-exporting it would drag React Aria's `TagGroup` into the
 * chunk every visitor downloads. Import it from `./HandleTags.tsx` directly.
 *
 * Everything else — `Panel`, `StatTile`, `Value`, `Rank`, `Pill`, `Loading`,
 * `Failure`, `Empty` — is presentation with no interaction to get wrong, so it
 * is plain markup and tokens.
 */
export { Button, DisabledLinkButton, LinkButton, ToggleButton } from './Button.tsx';
export {
  DataCell,
  DataRow,
  DataTable,
  DenseCell,
  HeadCell,
  HeadRow,
  TableRows,
} from './DataTable.tsx';
export { Details, Json } from './Details.tsx';
export { HandleComboBox } from './HandleComboBox.tsx';
export { Panel, PanelBody, PanelFooter, PanelHeader } from './Panel.tsx';
export { Pill, RewoundMark, Token } from './Pill.tsx';
export { Rank } from './Rank.tsx';
export { standing } from './standing.ts';
export { StatTile } from './StatTile.tsx';
export { Empty, Failure, Loading } from './Status.tsx';
export { PeriodTabs } from './Tabs.tsx';
export { Value } from './Value.tsx';
