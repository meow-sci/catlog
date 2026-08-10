# catlog

**Leaderboards for [Kitten Space Agency](https://www.rocketwerkz.com/), from flights you were
already flying.**

Install the mod, claim a handle, and play. catlog watches quietly in the background and works out
what you did — the hardest landing anyone has walked away from, the fastest anything has ever gone,
the most kittens tumbled down a hill on Luna, the quickest anyone has got to Mars from a standing
start. Nothing to click, nothing to submit, no runs to set up.

---

## What's on it

**Boards.** Records and counters, ranked across everyone playing. Biggest lithobrake survived, peak
g survived, fastest surface and orbital speed, RUDs by cause, orbits achieved, bodies visited,
dockings, stagings, kittens recovered, distance travelled — and time-to-milestone boards where the
*smallest* number wins: fastest to orbit, fastest to each body.

Every board reads over **all time, or the last day, week, month or year**, so a good week is worth
something even if someone set an untouchable record in March.

**Boards you created by going somewhere.** catlog keeps no list of planets. A board for a place
exists because somebody flew there and the name turned up in the data — so a modded solar system
gets its own boards without anyone shipping an update. A new board is listed publicly once two
different players are on it.

**Your profile.** Every placement, every rank, and *its denominator*: "#3 of 41", not a bare "#3".

**Merit badges.** Once-only recognitions for first flights, ambitious stacks, survivable mistakes,
exploration and kittens. Each appears both across a player's current history and in the save that
earned it, with the celestial system shown so an achievement in one system is not confused with
the same title in another.

**The raw log.** Every event catlog recorded, browsable. This is the part that makes "your record is
214 m/s" checkable rather than asserted — you can read the flight it came from.

**A live feed.** What is happening right now, arriving without a reload.

**Comparison.** Up to eight handles side by side, across every board any of them is on. Good for a
friend group deciding who is actually the worst pilot.

**Stats about the stats.** How many events catlog holds, of what kinds, arriving how fast, since
when. No records, no ranking, nobody's handle — just the size of the thing.

All of it lives on one website — server-rendered and fast, with the feed arriving live over a
single stream.

---

## Getting on it

1. **Install the mod** into your KSA mods folder.
2. **Sign in** on the catlog website with Discord, Google or GitHub.
3. **Claim a handle** — this is the only name anyone ever sees.
4. **Download your credential file** and drop it where the mod can find it.
5. **Play.**

Your flights start showing up within a minute or so. Nothing else is ever asked of you.

Accounts need to be at least 30 days old to claim a handle, which is the cheapest way to keep
throwaway accounts off the boards.

---

## What catlog knows about you

**Not your email address.** catlog never asks any identity provider for it, so it is never received,
never stored, and cannot leak, be enumerated out of a database dump, or be handed to anyone. This is
the one thing that could not be added later and mean the same thing: a system that never had the
data has a property a system that deleted the data does not.

What it does hold is a one-way hash of your provider's account id, the handle you chose, and the
gameplay events your flights produced. **The handle is the only public identity there is.**

The public views are careful about the rest. Three fields derived from your *machine* rather than
your account — the install id, the per-kitten id and the save id — are dropped or relabelled per
player before anything is published, so two accounts shipping from the same computer cannot be
linked from outside. Your untrusted local clock is never published either.

**You can turn parts of it off.** The mod's settings file lists every kind of thing catlog records,
and any of them can be switched off individually — each one you turn off is simply a leaderboard you
stop appearing on. Five cannot be switched off, because they are what keeps everyone else's numbers
honest rather than anything about you.

**You can leave.** One button deletes everything: events, batches, credentials, archived data. Your
handle is retired rather than recycled, so nobody can take it and pretend to be you.

---

## About cheating

catlog assumes you are running the game as it shipped, and mostly leaves it there.

Every number on every board is **derived by the server from what happened**, never submitted by the
game. There is no "here is my score" message to forge — the mod reports events, and the server
decides what they are worth.

Beyond that, catlog only notices things the game itself tells it about: a debug window that was used,
a value that was edited, a teleport command, a refuel from the console. A flight where one of those
happened scores nothing and never appears publicly. That is the whole of it. There is no system here
trying to infer from the *shape* of your play whether you are honest — that would be a lot of
machinery, aimed at a cat game, and it would eventually be wrong about somebody who was just good.

Ordinary play can never trip any of this. Reloading a save is not cheating and is not treated as
such — if you load an earlier save, the time-to-milestone boards simply note it next to your row,
and rank you normally.

The honest version: someone determined enough to modify the mod itself can put a fake number on a
leaderboard. We know. It is a game about cats in space, and the complexity is better spent elsewhere.

---

## What it costs to run

One small binary and one embedded database on one cheap VPS, run by one person for fun. The public
pages are cacheable, so a busy day costs a CDN's bandwidth rather than money. That constraint is
deliberate — the way a hobby project dies is not a crash, it is a monthly invoice that makes the
owner turn it off.

---

## Everything else

catlog is open source. If you want to know how it works, run it yourself, or change something:

- **[DEVELOPMENT.md](DEVELOPMENT.md)** — build it, run it, test it.
- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — what the pieces are and how they fit.
- **[docs/CONSTITUTION.md](docs/CONSTITUTION.md)** — the principles the rest follows from.
- **[docs/DECISIONS.md](docs/DECISIONS.md)** — every decision, and why.

Licensed under the terms in [LICENSE](LICENSE).
