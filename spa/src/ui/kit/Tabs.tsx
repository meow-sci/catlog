import type { ReactNode } from 'react';
import { Tab, TabList, Tabs } from 'react-aria-components';
import { cn } from '../cn.ts';

/**
 * A row of windows — `alltime | daily | weekly | monthly | yearly`.
 *
 * React Aria's `Tabs` for the roving tabindex and the arrow-key movement, but
 * **each tab is a link**, because a period is a *place*: `?period=weekly` is
 * part of the URL, it is what makes "how did this week go" something you can
 * send somebody, and it is what lets the back button undo the choice. `Tab`
 * takes an `href` and keeps its keyboard semantics.
 *
 * There is no `TabPanel` here. The panel is the page, and rendering the board
 * inside a tab panel would make the tab own the data — which it must not, since
 * the URL already does.
 */
export function PeriodTabs(props: {
  readonly label: string;
  readonly selected: string;
  readonly periods: readonly string[];
  readonly hrefFor: (period: string) => string;
  readonly labelFor?: (period: string) => ReactNode;
  readonly className?: string;
}) {
  return (
    <Tabs selectedKey={props.selected} className={cn('min-w-0', props.className)}>
      <TabList
        aria-label={props.label}
        items={props.periods.map((period) => ({ id: period }))}
        className="border-border bg-panel-sunken inline-flex flex-wrap gap-1 rounded-lg border p-1"
      >
        {(item: { id: string }) => (
          <Tab
            id={item.id}
            href={props.hrefFor(item.id)}
            className="text-fg-muted data-hovered:bg-wash-hover data-hovered:text-fg data-selected:bg-accent data-selected:text-accent-fg cursor-pointer rounded-md px-3 py-1 text-sm font-medium transition-colors duration-150 data-selected:font-semibold"
          >
            {props.labelFor?.(item.id) ?? item.id}
          </Tab>
        )}
      </TabList>
    </Tabs>
  );
}
