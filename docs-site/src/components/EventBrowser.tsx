import { useState } from "react";
import {
  EVENTS,
  FAMILY_LABEL,
  TRIGGER_LABEL,
  type CatlogEvent,
  type Family,
  type Trigger,
} from "../data/events";
import "./event-browser.css";

const FAMILIES = Object.keys(FAMILY_LABEL) as Family[];
const TRIGGERS = Object.keys(TRIGGER_LABEL) as Trigger[];

function matches(e: CatlogEvent, q: string, family: Family | "", trigger: Trigger | ""): boolean {
  if (family !== "" && e.family !== family) return false;
  if (trigger !== "" && e.trigger !== trigger) return false;
  if (q === "") return true;
  const needle = q.toLowerCase();
  return (
    e.type.includes(needle) ||
    e.summary.toLowerCase().includes(needle) ||
    e.cause.toLowerCase().includes(needle) ||
    e.source.toLowerCase().includes(needle) ||
    e.fields.some((f) => f.key.toLowerCase().includes(needle)) ||
    e.feeds.some((f) => f.toLowerCase().includes(needle))
  );
}

/**
 * A filter over the whole event catalog. Every row links to the family page that
 * explains that event in full — this table is an index, not a replacement.
 */
export default function EventBrowser() {
  const [q, setQ] = useState("");
  const [family, setFamily] = useState<Family | "">("");
  const [trigger, setTrigger] = useState<Trigger | "">("");

  const shown = EVENTS.filter((e) => matches(e, q, family, trigger));

  return (
    <div className="eb">
      <div className="eb-controls">
        <label className="eb-search">
          <span className="eb-label">Search</span>
          <input
            type="search"
            value={q}
            placeholder="orbit, speed_ms, tumble…"
            onChange={(ev) => setQ(ev.target.value)}
          />
        </label>

        <label className="eb-select">
          <span className="eb-label">Group</span>
          <select value={family} onChange={(ev) => setFamily(ev.target.value as Family | "")}>
            <option value="">All groups</option>
            {FAMILIES.map((f) => (
              <option key={f} value={f}>
                {FAMILY_LABEL[f]}
              </option>
            ))}
          </select>
        </label>

        <label className="eb-select">
          <span className="eb-label">How it fires</span>
          <select value={trigger} onChange={(ev) => setTrigger(ev.target.value as Trigger | "")}>
            <option value="">Either</option>
            {TRIGGERS.map((t) => (
              <option key={t} value={t}>
                {TRIGGER_LABEL[t]}
              </option>
            ))}
          </select>
        </label>
      </div>

      <p className="eb-count">
        {shown.length} of {EVENTS.length} events
      </p>

      <ul className="eb-list">
        {shown.map((e) => (
          <li key={e.type} className="eb-item">
            <div className="eb-head">
              <a className="eb-type" href={`/catlog/events/${e.page}/#${e.type.replace(".", "")}`}>
                {e.type}
              </a>
              <span className={`eb-pill eb-pill-${e.trigger}`}>{TRIGGER_LABEL[e.trigger]}</span>
              {e.droppable ? (
                <span
                  className="eb-pill eb-pill-drop"
                  title="Dropped first if the local spool fills up"
                >
                  droppable
                </span>
              ) : null}
            </div>
            <p className="eb-summary">{e.summary}</p>
            <p className="eb-meta">
              <span className="eb-meta-k">Records</span>
              {e.fields.map((f) => f.key).join(", ")}
            </p>
            <p className="eb-meta">
              <span className="eb-meta-k">Feeds</span>
              {e.feeds.length > 0
                ? e.feeds.join(", ")
                : "nothing — recorded, but no leaderboard reads it"}
            </p>
          </li>
        ))}
      </ul>

      {shown.length === 0 ? <p className="eb-empty">Nothing matches that.</p> : null}
    </div>
  );
}
