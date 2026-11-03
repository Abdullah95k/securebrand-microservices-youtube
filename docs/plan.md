# SecureBrand YouTube collector: plan v4 (draft for approval)

Status: **approved on 1 November 2026.** Implementation follows §10, starting with the Phase 0 kit.
Last updated: 1 November 2026. All times are **Iraq time (UTC+3)**.

History:

- **v1:** first draft.
- **v2:** scraping for collection, API for search.
- **v3:** coverage of every Iraq-relevant channel, IP failover, dynamic keywords, 365-day backfill.
- **v4 (this version):** keep all data with full identities, commenter enrichment, compressed archive, server recommendation,
  cost-optimized configuration.

Legend: **[verify]** marks a fact I could not check against a primary source. Google's developer site is blocked from my build sandbox,
and YouTube's internal website endpoints are undocumented. Phase 0 (§9) confirms or rejects every [verify] item before we build on it.

---

## 0. Decisions so far, and the short version

### 0.1 Your decisions

| # | Date | Decision |
|---|---|---|
| D1 | 1 Nov 2026 | Scope: channels relevant to Iraq and the Iraqi audience |
| D2 | 1 Nov 2026 | No client-owned channels: no owner logins, no Analytics API |
| D3 | 1 Nov 2026 | Discovery by searching all of YouTube for keywords; channels found are added to the list |
| D4 | 1 Nov 2026 | Scraping (logged out) for the listed channels and their content; the official Data API for search |
| D5 | 1 Nov 2026 | As optimized as possible while keeping the highest quality, stability, data volume, speed and accuracy |
| D6 | 1 Nov 2026 | No reuse of the Facebook project ("fbs") for now |
| **D7** | 1 Nov 2026 | **Proxy IP pool, with failover:** when one IP is blocked, the work continues on another IP |
| **D8** | 1 Nov 2026 | **Both search methods:** official API search **and** scraped YouTube search |
| **D9** | 1 Nov 2026 | **Keywords and languages are dynamic:** changeable at any time, no code changes |
| **D10** | 1 Nov 2026 | **Every channel that posted anything mentioning Iraq** (or something Iraq-relevant) goes on the list. No minimum size or activity |
| **D11** | 1 Nov 2026 | **Coverage as large as realistically possible**; final size not fixed yet |
| **D12** | 1 Nov 2026 | **Backfill 365 days** for each new channel |
| **D13** | 1 Nov 2026 | A one-time official-API accuracy check in Phase 0 is fine |
| D14 | 1 Nov 2026 | Core vs mention split approved |
| D15 | 1 Nov 2026 | Keyword suggestions are switched on automatically |
| D16 | 1 Nov 2026 | The seed keyword list is approved as a start |
| D17 | 1 Nov 2026 | "Mentioning Iraq" = a keyword match in the title, description, tags, hashtags or post text of something the channel itself posted |
| **D18** | 1 Nov 2026 | **Keep everything; no automatic deletion. Store full commenter names, IDs and every other public detail we can collect** |
| D19 | 1 Nov 2026 | Servers: my recommendation (fbs uses DigitalOcean) → §7.2 |
| D20 | 1 Nov 2026 | Outgoing data and alerts: mostly webhooks, designed later |
| D21 | 1 Nov 2026 | Budget: as affordable as possible without giving up data, accuracy, speed or stability → §7.4–7.5 |
| D22 | 1 Nov 2026 | Legal review done: a lawyer has signed off on the approach |
| D23 | 1 Nov 2026 | Push-notification subdomain: later. Until then, new uploads are detected by RSS and channel-tab checks |
| D24 | 1 Nov 2026 | No live chat |
| D25 | 2 Nov 2026 | E11: the 300-title sample is labelled by AI (only the public titles are sent to the AI provider), with an optional hand spot check of about 30 titles to measure how far to trust the AI |
| D26 | 3 Nov 2026 | **All data is kept forever**, Phase 0 included: no time limits. Two things still remove data: an erasure request (with its suppression entry), and YouTube's API rule that data from the official API may be kept at most 30 days (the same items scraped from the website are kept) |

Your non-negotiable rules still apply:

- **public data only, never log in**;
- **never solve or get around bot checks or CAPTCHAs**, never generate YouTube's anti-bot tokens, and keep pacing;
- erasure requests (by author and by channel) with a suppression list, so erased data never comes back. D18 switches off automatic
  deletion and pseudonymization by default; both remain available as settings;
- credentials only in files or environment variables;
- no real data in the public repo.

D7 changes how "back off when blocked" works: it now means **the blocked IP backs off**, not the whole system. See §3.

### 0.2 The short version

- **Coverage model (D14).** Every channel with at least one Iraq-relevant video, Short or post is on the list.
  - **Core channels** (mostly Iraq-focused) are collected fully.
  - **Mention channels** are watched for new uploads, and only their Iraq-relevant items are collected in full.
- **Discovery** runs all the time, from four sources:
  - the official search API (≈ 100 searches/day);
  - scraped YouTube search (no quota);
  - the "channel graph": channels that listed channels mention, feature or collaborate with;
  - **commenters who upload videos themselves**.

  Keywords and languages are edited live. New keywords are suggested and switched on automatically (D15).
- **Everything is kept (D18):**
  - no automatic deletion;
  - full commenter names, handles, IDs, avatars and badges;
  - comments their authors later delete, marked as deleted;
  - the public details on commenters' own channel pages, read only when there is spare capacity.

  Data older than 12 months moves to a **compressed archive**, so storage stays cheap and the data stays searchable. **Erasure requests
  and the suppression list still work:** that was in your original rules, and the law requires it when someone asks.
- **Servers (D19):**
  - production runs on **Hetzner dedicated servers**. For this storage-heavy, always-on job they cost roughly 55–65% less than equivalent
    DigitalOcean machines, and include unmetered traffic;
  - Phase 0 runs on one small DigitalOcean droplet in your existing account.
- **Scale and cost** (cost-optimized configuration, §7):

  | Channels on the list | Requests/day after backfill | Proxy IPs | Monthly cost |
  |---|---|---|---|
  | 20,000 | ~300,000 | 20–50 | **~$160–250** |
  | 100,000 | ~1.5 million | 80–250 | **~$500–1,150** |
  | 300,000 | ~4.4 million | 250–700 | **~$1,550–3,300** |

  The system grows coverage by itself. When capacity is short, it checks the least active channels less often instead of dropping any, and
  tells you what to add.
- **365-day backfill:** about 14 million requests at 20,000 channels and about 70 million at 100,000, so roughly a month with extra
  temporary IPs. The biggest and most relevant channels go first.
- **Unchanged from earlier versions:**
  - pinned/hearted comments, community posts and About links are collected;
  - comment times are relative ("3 days ago"); frequent checks make them precise for new comments;
  - large like counts are abbreviated;
  - video/audio files are out of scope, and transcripts probably are too.
- **Legal risk, which I recommend a lawyer reviews before production (§4):**
  - YouTube's Terms forbid automated access and collecting usernames;
  - switching IPs after a block (D7) reads as deliberate evasion;
  - keeping everything forever with full identities, including deleted comments (D18), is the highest-risk privacy setup under GDPR for
    commenters living in the EU.

---

## 1. Remaining questions

All questions are answered (D14–D24). The only item left for later is the **subdomain for push notifications** (D23). Until it exists,
new uploads are detected by RSS and channel-tab checks.

Defaults I've set for D18. Any of them can be changed at any time:

