import type { ReactNode } from 'react';
import {
  Button as AriaButton,
  ToggleButton as AriaToggleButton,
  type ButtonProps,
  type ToggleButtonProps,
} from 'react-aria-components';
import { cn } from '../cn.ts';

/**
 * Buttons, on React Aria's press handling.
 *
 * Not `<button onClick>`: React Aria normalises pointer, touch, keyboard and
 * screen-reader activation into one `onPress`, cancels a press that drags off
 * the target, and drives the `data-hovered` / `data-pressed` /
 * `data-focus-visible` attributes the styles below key on — including
 * `data-focus-visible`, which is the attribute `index.css`'s single focus ring
 * hangs off.
 *
 * **A button is for an action; a link is for a place.** Anything that navigates
 * stays an `<a href>` (see [LinkButton]) so middle-click, cmd-click and "copy
 * link address" keep working and the router's one delegated listener still sees
 * it.
 */
type Variant = 'primary' | 'secondary' | 'ghost';

const BASE =
  'inline-flex items-center justify-center gap-1.5 rounded-md text-sm font-medium ' +
  'transition-colors duration-150 cursor-pointer min-h-8 px-3 py-1.5 ' +
  'data-disabled:cursor-not-allowed data-disabled:opacity-40';

const VARIANT: Record<Variant, string> = {
  // `--color-accent` may only ever be a *fill*: #2cfa1f on white is 1.42:1.
  // `--color-accent-fg` on top of it is ~13:1 in both themes.
  primary:
    'bg-accent text-accent-fg data-hovered:bg-accent-hover data-pressed:bg-accent-press font-semibold',
  secondary:
    'border border-border-strong bg-panel-raised text-fg data-hovered:bg-wash-hover data-pressed:bg-wash-press',
  ghost: 'text-fg-muted data-hovered:bg-wash-hover data-hovered:text-fg data-pressed:bg-wash-press',
};

export function Button(props: ButtonProps & { readonly variant?: Variant }) {
  const { variant = 'secondary', className, ...rest } = props;
  return <AriaButton {...rest} className={cn(BASE, VARIANT[variant], className as string)} />;
}

/**
 * A two-state button — the theme switch, "This is me".
 *
 * Selection is announced through `aria-pressed`, which React Aria sets, so the
 * state is a fact a screen reader hears rather than a colour it cannot see.
 */
export function ToggleButton(props: ToggleButtonProps & { readonly variant?: Variant }) {
  const { variant = 'secondary', className, ...rest } = props;
  return (
    <AriaToggleButton
      {...rest}
      className={cn(
        BASE,
        VARIANT[variant],
        'data-selected:bg-accent data-selected:text-accent-fg data-selected:border-transparent data-selected:font-semibold',
        className as string,
      )}
    />
  );
}

/**
 * A link that looks like a button.
 *
 * A plain anchor on purpose. React Aria has a `Link`, but every in-app link here
 * is intercepted by one delegated `click` listener on `document` precisely so
 * the links stay ordinary anchors — that is what makes the browser's own
 * affordances (status bar preview, open-in-new-tab, copy address) work, and it
 * is why a component library is not allowed to own them.
 *
 * The classes are spelled out rather than derived from [VARIANT] by string
 * substitution, because Tailwind extracts class names by scanning the source:
 * a variant computed at runtime is a variant whose CSS was never generated.
 */
const LINK_BASE =
  'inline-flex items-center justify-center gap-1.5 rounded-md text-sm font-medium ' +
  'transition-colors duration-150 min-h-8 px-3 py-1.5 ' +
  'aria-disabled:cursor-not-allowed aria-disabled:opacity-40';

const LINK_VARIANT: Record<Variant, string> = {
  primary: 'bg-accent text-accent-fg hover:bg-accent-hover active:bg-accent-press font-semibold',
  secondary:
    'border border-border-strong bg-panel-raised text-fg hover:bg-wash-hover active:bg-wash-press',
  ghost: 'text-fg-muted hover:bg-wash-hover hover:text-fg active:bg-wash-press',
};

export function LinkButton(props: {
  readonly href: string;
  readonly variant?: Variant;
  readonly className?: string;
  readonly children: ReactNode;
  readonly 'aria-label'?: string;
}) {
  const { variant = 'secondary', href, className, children } = props;
  return (
    <a
      href={href}
      aria-label={props['aria-label']}
      className={cn(LINK_BASE, LINK_VARIANT[variant], className)}
    >
      {children}
    </a>
  );
}

/**
 * The unavailable direction of a pager.
 *
 * A `<span aria-disabled>` rather than a dead link: there is no URL to offer,
 * and a link to nowhere is worse than no link.
 */
export function DisabledLinkButton(props: { readonly children: ReactNode }) {
  return (
    <span
      aria-disabled
      className={cn(LINK_BASE, LINK_VARIANT.secondary, 'cursor-not-allowed opacity-40')}
    >
      {props.children}
    </span>
  );
}
