# nomenclature

- catlog: the name of this repo, mod and leaderboard website.  this is NOT a typo, it's a portmanteau of "cat" and "logs" that is intentionally meant to sound like "catalog"
- KSA: short for Kitten Space Agency, a space flight simulation game
- player: a human playing KSA with the catlog mod installed for telemetry


# goals

catlog will be an ecosystem of tools, APIs and websites which will provide passive telemetry collection from KSA via a C# code base game mod

the APIs will record the data in a global datastore and make read views of the data available via APIs and websites

A website and possibly APIs will be made available to view the data as leaderboards or raw data


# features

- OAuth2/OIDC login for a player on a website via blessed IdPs
- self-service function which produces public asymmetric encryption material that they configure to use for auth from the players ksa mod to the catlog ingestion APIs
- the self-service JWT/JWS or x.509 cert (whatever we choose) will let the user supply a pseudonym (a username/handle) that will be embedded in the encrypted public component.  this will be the only publicly identifiable information for this user stored in DBs and exposed on APIs to fetch the stats data.
- when content curation must occur (ban a user), all data linked to their email must be deleted.  we can do this by something like having the PK of a user be the lowercase version of their email sha-256 hashed, and all their data is linked to this hash.  it must be trivial to delete all data associated with a use in the event of system abuse of any kind.


# stories

these are some stories of how the mod and APIs and website will be used

- user visits catlog website to sign up.  they login to a webapp using the Discord IdP, they see a page which allows them to see their handles they have generated a JWT/JWS or X.509 cert (TBD which) for before
  - we do not store these so they must save them locally once generated.
  - if they have none, a simple button/wizard is available to create a handle
  - if they have handles already, show a list with controls to generate new public JWS/JWT or X.509's easily for the same handle


# data to collect

## events

- vehicle state change (landed, in atmosphere, orbit, soi change)
- RUDs (rapid unscheduled disassembly, e.g. vehicle destroyed) - if we can differentiate the reasons why, that's better.  the current build supports vehicle destruction by contact with the ground, water or excessive aerodynamic forces
- community suggestions - TBD how we would have the data to detect these kinds of high level events but ideally if we have the underlying data that is best
     - May I suggest the biggest lithobrake record (fastest impact where science/kitten survived, if possible) lol
     - Ah cool, I have one, since cats always land on their feet, do the opposite, a stat of most amount of times a kitten did in fact NOT land on their feet (while doing EVA on a planet or something)

## passive

- acceleration
- velocity


# security considerations

- KYC (know your client) is impractible, we will only be able to support trusting chosen well-known IdPs for authentication
- never expose the players identity (email)
- never store the players identity (email) - one-way hash it as the storage key.  this provides a method to identify data from a given player is needed for content moderation and abuse purging and banning from the service.


# technology proposals and ideas (NOT final choices)

- login will be via Discord IdP using OAuth2/OIDC flow
- events should be stored in a event store pattern for event sourcing.  we will generate aggregates and read projections from the event store to generate stats and have data to serve from APIs and provide a website to view
- once logged in, user can provide a handle (e.g. username) and generate the public part of a PKI setup used for client auth
- JWS/JWK could be the issued material for clients
- X.509 cert could be the issued material for clients
- mTLS could be used for hosted telemetry ingestion API if we issued X.509 client certs
- design as a distributed system
- treat the ingested data as a distributed log with a conflict free resolution for multi-server convergence.  the data is always isolated event logs for a given player so there is never a conflict with any other data, only ordering is a problem for data convergence, and even the chosen order does not matter
- datastar for browser ui
- SQLite or some variant (Cloudflare D1, Turso, something else) might make sense for the DB as both the event store and read projects/aggregate roots
  - Strong preference for trying out Turso, see the turso-db skill
- should be globally distributed if possible
- Cloudflare as host/distribution for global distribution?
- CDN for global distribution?  For this, perhaps a well-known log index file is maintained on the CDN which links to immutable, ever-growing list of data chunks.  This has a growth problem for consumers, there likely needs to be a compaction process on both the server for its complete immutable dataset and the client side reconciliation
- go and/or rust for backend to provide APIs and other backend processes as needed
- possibly use Digital Ocean low cost VPS server



# non functional goals

- must be very inexpensive to run
- must be extremely efficient for data ingress, processing, egress (queried via website, apis)
- must be very efficient for players
- strongly consider self-hosting on a simple linux VM VPS host - i have the skills and know-how to do this.  i am OK with engineering and building all my own backends and DBs to be ultra efficient.
