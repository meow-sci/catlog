import { AlertTriangle } from 'lucide-react';
import { Component, type ReactNode } from 'react';
import { Button, Panel } from './kit/index.ts';

interface Props {
  readonly children: ReactNode;
  /**
   * Changing this clears a caught error, so navigating away from a page that
   * crashed shows the next page rather than the tombstone. A prop rather than
   * `key={routeKey}` on this component, because remounting per route key would
   * destroy page state — an accumulated event log, a board's focus — on every
   * ordinary navigation.
   */
  readonly resetKey: string;
}

interface State {
  readonly error: Error | null;
  /** The resetKey the current `error` belongs to; see getDerivedStateFromProps. */
  readonly forKey: string | null;
}

/**
 * The last line between a rendering crash and a blank page.
 *
 * The event-log pages render arbitrary server JSON (review flag), and a
 * payload shaped in a way no renderer anticipated must cost one page view, not
 * the whole app shell. Deliberately tiny, and a class because that is still
 * the only way to catch a render error.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null, forKey: null };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  /**
   * Clears a caught error when the route moves on — derived rather than a
   * `componentDidUpdate` setState, so the recovery render is the same render
   * as the navigation instead of a second one after it.
   */
  static getDerivedStateFromProps(props: Props, state: State): Partial<State> | null {
    if (state.error !== null && state.forKey === null) {
      // The error was just caught; remember which route it belongs to.
      return { forKey: props.resetKey };
    }
    if (state.error !== null && state.forKey !== props.resetKey) {
      return { error: null, forKey: null };
    }
    return null;
  }

  render() {
    const { error } = this.state;
    if (error === null) return this.props.children;
    return (
      <Panel className="px-4 py-8">
        <div role="alert" className="flex flex-col items-start gap-3">
          <div className="flex items-start gap-3 text-sm">
            <AlertTriangle aria-hidden className="text-danger mt-0.5 size-4 shrink-0" />
            <div>
              <p className="text-fg">This page hit an error it could not draw around.</p>
              {/* The message, not a friendlier lie: same rule as `Failure` (§9.3). */}
              <p className="text-fg-muted mt-1 font-mono text-xs">{error.message}</p>
            </div>
          </div>
          <Button
            onPress={() => {
              window.location.reload();
            }}
          >
            Reload the page
          </Button>
        </div>
      </Panel>
    );
  }
}