| Setting | Default |
|---|---|
| Commenter enrichment (reading commenters' own channel pages) | Every commenter, using spare capacity only; most active first. Refreshed every 6 months for active commenters, yearly for the rest |
| Avatar images | Stored for channels and commenters, small size, each image stored once |
| Comments deleted by their authors | Kept, marked deleted with the time |
| Pseudonymized exports and webhook payloads (codes instead of names) | Available, switched off |
| Time-based retention | Available, switched off |

### Assumptions used until Phase 0 measures them

| Topic | Assumption |
|---|---|
| Channels on the list | Modelled at **20,000 / 100,000 / 300,000** |
| Core vs mention | 50% core, 50% mention |
| Core channel size mix | 75% small (≈ 30 videos, ≈ 1 upload/month, ≈ 10 comments/video; about half of them dormant)<br>22% medium (≈ 300 videos, ≈ 15 uploads/month, ≈ 100 comments/video)<br>3% large (≈ 2,000 videos, ≈ 40 uploads/month, ≈ 500 comments/video) |
| Mention channels | ≈ 1 Iraq-relevant item per month; uploads of other items are only noted, not collected |
| Retention | **Everything kept indefinitely** (D18). Data older than 12 months moves to the compressed archive |
| Media | Thumbnails, avatars (channels and commenters), banners |

---

## 2. What we'll collect, from where, and how accurate it is

Sources:

- **Web** = YouTube's internal website endpoints, logged out. These are `youtubei/v1/browse`, `/next`, `/player`, `/search` and the watch
  page; together they are called "InnerTube".
- **RSS / WebSub** = public channel feeds and push notifications.
- **API** = the official Data API, used for search (plus the one-time Phase 0 accuracy check).

Collection scope by channel type:

| Data | Core channels | Mention channels |
|---|---|---|
| Channel info + snapshots | ✅ | ✅ |
| List of all uploads | ✅ full details | ✅ basic only: title, time, views from RSS/listings |
| Video details, stats over time, comments | ✅ every video in the window | ✅ **Iraq-relevant items only** |
| Community posts + their comments | ✅ all | ✅ Iraq-relevant posts only |
| Playlists | ✅ | Only playlists with Iraq-relevant titles |

### Channel

| Field | Source | Accuracy / notes |
|---|---|---|
| Name, handle, description, country, join date | Web (channel page + About panel) | Exact |
| Links on the About page | Web (About panel) | Exact |
| Subscriber count | Web | Abbreviated ("1.23M"), as the website shows. The official API is also rounded to 3 digits |
| Total views, video count | Web (About panel) | Exact [verify] |
| Avatar, banner | Web → image CDN | Exact |
| Featured / linked channels | Web (Channels tab, About) | Exact; also feeds discovery |
| Change history | Our snapshots from day 1 | No history before day 1 |

### Videos, Shorts, live streams

| Field | Source | Accuracy / notes |
|---|---|---|
| Title, description, channel | Web; RSS/WebSub for new uploads | Exact |
| Publish time | RSS/WebSub (exact, new uploads); Web player data (date or date-time [verify]) | Exact for new videos; possibly date-only for old ones |
| Duration | Web | Exact |
| Tags, category | Web (**player data only**) | Only if the player endpoint answers without a bot check [verify, critical]. If not, these fields are marked "unavailable" |
| Views | Web ("12,345 views"); RSS (latest 15 videos) | Exact |
| Likes | Web (like button label); RSS (latest 15) | Exact [verify] |
| Comment count | Web (comments header) | Exact or abbreviated [verify] |
| Shorts / long video / live | Web (separate Videos / Shorts / Live tabs) | Exact |
| Live status, schedule, live viewers | Web | Viewer count every 5–15 min while live |
| Chapters (creator-written and automatic) | Web | Exact |
| Thumbnails | Image CDN | Exact |
| Transcripts | Web | **Probably not possible within your rules** (2026 transcript endpoints need anti-bot tokens). Phase 0 checks honestly |
| Video/audio files | — | **Out of scope** (anti-bot tokens; copyright) |

### Comments and replies

| Field | Source | Accuracy / notes |
|---|---|---|
| Text, author channel ID, author handle/name, avatar | Web | Exact; stored in full (D18) |
| Author badges: verified, channel member (and for how long), creator | Web | Exact |
| Commenter's own public channel page: name, handle, description, country (if shown), join date, links, subscriber and video counts, avatar, banner | Web (enrichment, §8.7a) | Exact; read with spare capacity, most active commenters first |
| Publish time | Web | **Relative** ("3 hours ago"). Precise for comments we first see soon after posting; coarse for backfilled ones ("8 months ago" ≈ ±2 weeks). Precision stored per comment |
| Likes | Web | Exact below 1,000, abbreviated above ("1.2K"): lower bound + text stored [verify] |
| Replies, reply count | Web | Exact |
| Pinned, hearted by creator, reply from creator | Web | Exact |
| Edited | Web ("(edited)" marker) | Exact; earlier versions we saw are kept |
| Deleted | Missing on a re-scan | Marked deleted with the time; **the text is kept** (D18). A setting can redact it instead |

### Community posts, playlists, new uploads

| What | Source | Notes |
|---|---|---|
| Community posts (text, images, polls, likes, comment count, comments) | Web (Posts tab) | Likes abbreviated; time relative |
| Playlists and items | Web | Exact |
| New uploads | WebSub push + RSS + channel tabs | §8.5 |
| Keyword search | API `search.list` + Web search | §5 |

---

## 3. How the scraper follows your rules (updated for D7)

| Your rule | What the scraper does |
|---|---|
| Public data only, never log in | Logged-out requests only. **No Google/YouTube accounts and no account cookies, ever.** Each IP keeps its own anonymous visitor ID, the one every logged-out browser receives. IDs are never shared between IPs. Nothing private, members-only or login-gated is collected |
| Never get around bot checks or CAPTCHAs | **Never** solve CAPTCHAs or use solving services. **Never** generate or obtain BotGuard / "proof-of-origin" tokens. **Never** use headless browsers to pass challenges, or pretend to be Googlebot. A challenged request is **not answered**; the IP that got it goes into cooldown. Endpoints that *require* anti-bot tokens (media streams, likely transcripts) are switched off |
| Pace requests | Every IP has its own slow budget (Phase 0 measures it; planning figure: one request every 1–3 s). Random gaps between requests, never bursts. Load is spread across IPs **and across subnets**, the groups of neighbouring addresses YouTube tends to judge together |
| Back off when blocked (with D7) | Covered by the failover rules below |
| Credentials never committed or logged | Proxy passwords and the API key are read from files named in environment variables (`*_FILE`). The logger redacts proxy URLs and auth headers |
| Public repo, no real data | Generated fixtures; real response *shapes* only as scrubbed skeletons, reviewed by a person before commit (§8.10) |

**Failover rules:**

1. **The blocked IP backs off.** It cools down for 30 min, then 2 h, then 12 h, then 24 h if blocks repeat. The counter resets after a
   clean day. An IP blocked on several days in a row is quarantined, and you get an alert to replace it.
2. **The work moves to another healthy IP** after a short random delay of 5–60 seconds. That is what you asked for. The new IP gets
   the job as a normal request, at its own normal pace.
3. **One job can't burn the pool.** If the same request gets blocked on 3 different IPs, it is parked for 6 hours. That pattern means that
   particular page or endpoint is protected, and trying more IPs would only get them all flagged.
4. **Health is tracked per IP and per endpoint type** (search, channel pages, video pages, player). An IP blocked for search can keep
   fetching channel pages.
5. **Subnet awareness.** If several IPs in the same subnet are blocked, the whole subnet cools down.
6. **Pool brake.** These protect the IPs you're paying for; YouTube flags whole subnets and providers:
   - if more than 20% of the pool is cooling within an hour, the overall speed drops by 30% and recovers slowly;
   - if more than 50% is cooling, everything pauses for 30–60 minutes and you get an alert.

Rules 3 and 6 go slightly beyond "use another one". I recommend keeping them because without them, one bad hour can get the entire pool
banned. Each rule is a setting, so it can be adjusted.

---

## 4. Legal and policy position

### 4.1 YouTube's Terms of Service (scraping, including scraped search)

YouTube's Terms forbid:

- **automated access** (except public search engines, or with written permission);
- **collecting information that might identify a person**, such as usernames, without permission.

Collecting comments with author identities does exactly that. **D7 (switching IPs after a block) is the kind of behaviour YouTube treats as
deliberate evasion.** That makes a legal complaint likelier and weakens any "we behaved like a normal visitor" argument. Possible
consequences:

- IP and subnet blocks;
- a breach-of-contract claim or cease-and-desist letter;
- loss of our Google Cloud project's API access.

This is your business decision (D4, D7). **I recommend a lawyer reviews it before production.**

### 4.2 The official API (search)

- **Quota:** 10,000 units/day. One `search.list` call costs 100 units, so **≈ 100 searches/day**, each up to 50 results. Quota resets at
  midnight US Pacific time: **11:00 Iraq time** until 13 March 2027, and **10:00 Iraq time** during US daylight saving (14 March –
  6 November 2027).
- **More quota needs a compliance audit,** and the API terms prohibit scraping [verify exact wording]. So we treat 10,000/day as a **hard
  ceiling**.
- No multiple projects to multiply quota: that is forbidden and enforced.
- **API data rules:** search results must be refreshed or deleted within 30 days. Search hits are kept 30 days at most. The one-time Phase 0
  accuracy check (D13) is deleted after the report.
- **The API key could be cut off** if YouTube links it to the scraping system. Discovery would then continue on scraped search and the
  channel graph (§5).

### 4.3 Personal data (D18)

- **"Public" does not take data out of data-protection law.** GDPR covers the personal data of people in the EU even when they posted it
  publicly, and the Iraqi diaspora in the EU is large.
- **D18 is the highest-risk combination:** no time limit, full identities, reading commenters' own channel pages, and keeping comments
  their authors deleted. The parts most likely to be challenged are keeping deleted comments and building commenter profiles.
- **What stays in place regardless:** erasure on request (by author, by channel, by item), with a suppression list so erased data never
  comes back. This was in your original rules, and it's what the law requires when someone asks.
- **What we still need:**
  - a legal basis: legitimate interest, with a written balancing test (a legitimate-interest assessment);
  - a data protection impact assessment (DPIA);
  - a public privacy notice (GDPR Article 14) saying what we collect and how to ask for erasure.
- **Built in but switched off:** retention limits and pseudonymization stay in the system as settings. If your lawyer requires them, turning
  them on is a configuration change, not a rebuild.
- **Reporting:** comments can reveal religion, politics or health, so reports should show aggregates rather than profiles of individuals.
- **Iraq:** as far as I know there is no comprehensive data protection law in force yet; please confirm with your lawyer.
- Details are in §8.9.

---

## 5. Discovery: finding every Iraq-relevant channel

### 5.1 Dynamic keywords and languages (D9)

**Storage:**

- keywords live in a database table, editable through the admin tool (later a small admin web page);
- **changes take effect within a minute, with no redeploy**;
- each keyword has: its spellings and variants, language(s), group, kind (place, person, brand, topic, hashtag), priority, status
  (active / paused / banned), source (seed / admin / suggested), and an optional expiry date;
- languages are also a table. Adding a language is a data change, not a code change.

**Arabic and Kurdish-aware matching.** Text is normalized before matching:

- Arabic: alef variants (أ إ آ → ا), taa marbuta (ة/ه), yaa (ى/ي), tatweel and diacritics removed;
- Kurdish: Sorani in Arabic script, Kurmanji in Latin script;
- common transliterations ("Iraq / Irak / Iraque / العراق / عێراق").

One fast multi-keyword matcher (Aho-Corasick) checks every title, description, tag, hashtag and post we collect.

**Seed list (N3):** Iraq and Iraqi, the 18 governorates and major cities, in each language. You add brands, people and topics whenever you
like.

**Suggestions (N2):**

- **Co-occurring terms:** the system mines hashtags, tags and frequent terms from content that already matched. Terms that keep appearing
  alongside Iraq keywords become suggestions.
- **Trending terms:** terms spiking in the last 24 hours compared with their usual level (news events) become **temporary high-priority
  keywords** with a 7-day expiry.
- Default: suggestions above a quality threshold are activated automatically, at most 20 per day, and you can ban any of them.

**False positives:** ambiguous words, such as a city name that is also a brand, are tracked. The system measures how often a keyword's
matches are confirmed by other Iraq signals and flags keywords with low precision for review.

### 5.2 Two search engines (D8)

| | Official API search | Scraped YouTube search |
|---|---|---|
| Budget | ≈ 100 calls/day (hard ceiling) | No quota; paced by the proxy pool |
| Results | Up to 50 per call | About 20 per page; follow pages |
| Filters | Exact date windows (`publishedAfter`), `regionCode=IQ`, `relevanceLanguage`, OR/NOT operators | "Upload date" (last hour / today / week / month / year), sort by upload date, type (video, Short, channel) |
| Use | **Precise scheduled sweeps** of the top-priority keyword groups, every 3 h, with exact time windows | **Breadth:** every active keyword on rotation (priority keywords hourly, the rest every few hours), newest first, stopping when results reach the last run's |
| Weak point | Small budget; the key could be cut off | Search pages are often the most protected endpoint; Phase 0 measures the block rate |

Both write "hits" (keyword → video → channel). Duplicates are merged.

### 5.3 The channel graph (free, from data we already scrape)

From every listed channel we extract:

- channels it **@mentions or links** in descriptions, community posts and About links;
- its **featured channels**;
- **collaborators** on joint videos.

Each of these becomes a candidate. **Commenters' channels** also become candidates when enrichment (§8.7a) finds they have uploads of
their own.

### 5.4 Candidate check → add to the list (D10: no size minimum)

1. **A new candidate gets a quick check:**
   - About panel + first page of videos, about 3 requests;
   - then a **channel-scoped search** for each active keyword group, which finds older Iraq-relevant items without reading the whole
     channel.
2. **The rule is simple:** if the channel itself has posted **at least one item matching an Iraq keyword**, it goes on the list. Size and
   activity don't matter.
3. **Classification:**
   - **Core:** country = Iraq, **or** at least 30% of recent items match, **or** strong Iraq signals (Kurdish Sorani content, Iraqi place
     names, Iraqi dialect markers in titles).
   - **Mention:** everyone else on the list.
   - Re-checked monthly. Mention channels move up to core if their share of Iraq content grows.
4. **No-match candidates** are remembered and re-checked after 90 days. They might post Iraq content later; also, WebSub tells us about any
   new upload from channels we follow, and §8.5 covers mention channels.

### 5.5 What "all channels possible" realistically means

- **New Iraq-related uploads** from now on will be found **well**, because frequent newest-first searches catch them as they appear.
- **Older content:** search engines only return a limited number of results per query [verify the depth in Phase 0], and the website search
  has no custom date ranges. We won't find **every** channel that ever mentioned Iraq years ago. The channel graph and many keyword
  variants close much of that gap.
- **Phase 0 measures discovery speed** (new qualifying channels per day) and how quickly it levels off, so we can estimate the final list
  size.

---

## 6. Volume and capacity model (estimates; Phase 0 replaces them)

### 6.1 Tiers

Tiers are assigned automatically and recomputed weekly, with manual overrides.

| Tier | Who | Approximate share of list |
|---|---|---|
| A | Large core channels, or the most relevant | ~1.5% |
| B | Medium core channels | ~11% |
| C | Small, active core channels | ~20% |
| D | Dormant core channels (no upload in 180 days) | ~17.5% |
| M | Mention channels | ~50% |

### 6.2 Steady requests per channel per day

| Work | A | B | C | D | M |
|---|---|---|---|---|---|
| New-upload detection (WebSub is free; RSS backup) | 48 | 12 | 4 | 1 | 1 |
| Channel snapshot (About panel) | 8 | 2 | 0.7 | 0.3 | 0.3 |
| New videos: details + comment scans + sweeps | ~250 | ~31 | ~0.8 | 0 | ~2 (relevant items only) |
| Late comments on older videos (monthly first page) | ~24 | ~6 | ~0.4 | ~0.1 | ~0.3 |
| Posts, playlists | ~1 | ~0.4 | ~0.2 | ~0.1 | ~0.1 |
| **Total** | **~330** | **~51** | **~6** | **~1.5** | **~3.5** |

Weighted, that is **≈ 25 requests/day per core channel** and **≈ 3.5 per mention channel**.

### 6.3 One-time backfill per new channel (365 days)

| Channel | Requests |
|---|---|
| Small core | ~40 |
| Medium core | ~1,500 |
| Large core | ~17,000 (480 videos × ~35, comment cap per video applies) |
| **Average core** | **≈ 875** |
| Mention (channel-scoped searches + Iraq-relevant items only) | **≈ 500** |

### 6.4 Scenarios

| | 20,000 channels | 100,000 channels | 300,000 channels |
|---|---|---|---|
| Steady requests/day (collection + discovery) | ~305,000 (≈ 3.5/s) | ~1.5 M (≈ 17/s) | ~4.4 M (≈ 51/s) |
| Total backfill (365 days) | ~14 M requests | ~69 M | ~206 M |
| Backfill time with extra capacity | ~30 days at +5/s | ~30 days at +25/s | ~40 days at +60/s |
| Proxy IPs during backfill (1 request per 1–3 s per IP, 70% use, +30% failover spare) | 20–50 | 80–250 | 250–700 |
| Proxy IPs after backfill | ~10–30 | ~40–130 | ~120–400 |
| Data transfer (≈ 40 KB compressed per request) | ~0.4 TB/month (+0.5 TB in the backfill month) | ~1.8 TB (+2.6 TB) | ~5.3 TB (+6 TB) |
| Search | API ≈ 100 calls/day + scraped search ≈ 10–50k requests/day | same | same |
| Commenter enrichment (spare capacity only) | ~+60–140k/day | ~+300–700k/day | ~+1–2 M/day |

Channels are discovered gradually, so in practice backfill is spread over the first months rather than done in one block.

### 6.5 Storage (nothing is deleted, D18)

| | 20,000 | 100,000 | 300,000 |
|---|---|---|---|
| Comments added per year | ~140 M rows (~100 GB) | ~700 M (~500 GB) | ~2.1 B (~1.5 TB) |
| Commenter profiles | ~20 M (~20 GB) | ~80 M (~80 GB) | ~200 M (~200 GB) |
| Videos, channels, posts | ~20 GB | ~100 GB | ~300 GB |
| Snapshots per year | ~10 GB | ~45 GB | ~135 GB |
| **Database after year 1** | **~150 GB** | **~750 GB** | **~2.2 TB** |
| After 3 years, all in Postgres | ~450 GB | ~2.2 TB | ~6.5 TB |
| After 3 years, with the archive (older than 12 months compressed ~5–8×) | ~170 GB + ~60 GB archive | ~800 GB + ~300 GB | ~2.3 TB + ~900 GB |
| Object storage per year (thumbnails, avatars, debug samples) | ~150–200 GB | ~0.7–1 TB | ~2 TB |

**How "keep everything" stays affordable:**

- whole monthly partitions older than 12 months move from Postgres into **compressed columnar storage**: ClickHouse on the same server, or
  Parquet files in object storage;
- archived data stays searchable. Queries are slower, but fine for history and reports.

### 6.6 Coverage first, freshness flexes (D11)

The **capacity planner** works out every hour:

- **supply:** healthy IPs × pace × target use;
- **demand:** channels × their tier schedules.

When demand is bigger than supply:

- **nothing is dropped from the list**;
- new-upload detection and new-video details always keep running;
- the planner **stretches check intervals**, long tail first: D, then M, then C;
- backfill slows down;
- the dashboard shows **"freshness debt"**, with the number of IPs or servers that would remove it.

You decide when to buy more; the system never stops covering.

---

## 7. What you need to provide, and the monthly cost

### 7.1 Requirements list

| # | Item | Needed for | Notes |
|---|---|---|---|
| 1 | **Google Cloud project** + **YouTube Data API v3** + **API key** (restricted to that API and the server IP) | API search, Phase 0 accuracy check | Free. Steps in §9.3 |
| 2 | **Phase 0 server:** one DigitalOcean droplet in your existing account | Phase 0 (2–3 weeks) | 4 vCPU / 8 GB / 160 GB, about $48–56/month; shut down after Phase 0, once its data is copied to production (D26) |
| 3 | **Production servers:** Hetzner dedicated (§7.2) | Everything after Phase 0 | Start with one server; add more as coverage grows |
| 4 | **Proxies:** static datacenter IPv4 (main pool) + a smaller set of ISP IPs, from 2–3 providers, many subnets, outside the EU | Scraping | Monthly plans, bought in batches as coverage grows (§7.3) |
| 5 | Optional **residential allowance** | Only if Phase 0 shows an endpoint fails on both datacenter and ISP IPs | Off by default; consent-based sourcing only; capped |
| 6 | **Subdomain** for push notifications | WebSub | Let's Encrypt certificate (free) |
| 7 | **Object storage** | Thumbnails, avatars, archive files, debug samples | Hetzner Object Storage or DigitalOcean Spaces |
| 8 | **Offsite backups** | Production | Hetzner Storage Box or Spaces, 30-day rolling |
| 9 | **Webhook endpoint(s)** (later, D20) | Events and alerts | Your receiving URL + a shared secret, kept in a file |
| 10 | **Legal review**, privacy notice, legitimate-interest assessment, DPIA | Before production | Legal work |
| 11 | **Scraper maintenance time** | Ongoing | A few fixes a month (§8.11) |

### 7.2 Server recommendation (D19)

| | Hetzner dedicated (recommended) | DigitalOcean (what fbs uses) |
|---|---|---|
| Price for this workload | **Lower:**<br>AX42 (8 cores, 64 GB RAM, 2 × 512 GB NVMe) ≈ €97/month<br>AX102 (16 cores, 128 GB, 2 × 1.92 TB NVMe) ≈ €257/month<br>One-time setup fee €49–129 [verify at order] | **Higher:**<br>8 dedicated vCPU / 32 GB ≈ $252/month<br>16 vCPU / 64 GB ≈ $504/month<br>Block storage $0.10/GB-month (2 TB = $200/month) |
| Disk | Fast local NVMe included. This matters most, because the database grows every year | Mostly paid add-on block storage |
| Traffic | Unmetered (1 Gbit/s) | Allowance per droplet, then paid |
| Stability | Real hardware with mirrored (RAID1) disks; Hetzner replaces failed parts. We add a live replica from about 100,000 channels | Easy snapshots and resizing; managed services available |
| Effort | We manage the operating system (Docker, automatic updates, monitoring) | Similar; slightly easier tooling; same account as fbs |
| Price history | Raised prices twice in 2026 (April and June); still clearly cheaper for this job | Stable |

**Why Hetzner:**

- this job is **disk-heavy** (terabytes over time) and **always on**. That is where dedicated servers beat cloud servers by the widest
  margin: about 55–65% cheaper here for the same capacity;
- the scraping traffic goes through proxies, so the server provider's IP reputation doesn't matter;
- Hetzner's dedicated servers are in Germany and Finland. That's fine, because distance to YouTube doesn't matter.

**Staying on DigitalOcean** works too, at roughly twice the server cost. It's only the better choice if having one provider with fbs matters
more than the saving. Webhooks (D20) connect the two systems either way.

**Fallback:** OVHcloud dedicated servers (Advance range, from about €115/month) if Hetzner's stock or prices change.

**Growth path:**

| Stage | Servers | ≈ Monthly |
|---|---|---|
| Phase 0 | 1 DigitalOcean droplet (4 vCPU / 8 GB) | ~$50 |
| Start, up to ~30,000 channels | 1 × AX42-class: everything on one machine | ~€97 (~$110) |
| ~30,000–100,000 | 1 × AX102-class database + 1 × AX42-class for workers. At ~100,000, a second AX102 as a live replica | ~€355–610 (~$400–700) |
| ~300,000 | Database primary + replica on 48-core / 256 GB machines, an archive server, 2–3 worker servers | ~€1,100–1,700 (~$1,250–1,950) [verify larger-server prices] |

Moving up a stage is a planned migration: copy the database to the new machine, then switch over, with only minutes of downtime.

### 7.3 Proxies

- **Main pool:** static datacenter IPv4, roughly $0.50–1.50 per IP per month at volume, with unlimited or high bandwidth. We move terabytes
  per month, so the fair-use limits matter.
- **Second pool:** ISP proxies, roughly $1.25–2.50 per IP. These are IPs registered to consumer internet providers, which are harder to
  block. We use them for endpoints that Phase 0 shows block datacenter IPs.
- **Buying:** 2–3 providers, many subnets, outside the EU, monthly terms so bad IPs can be replaced.
- **Phase 0 first:** we test about 10 IPs from 2 providers before buying in bulk.

### 7.4 Monthly cost, cost-optimized configuration

USD at about $1.15 per €. Figures are for the backfill period; costs drop after it.

| Item | 20,000 channels | 100,000 channels | 300,000 channels |
|---|---|---|---|
| YouTube Data API | $0 | $0 | $0 |
| Servers (Hetzner, §7.2) | ~$110 (AX42) | $400–700 | $1,250–1,950 |
| Proxies (mostly datacenter, some ISP) | $30–85 | $60–375 | $200–1,100 |
| Object storage + backups | $15–40 | $40–80 | $100–250 |
| Domain/TLS, monitoring (self-hosted) | $0 | $0 | $0 |
| **Total** | **~$160–250** | **~$500–1,150** | **~$1,550–3,300** |

One-time costs: Hetzner setup fees (€49–129 per server), and Phase 0 (about $50 for the droplet + $20–30 for test proxies).

### 7.5 What keeps it affordable

| Lever | Saving |
|---|---|
| Core vs mention split (D14) | Skips thousands of unrelated videos from big international channels: 2–5× less work |
| Push notifications + RSS for new uploads | Detection costs almost nothing per channel |
| Newest-first comment scans that stop at known comments | Most scans cost 1 request |
| Tiers + capacity planner | Quiet channels are checked rarely; you add capacity only when freshness debt says so |
| Datacenter proxies first, ISP where needed, residential off | Residential per-GB pricing would cost 10–20× more at our volume |
| Dedicated servers with local NVMe | ~55–65% less than cloud servers for this workload |
| Compressed archive for data older than 12 months | Stops "nothing deleted" from multiplying disk cost |
| No raw-response storage (failures and a 1% sample only) | Saves terabytes |
| Commenter enrichment on spare capacity only | Never slows down fresh data |
| Phase 0 before buying | IPs and servers are bought for measured numbers, not guesses |

---

## 8. Architecture

### 8.1 Overview

```
 keywords + languages (DB, editable live) ◄── suggestions (trending / co-occurring terms)
        │
        ▼
 search-runner ── API search (≤100/day) ──┐
               └─ scraped search ─────────┤ hits          channel graph (mentions, featured, collabs)
                                          ▼                         │
                                   discovery: candidate check → match? → core / mention ──► registry (list, tiers)
                                                                                               │
 WebSub hub ──POST──► websub-receiver ─┐                                                       ▼
 RSS feeds ◄────────── rss-poller ─────┼── "check now" ──► scheduler ◄── capacity planner (supply vs demand)
                                       │                     │ due jobs (P0–P3)
                                       │                     ▼
                                       │              scraper-fetcher ──► proxy-manager ──► YouTube website endpoints
                                       │                     │            (per-IP budgets, cooldowns, failover,       + image CDN
                                       │                     ▼             subnet health, pool brake)
                                       │                  parser (type-based, fill-rate drift detection)
                                       │                     │
                                       │                     ▼
                     privacy ◄── suppression check ──► writer (upsert, snapshots, diffs, keyword matcher) ──► Postgres, S3
                                                             │
                                                             ▼
                                                   events → dashboards / analysis (later)
```

### 8.2 Services

All services are written in Go. One repo, several binaries, deployed with Docker Compose; services can be spread over servers as
coverage grows.

| Service | Job |
|---|---|
| **keywords** | Keyword and language tables, normalization rules, admin changes applied live, suggestion engine (co-occurrence, trends), precision tracking |
| **search-runner** | API search under the **quota governor**: 100 units charged per call before sending, a hard daily ceiling, reset at 11:00 Iraq time in winter and 10:00 in summer. Also scraped search on rotation |
| **discovery** | Candidates from search hits and the channel graph; candidate check (About + channel-scoped search); core/mention classification; re-checks |
| **registry** | The list: channel IDs, type (core/mention), tier, status (candidate / listed / dormant / no-match / suppressed), backfill progress |
| **capacity planner** | Hourly supply vs demand; stretches long-tail intervals when short; reports freshness debt and recommended IPs/servers |
| **scheduler** | A "next check time" per channel, video, comment stream and post. Priority queues: P0 new uploads and new-video details, P1 new comments, P2 refreshes and sweeps, P3 backfill and candidate checks, P4 commenter enrichment |
| **proxy-manager** | IP pool, health per IP **and per endpoint type**, subnet grouping, per-IP budgets, cooldowns, failover, the 3-IP park rule, the pool brake (§3). Credentials loaded from a file and never logged |
| **scraper-fetcher** | InnerTube client: logged-out web client (`hl=ar`, `gl=IQ`), continuations, image CDN, client version auto-detected daily. Raw responses kept only for parse failures and a 1% sample (3 days) |
| **parser** | Versioned, type-based parsers; validates fields and reports **field fill rates** (§8.11) |
| **writer** | Upserts, snapshots, change detection (edits, deletions, title/description changes), runs the **keyword matcher** on all new text, drops suppressed items **before writing** |
| **websub-receiver** | Public HTTPS endpoint; hub verification, HMAC signature checks, lease renewals |
| **rss-poller** | Tier-interval feed checks with conditional requests; exact stats for the latest 15 videos |
| **privacy** | Erasure requests, suppression list, erasure audit log. Also optional retention rules and pseudonymized exports (switched off, D18) |
| **enrichment** | Reads commenters' public channel pages using spare capacity (P4), most active commenters first; refreshes every 6–12 months |
| **archiver** | Moves monthly partitions older than 12 months from Postgres into compressed archive storage (ClickHouse or Parquet), still searchable |
| **webhooks** (later, D20) | Outgoing events and alerts, signed with HMAC, with retries and a dead-letter queue |
| **media** | Thumbnails, avatars, banners |
| **admin CLI** (later a small web page) | Keywords, channels, tiers, review of flagged keywords, erasure requests, health |

**No Kafka.** Up to ~50 requests per second is comfortable with a Postgres job queue (`FOR UPDATE SKIP LOCKED`), with the queue tables split
by priority. **Valkey** holds per-IP rate counters, cooldowns and health, shared by all fetcher processes. The archiver stores data
older than 12 months in **ClickHouse** or **Parquet** files. Phase 0 and the first months of data decide which.

### 8.3 Storage (Postgres + S3)

- **Main tables:** `channels` (type, tier, country, links), `videos` (kind; `relevant` flag + matched keywords), `comments`,
  `comment_versions`, `authors`, `author_profiles` (enrichment), `author_badges`, `posts`, `post_comments`, `playlists`,
  `playlist_items`.
- **Time series:** `channel_snapshots`, `video_snapshots`, `post_snapshots`, partitioned by month.
- **Discovery:** `keywords`, `keyword_variants`, `languages`, `keyword_suggestions`, `search_hits` (API hits deleted after 30 days),
  `candidates`, `channel_links_graph`.
- **Privacy:** `suppression_list`, `erasure_requests`, `erasure_audit`.
- **Operations:** `jobs_p0`…`jobs_p3`, `entity_schedule`, `quota_ledger`, `proxy_health` (no credentials), `parse_failures`,
  `field_fill_rates`, `capacity_reports`.
- **Accuracy columns:**
  - `published_at_est` + `published_at_precision_s`;
  - `like_count_min` + `like_count_text`;
  - `source` on every row (`web`, `web_search`, `rss`, `websub`, `api_search`).
- **Large tables** (comments, snapshots, author profiles) are partitioned by month, so the archiver can move whole old partitions cheaply.

### 8.4 How each collection works

- **Channel:** channel page + About panel (country, links, join date, totals) + Channels tab, at the tier interval.
- **Upload list:**
  - core channels: the Videos, Shorts and Live tabs, about 30 items per page [verify], following continuations;
  - mention channels: RSS/WebSub for new uploads; history comes only from channel-scoped keyword searches.
- **Video details:** watch page (page data + player data in one request) or `/next` + `/player`; Phase 0 picks. If player data is refused,
  tags and category are marked "unavailable".
- **Comments:** §8.7. **Posts:** Posts tab, then comments of new (or, for mention channels, relevant) posts.
- **Live:** scheduled streams are caught via the Live tab and WebSub. While live, viewers are read every 5 min (tiers A/B) or 15 min
  (others).

### 8.5 New-upload detection

1. **WebSub push** for every listed channel, core and mention. The video is fetched immediately. For mention channels the title and
   description are matched first, and only relevant items get full collection.
2. **RSS** at tier intervals (30 min for A, then 2 h, 6 h, 24 h, 24 h).
3. **Channel tab check** daily for A/B, weekly for C, monthly for D. It catches misses, premieres, and videos made public later.

We measure detection delay per layer. WebSub at 100,000+ subscriptions has not been tested at that scale [verify the hub's limits]. If the
hub limits us, RSS intervals are tightened automatically.

### 8.6 How often things are re-checked (defaults; the capacity planner can stretch C, D and M)

| What | A | B | C | D | M (relevant items) |
|---|---|---|---|---|---|
| New upload detected | minutes (push) / ≤ 30 min | minutes / ≤ 2 h | minutes / ≤ 6 h | minutes / ≤ 24 h | minutes / ≤ 24 h |
| New-video stats, first 48 h | 30 min | 2 h | 6 h | 6 h | 2 h |
| Video stats, 2–30 days | 6 h | 12 h | 24 h | 24 h | 12 h |
| Video stats, 30 days – 1 year | weekly | weekly | monthly | monthly | weekly |
| Video stats, older | monthly | monthly | quarterly | quarterly | monthly |
| New comments, 0–48 h | 1 h | 3 h | 6 h | 6 h | 3 h |
| New comments, 2–7 days | 6 h | 12 h | 24 h | 24 h | 12 h |
| New comments, 7–30 days | 24 h | 48 h | weekly | weekly | 48 h |
| Late comments, 30–365 days | monthly | monthly | quarterly | quarterly | monthly |
| Full comment sweep | 7 + 30 days | 7 + 30 days | 30 days | 30 days | 7 + 30 days |
| Channel snapshot | 6 h | 24 h | 3 days | weekly | weekly |
| Community posts | 24 h | 3 days | weekly | monthly | weekly (relevant only) |

### 8.7 Keeping comments complete and up to date

1. **Open each video's comment section** and switch to **"Newest first"**.
2. **Read top-level comments**, about 20 per page. For each we record:
   - ID, text, author ID, handle/name, avatar;
   - the relative time;
   - likes;
   - reply count;
   - pinned, hearted, author-is-creator;
   - the "(edited)" marker.
3. **Read replies**, about 10 per page, following continuations.
4. **Backfill (365 days):** every video in the window, newest videos first. Each video gets a full read up to a **cap** (default 20,000
   comments per video, newest first). Capped videos are flagged "partial".
5. **New-comment scans:** read newest-first and **stop after the first page that is entirely known** (plus one page of overlap). Usually
   1 request. When a thread's reply count has grown, we re-fetch its replies.
6. **Publish times:** a comment that says "25 minutes ago" when we first see it gets a near-exact time. Backfilled comments get coarse
   estimates, and the precision is stored.
7. **Edits:** changed text or a new "(edited)" marker → a new version is saved.
8. **Deletions:**
   - a full sweep that no longer finds a known comment marks it missing;
   - a second miss confirms it;
   - then it is marked deleted with the time. Its text and author data are **kept** (D18); a setting can switch this to redacting
     instead.
9. **Completeness per video:** collected ÷ the displayed comment count. Below 90% → re-sweep and alert.
10. **Limits we accept:**
    - the website has no channel-wide "all new comments" feed;
    - late comments on older videos are caught by monthly or quarterly first-page scans;
    - deletions on videos older than 30 days are caught only by those scans.

### 8.7a Commenter details (enrichment, D18)

- **Queue:** every new commenter (by channel ID) is queued.
- **What we read:** using **spare capacity only** (priority P4), the enrichment worker reads their public channel page and About panel:
  - name, handle, description;
  - country (if shown), join date;
  - links;
  - subscriber and video counts;
  - avatar and banner.
- **Order:** the most active commenters first (most comments on Iraq-relevant content), then everyone else.
- **Refresh:** every 6 months for active commenters, yearly for the rest.
- **Discovery link:** if a commenter's channel has uploads of its own, it goes through the candidate check (§5.4). A commenter who also
  posts Iraq-relevant videos becomes a listed channel (D10).
- **Cost:** about 2–3 requests per commenter. At 20,000 channels that is roughly +60–140k requests/day while catching up, and the capacity
  planner fits it into idle time.

### 8.8 Rate limiting, retries, stability

- **Rate limits:**
  - per IP: a token bucket at the measured pace, with random gaps;
  - per subnet and per endpoint type: caps;
  - per channel: one job at a time;
  - global: a speed multiplier driven by the pool brake.
- **Retries:**

  | Error | Action |
  |---|---|
  | Network errors, 5xx | Exponential backoff with jitter (2 s, 4 s, 8 s, 16 s, 32 s), at most 5 attempts |
  | Block signals (429/403, "confirm you're not a bot", the "sorry" page) | §3 failover rules |
  | Parse errors | Raw response kept 3 days, job moved to `parse_failures`, alert on a rising rate |
  | Video/channel gone | Re-checked once; if still gone, marked removed with the time; stored content kept (D18) |

- **Circuit breakers** per endpoint and for RSS (if more than 20% of feeds fail at once, RSS pauses and tab checks take over).
- **Kill switches** per endpoint and per feature (comments, posts, scraped search, discovery).
- **Safe re-runs:** all writes are upserts keyed by YouTube IDs.

### 8.9 Privacy: what is kept, erasure (D18)

**Stored in full:**

- commenters' channel ID, handle, display name, avatar (URL and image) and badges (verified, channel member and for how long, creator);
- every comment version we saw;
- comments their authors deleted, marked deleted;
- everything public on commenters' own channel pages, through enrichment (§8.7a).

**Retention defaults:**

| Data | Kept for |
|---|---|
| Comments, replies, versions, author data and profiles | **Indefinitely** (moved to the archive after 12 months) |
| Deleted comments | Indefinitely, marked deleted with the time |
| Snapshots (stats over time) | Indefinitely (archived after 12 months) |
| API search hits | 30 days (a YouTube API rule; it doesn't affect scraped data) |
| Raw debug responses | 3 days (operational only) |
| Logs | 14 days. IDs and counts only, never comment text or names, which keeps personal data and credentials out of log tools |
| Backups | 30 days rolling; erasures re-applied after any restore |

**Settings available but switched off:**

- time-based retention per data type;
- redacting deleted comments;
- pseudonymized exports and webhook payloads. These carry `author_key = HMAC-SHA256(secret, author_channel_id)` instead of names, so
  unique-author counts still work. Useful when sharing data with people who don't need identities.

**Erasure requests:**

| Kind | What happens |
|---|---|
| By author (URL, handle or ID) | Delete all their comments, replies, versions and author row. A suppression entry (keyed hash) drops their future comments **before** storage |
| By channel | Delete the channel, its videos, posts, the comments on them, snapshots and media. Suppress it, so **discovery can never re-add it** without an admin override with a recorded reason |
| By video / comment / post | Delete and suppress |

How an erasure is processed:

- primary stores cleaned within **72 hours**;
- debug samples age out in 3 days, backups in 30;
- every step logged in `erasure_audit`, without personal data;
- a daily job checks that suppressed items in the database = 0.

### 8.10 The repo is public: hygiene

- No captured responses, real IDs, names, comments or proxy details in the repo, ever.
- **Fixtures** come from a generator with fake IDs (for example `UCfake…`).
- **Response skeletons:** the `skeleton` tool replaces every value with a type placeholder and shortens arrays to one item. A person
  reviews each skeleton before commit.
- **CI on every pull request:**
  - gitleaks (secrets);
  - a fixture scanner that fails on real-looking YouTube IDs, emails, phone numbers, `AIza…` keys and `user:password@` proxy URLs.
- **`.gitignore`** blocks `.env*`, `*.key`, `secrets/`, `proxies*.txt` and data folders.
- The seed keyword list (public place names) can live in the repo. **Your own brand/person keywords are entered in the database, not
  committed**, because they reveal who you monitor.

### 8.11 Stability against website changes

- **Type-based parsing:** find objects by type anywhere in the response, instead of fixed paths that break when YouTube moves things.
- **Field fill rates:** for each field we track how often it is present. A drop raises an alert within minutes and pauses affected jobs
  rather than storing bad data.
- **Canary checks:** every 15 min a few public channels and videos are fetched and parsed. Only health metrics are kept.
- **Client version auto-update:** read from YouTube's homepage once a day.
- **Reference implementations:** when something breaks, compare with yt-dlp's (public domain) and YouTube.js's (MIT) current parsers.
- **Expected maintenance:** a few breakages a month, each usually a same-day parser fix shipped as a small pull request.

### 8.12 Testing: a fake YouTube

- **fake-youtube:** a Go server imitating the website endpoints (`browse`, `next`, `player`, `search`, watch page), RSS, the WebSub hub and
  the search API. It uses generated data and reproduces:
  - continuations and newest-first ordering;
  - relative times and abbreviated counts;
  - pinned and hearted comments, edits and deletions;
  - block responses per IP and per subnet;
  - layout changes;
  - search quota.
- **Unit tests:** parsers, Arabic/Kurdish normalization and matching, time and count estimates, scheduler, capacity planner, proxy
  manager (cooldown, failover, the 3-IP park rule, subnet cooldown, pool brake), quota governor, classification, pseudonymization,
  suppression.
- **Integration tests** (Docker Compose in CI), checking that:
  - a keyword added at runtime gets searched;
  - a matching channel gets listed and classified;
  - backfill runs;
  - a push notification leads to a fetch;
  - comment edits and deletions are detected;
  - a blocked IP fails over correctly;
  - the pool brake engages;
  - erased data never returns;
  - the API quota ceiling holds.
- **No CI test touches real YouTube.**

### 8.13 Monitoring

- **Metrics (Prometheus):**
  - requests by endpoint, IP, subnet and result; blocks per IP/endpoint/hour; IPs cooling or quarantined; failovers; parked jobs; pool
    brake state;
  - parse failures; field fill rates;
  - detection delay; freshness per tier; **freshness debt and recommended IPs**; backfill progress;
  - discovery: hits, candidates, listed (core/mention), keyword suggestions, keyword precision;
  - API quota used and its forecast to the reset;
  - comment completeness;
  - erasure request age; suppression violations (must be 0).
  - enrichment backlog; archive size and the age of the oldest partition still in Postgres;
  - disk use and its forecast (months until full).
- **Alerts:**
  - pool brake engaged;
  - IP quarantines;
  - a fill-rate drop or parse-failure spike;
  - freshness debt growing for 24 h;
  - WebSub renewals failing;
  - API quota exhausted early;
  - an erasure request older than 48 h;
  - any suppression violation.
- **Dashboards (Grafana):** scraping health, capacity and freshness, discovery and keywords, comments, privacy.

---

## 9. Phase 0: measure before building

**Goal:** measure the numbers that decide cost and design:

- the safe pace per IP and endpoint, and how failover behaves;
- which fields really come back, and how accurately;
- requests and bytes per item;
- comment completeness;
- **how many Iraq-relevant channels discovery finds, and what they look like**.

Everything stays within §3.

**Duration:** about **2–3 weeks**.

### 9.1 You provide

- **10–20 example channels** you already know (news, entertainment, Shorts-heavy, live streaming, Arabic and Kurdish);
- any **brand/person/topic keywords** for day 1, entered on the server, not in chat if they're sensitive;
- the API key file (§9.3);
- one small DigitalOcean droplet in your existing account: 4 vCPU / 8 GB, Ubuntu 24.04 LTS, Docker (about $50 for the month);
- **about 10 test proxy IPs from 2 providers, spread over at least 3 subnets**, so we can measure failover and subnet effects;
- optionally, a public HTTPS subdomain for push notifications.

### 9.2 I provide (1–2 small draft pull requests after approval)

- a `ytprobe` command-line tool;
- the seed keyword list with Arabic/Kurdish normalization;
- the minimal fake-youtube;
- the `skeleton` scrubber;
- a report template.

Raw data stays only on your server, in `/var/lib/securebrand-youtube/phase0`, and is kept (D26; official-API data 30 days only). The
report contains only aggregates.

### Experiments

| # | Experiment | What we learn | Decision it feeds |
|---|---|---|---|
| E1 | **Block-rate curve** per endpoint (`browse`, `next`, `player`, watch page, **search**, RSS, image CDN) × IP type × pace (1 request per 10 s → 1 s) | Safe pace per IP and endpoint | Proxy count and cost |
| E2 | **Failover behaviour** | Recovery time after a block; whether blocks hit single IPs or whole subnets; how often failover succeeds; whether the 3-IP park rule triggers | Failover settings, subnet strategy |
| E3 | **Field coverage**: 200 videos, 50 channels | Tags, category, publish date-time, exact likes, chapters, links, country | Field list; whether the player endpoint is needed |
| E4 | **Accuracy check** (D13): the same 200 videos via the official API, ≈ 10 units, deleted after the report | Scraped vs official views, likes, comment counts, publish times | Trust level per field |
| E5 | **Cost per item** | Requests and bytes per backfill (small/medium/large), per video, per 100 comments, per post | Capacity model, bandwidth |
| E6 | **Comments** | Completeness; stop-early scans; replies; pinned/hearted; like abbreviation; time precision | Comment engine settings |
| E7 | **Edits and deletions** (re-sweep after 7 days) | Rates; the two-miss rule | Sweep schedule |
| E8 | **New-upload detection** (14 days) | WebSub vs RSS vs tab checks: delay and misses; RSS errors and stats freshness | Tier intervals |
| E9 | **Discovery yield**, API vs scraped search | Results per call or page; how deep a query goes before results run out; new qualifying channels per day; overlap between the two engines | Search mix, keyword scheduling |
| E10 | **Channel census**: ~500 discovered channels, About + first page only | Share of core vs mention; size mix; uploads/month; comments/video | **Real scenario size and cost** (replaces §6 assumptions) |
| E11 | **Keyword matching quality** on a sample of 300 items labelled by AI, spot-checked by hand (D25) | Precision/recall with Arabic/Kurdish normalization; ambiguous keywords | Matcher rules, suggestion threshold |
| E12 | **Transcripts within the rules** | Does any transcript endpoint answer **without** anti-bot tokens? If not, transcripts are dropped | Transcript go/no-go |
| E13 | **Parser robustness** | Parse-failure rate; client-version auto-detection | Maintenance estimate |

**Phase 0 result:** `docs/phase0-results.md` (aggregates only), containing:

- the measured pace and failover settings;
- the proxy recommendation;
- the **projected list size**, with cost per scenario;
- the final field list with accuracy notes;
- tier schedules;
- discovery and keyword settings;
- a go/no-go for transcripts and the player endpoint.

### 9.3 Getting ready for Phase 0 (you run these; nothing secret is shown in chat)

You can do steps 1–4 in the Google Cloud Console instead. Replace `securebrand-youtube` with a globally unique project ID if it's taken,
and `YOUR_SERVER_IP` with your server's public IP.

1. Create the project:

```bash
gcloud projects create securebrand-youtube --name="SecureBrand YouTube"
```

2. Enable the YouTube Data API v3:

```bash
gcloud services enable youtube.googleapis.com --project=securebrand-youtube
```

3. Create an API key limited to that API and your server's IP:

```bash
gcloud services api-keys create --project=securebrand-youtube --display-name="yt-search" --api-target=service=youtube.googleapis.com --allowed-ips=YOUR_SERVER_IP
```

4. Find the key's ID (the `uid` column):

```bash
gcloud services api-keys list --project=securebrand-youtube --format="table(uid,displayName)"
```

5. Make a private folder for secrets on the server:

```bash
sudo install -d -m 700 /etc/securebrand-youtube
```

6. Write the key straight into a file, without printing it. Replace `KEY_UID` with the ID from step 4:

```bash
gcloud services api-keys get-key-string KEY_UID --project=securebrand-youtube --format="value(keyString)" | sudo tee /etc/securebrand-youtube/youtube_api_key > /dev/null
```

7. Make the file readable by its owner only:

```bash
sudo chmod 600 /etc/securebrand-youtube/youtube_api_key
```

8. Create the proxy list, one proxy per line, in the form `http://user:password@host:port`:

```bash
sudo nano /etc/securebrand-youtube/proxies.txt
```

9. Make the proxy file readable by its owner only:

```bash
sudo chmod 600 /etc/securebrand-youtube/proxies.txt
```

I'll double-check the exact `gcloud` flags when we start Phase 0. If a command fails, send me the error message, but never a key or
password.

---

## 10. Delivery plan after Phase 0

These are small draft pull requests, each with unit and integration tests. I review each one and merge it when CI is green.

1. Repo skeleton: Go module, CI (lint, tests, gitleaks, fixture scanner), Docker Compose, `.gitignore`, README.
2. Phase 0 kit: `ytprobe`, seed keywords + normalization, minimal fake-youtube, `skeleton`. → **Run Phase 0, publish results, adjust
   this plan.**
3. Database schema + migrations; privacy building blocks (HMAC, suppression check, retention settings).
4. Proxy manager: pool, per-IP budgets, cooldowns, failover, subnet health, park rule, pool brake.
5. InnerTube client + parsers (channel, tabs, video, player, search) with fill-rate metrics.
6. Scheduler + job queues + registry + admin CLI.
7. Keyword service: tables, live updates, Arabic/Kurdish matcher.
8. Channel and video collection, snapshots, change logs, relevance flags.
9. New-upload detection: WebSub, RSS, tab checks.
10. Comment engine: backfill with caps, newest-first scans, replies, pinned/hearted, edits, deletions, completeness.
11. Community posts and playlists.
12. Search-runner (API quota governor + scraped search) + discovery (candidate check, channel graph, core/mention classification).
13. Capacity planner + freshness-debt reporting.
14. Keyword suggestions (co-occurrence, trends) + precision tracking.
15. Erasure workflow, suppression audit, optional retention and pseudonymized exports.
16. Media (thumbnails, avatars, banners).
17. Monitoring: metrics, dashboards, alerts, canaries.
18. Commenter enrichment.
19. Archiver: compressed history older than 12 months, still searchable.
20. Webhooks for events and alerts (D20), once you've defined the receiving side.

---

## 11. Main risks

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| 1 | **YouTube's Terms; failover after blocks (D7) reads as deliberate evasion** | Legal claims, cease-and-desist | Your decision; lawyer sign-off (D22); no CAPTCHA/token workarounds; erasure on request; aggregate reporting |
| 2 | **Blocking escalates** (subnet or provider bans, more anti-bot tokens) | Lost IPs, endpoints or fields | Subnet spread, 2–3 providers, the park rule, pool brake, per-endpoint switches; accept losing a field over breaking your rules |
| 3 | **List size explodes** (D10 + D11) | Cost growth, freshness debt | Core/mention split (N1), tiering, capacity planner, freshness debt reporting, optional budget ceiling |
| 4 | **Backfill takes long** at large scale | New channels stay partial for weeks | Priority by size and relevance; extra temporary IPs during ramp-up |
| 5 | **API key cut off** | API search stops | Scraped search and the channel graph continue |
| 6 | **Scraped search heavily protected** | Weaker discovery | API search + channel graph; tuned pacing |
| 7 | **The player endpoint is unavailable** without anti-bot tokens | No tags/category; date-only times for old videos | Detected in Phase 0 |
| 8 | **Website changes break parsers** | Data gaps | Type-based parsing, fill-rate alerts, canaries, same-day fixes |
| 9 | **Keyword noise** (ambiguous terms, bad suggestions) | Irrelevant channels listed | Precision tracking, ban list, suggestion threshold and daily cap |
| 10 | **Relative comment times / abbreviated likes** | Lower precision than the official API | Frequent scans on new videos; precision stored |
| 11 | **GDPR exposure, raised by D18** (no time limit, full identities, enrichment, deleted comments kept) | Fines, complaints, orders to delete | Erasure + suppression always on; DPIA; legitimate-interest assessment; privacy notice; retention, redaction and pseudonymization ready as settings if a lawyer requires them |
| 12 | **WebSub hub limits** at 100,000+ subscriptions | Slower detection | RSS tightened automatically |
| 13 | **Storage grows forever** (D18; TB scale at 100,000+ channels) | Cost, slow queries | Monthly partitions, the compressed archive for data older than 12 months, planned server upgrades (§7.2) |
| 14 | **Credential leak** | Abuse, cost | Files only, IP-restricted key, redaction, gitleaks, rotation |
| 15 | **Hetzner price changes** (two rises in 2026) | Higher server cost | Prices are locked for servers already rented; OVHcloud as a fallback; the stack is provider-neutral (Docker) |

## 12. Open questions and things to verify

- Later: the subdomain for push notifications (D23).
- To verify in Phase 0:
  - safe pace and failover behaviour;
  - whether the player endpoint works without anti-bot tokens;
  - whether transcripts work within the rules;
  - publish-time format for old videos;
  - like abbreviation rules;
  - exactness of total views and comment counts;
  - RSS stats freshness;
  - items per tab page;
  - search depth per query;
  - WebSub limits;
  - the real channel census.
- Legal: lawyer review (now including D7), legitimate-interest assessment, DPIA, privacy notice, Iraq law status, international transfers.

---

## Appendix A: options considered (from plan v1)

| Approach | Coverage | Cost at scale | Reliability | Blocking risk | Terms/legal risk | Maintenance | Role in v3 |
|---|---|---|---|---|---|---|---|
| Official Data API v3 | High, but no community posts, pinned/hearted, transcripts or About links | $0, but the free quota is far too small for collection | High | None | Low | Low | **Search** (+ the one-time accuracy check) |
| Analytics API | Owned channels only | $0 | High | None | Low | Low–Med | **Not used** (D2) |
| Website endpoints (InnerTube), logged out | Very high (minus anti-bot-token endpoints) | Proxies $30–2,100/month by scale | Medium | Medium–High | **High** | **High** | **Main collection + scraped search** |
| RSS + WebSub | New uploads + latest-15 stats | $0 | Medium + High | Low | Low | Low | **Detection** |
| yt-dlp | Similar to InnerTube | — | Low | High | High | High | **Reference code only** |
| Paid vendors | Similar to InnerTube | Tens of thousands of dollars per month at our comment volume | Medium | Vendor's | Medium | Low–Med | **Not used**; possible emergency fallback |

## Sources

Primary Google pages could not be fetched from my sandbox; secondary claims are marked **[verify]**.

- Data API quota costs and audits: [determine_quota_cost](https://developers.google.com/youtube/v3/determine_quota_cost),
  [Quota and Compliance Audits](https://developers.google.com/youtube/v3/guides/quota_and_compliance_audits),
  [OutlierKit quota guide](https://outlierkit.com/resources/youtube-api-quota/), [Blotato pricing guide](https://www.blotato.com/blog/youtube-api-pricing)
- Search operators and parameters: [search.list](https://developers.google.cn/youtube/v3/docs/search/list)
- Policies: [Developer Policies](https://developers.google.com/youtube/terms/developer-policies),
  [Derived metrics and data storage policy](https://developers.google.com/youtube/terms/derived-metrics-policy),
  [Revision history](https://developers.google.com/youtube/terms/revision-history)
- Multiple-project enforcement example: [Solar2D forum post](https://forums.solar2d.com/t/critical-problem-with-your-google-cloud-platform-api-project-appodeal/350374)
- YouTube Terms of Service: [youtube.com/t/terms](https://www.youtube.com/static?gl=US&template=terms)
- Bot checks and anti-bot tokens: [yt-dlp #15865](https://github.com/yt-dlp/yt-dlp/issues/15865),
  [yt-dlp #16147](https://github.com/yt-dlp/yt-dlp/issues/16147), [bgutil PO token provider](https://pypi.org/project/bgutil-ytdlp-pot-provider/2.0.2/)
- Transcripts in 2026: [youtube-transcript-api #592](https://github.com/jdepoix/youtube-transcript-api/issues/592),
  [#593](https://github.com/jdepoix/youtube-transcript-api/issues/593),
  [DEV: transcripts without the Data API](https://dev.to/xixisuperman/getting-youtube-transcripts-without-the-data-api-and-why-translate-to-100-languages-is-usually-43m8)
- Datacenter-proxy blocking anecdote: [strapi-plugin-youtube-transcripts PR #12](https://github.com/PaulBratslavsky/strapi-plugin-youtube-transcripts/pull/12)
- Comment fields on the website: [yt-dlp commit mirror](https://bytes.keithhacks.cyou/etc/yt-dlp/commit/c35448b7b14113b35c4415dbfbf488c4731f006f),
  [Apify comment ledger actor (relative-time fields)](https://apify.com/danthedataman/youtube-channel-comment-ledger.md),
  [YouTube.js Comments](https://www.ytjs.dev/api/namespaces/YT/classes/Comments)
- RSS reliability: [Google AI forum: RSS 404s](https://discuss.ai.google.dev/t/youtube-rss-feed-endpoint-returns-404-errors/113379),
  [Feeder help](https://feeder.co/help/rss/youtube-feeds/)
- WebSub: [Invidious lease handling](https://git.linux.ucla.edu/lug/invidious/commit/7425700009b2e127775ba33b9b9cf661867ecc2e),
  [ytWatchBot push handler](https://github.com/Feverqwe/ytWatchBot/blob/f03e8c53b27ba651868d495e310340ce4e8e62d5/legacy/pushApi.js)
- Vendor prices: [Bright Data YouTube scrapers](https://brightdata.com/blog/web-data/best-youtube-scrapers),
  [Apify YouTube comment scrapers](https://apify.com/apidojo/youtube-comments-scraper),
  [ScrapeCreators comment scrapers](https://scrapecreators.com/blog/youtube-comment-scrapers)
- Server prices: [Hetzner price adjustment](https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/),
  [AgentDeals Hetzner pricing after the 2026 adjustments](https://agentdeals.dev/hetzner-pricing-2026),
  [DigitalOcean General Purpose 8 vCPU / 32 GB](https://spendark.com/instances/g-8vcpu-32gb/),
  [OVHcloud Advance prices](https://www.ovhcloud.com/en-ie/bare-metal/advance/prices/)
- Proxy prices: [AIMultiple ISP proxies](https://aimultiple.com/isp-proxies), [Oxylabs ISP vs datacenter](https://oxylabs.io/blog/isp-vs-datacenter-proxies),
  [Byteful datacenter proxies](https://byteful.com/blog/best-datacenter-proxies)
