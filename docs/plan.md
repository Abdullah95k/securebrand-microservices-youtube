# SecureBrand YouTube collector: plan v3 (draft for approval)

Status: **proposal, nothing built yet.** Nothing gets implemented until you approve this plan.
Last updated: 1 November 2026. All times are **Iraq time (UTC+3)**.

History:

- **v1:** first draft.
- **v2:** scraping for collection, API for search.
- **v3 (this version):** coverage of every Iraq-relevant channel, IP failover, dynamic keywords, 365-day backfill.

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

Your non-negotiable rules still apply:

- **public data only, never log in**;
- **never solve or get around bot checks or CAPTCHAs**, never generate YouTube's anti-bot tokens, and keep pacing;
- privacy controls for comment data;
- credentials only in files or environment variables;
- no real data in the public repo.

D7 changes how "back off when blocked" works: it now means **the blocked IP backs off**, not the whole system. See §3.

### 0.2 The short version

- **Coverage model.** Every channel with at least one Iraq-relevant video, Short or post is on the list. Collection depth depends on the
  channel's type:
  - **Core channels** (mostly Iraq-focused) are collected fully: all videos, comments and posts.
  - **Mention channels** (for example an international news channel that posted one video about Iraq) are on the list and watched for new
    uploads. **Full details and comments are collected only for their Iraq-relevant items.**

  Without this split, a few huge global channels would use most of the capacity on content that has nothing to do with Iraq. This is my
  recommendation, and question N1 asks you to confirm it.
- **Discovery** runs continuously:
  - the official search API (≈ 100 searches/day);
  - **scraped YouTube search** (no quota, much wider);
  - the "channel graph": channels that listed channels mention, feature or collaborate with.
  Keywords, spellings and languages live in a database you can edit at any time. The system also **suggests new keywords** from trending
  Iraq-related hashtags and terms.
- **Scale.** Nobody knows yet how many channels this finds; Phase 0 measures it. Three sizes are modelled:

  | Channels on the list | Requests/day after backfill | Proxy IPs | Monthly cost |
  |---|---|---|---|
  | 20,000 | ~300,000 | 20–50 | **~$160–360** |
  | 100,000 | ~1.5 million | 80–250 | **~$510–1,500** |
  | 300,000 | ~4.4 million | 250–700 | **~$1,400–4,100** |

  The system **grows coverage by itself.** When capacity runs short, it checks the least active channels less often rather than dropping
  any. It tells you when adding IPs or servers would bring freshness back.
- **The 365-day backfill** is the biggest one-time job: about 14 million requests at 20,000 channels, about 70 million at 100,000. Newly
  found channels become fully covered within days to weeks, depending on capacity. The biggest and most relevant channels go first.
- **Unchanged from v2:**
  - pinned/hearted comments, community posts and About links are collected;
  - comment times are relative ("3 days ago"), made precise for new comments by checking often;
  - large like counts are abbreviated;
  - video/audio files are out of scope, and transcripts probably are too (they need anti-bot tokens).
- **Legal risk:** YouTube's Terms forbid automated access and collecting usernames. Switching to a fresh IP after a block is something
  YouTube would regard as evasion, so D7 **raises** that risk. I recommend a lawyer reviews this before production (§4).

---

## 1. Remaining questions (all at once)

New questions, raised by your latest answers:

- **N1. Core vs mention channels.** OK that channels which only *occasionally* mention Iraq get their Iraq-relevant items collected
  in full, but not their whole catalogue? The alternative is full collection for every listed channel. That is roughly 2–5× the cost,
  because international news channels upload thousands of unrelated videos.
- **N2. Keyword suggestions.** New keywords the system finds (trending hashtags, place names, event names) could:
  - **(a)** become active automatically, within a daily cap, with you able to remove them (my recommendation); or
  - **(b)** wait for someone to approve them.
- **N3. Seed keywords.** I'll prepare a starting list: Iraq and Iraqi, the 18 governorates and major cities, in Arabic, Kurdish (Sorani and
  Kurmanji), English, Turkmen and common transliterations. Anything to add from day 1 (brands, people, topics)? You can also add them later
  at any time.
- **N4. What counts as "mentioning Iraq"?** I propose: a keyword match in the **title, description, tags, hashtags or community post
  text** of something the channel itself posted. Comments by other people would **not** count. OK?

Still open from v2 (defaults in brackets):

- **Freshness targets** [the defaults in §8.6].
- **Privacy** [comments and commenter data kept **12 months from collection**; commenter identities **pseudonymized by default**]. Large
  Iraqi diaspora communities live in the EU, so GDPR will apply to some commenters.
- **Legal sign-off** before production [recommended].
- **Servers:** what server(s), and in which country? A domain for one public HTTPS address (push notifications)?
- **Alerts and consumers:** where do alerts go (Telegram, email, Slack)? Who reads the data: dashboard, API, analysis service?
- **Budget:** you want maximum realistic coverage. Is there a monthly ceiling the system should stay under? It can cap proxies and servers
  and degrade freshness instead.
- **Live chat** during streams [out of scope by default].

### Assumptions used until Phase 0 measures them

| Topic | Assumption |
|---|---|
| Channels on the list | Modelled at **20,000 / 100,000 / 300,000** |
| Core vs mention | 50% core, 50% mention |
| Core channel size mix | 75% small (≈ 30 videos, ≈ 1 upload/month, ≈ 10 comments/video; about half of them dormant)<br>22% medium (≈ 300 videos, ≈ 15 uploads/month, ≈ 100 comments/video)<br>3% large (≈ 2,000 videos, ≈ 40 uploads/month, ≈ 500 comments/video) |
| Mention channels | ≈ 1 Iraq-relevant item per month; uploads of other items are only noted, not collected |
| Retention | Comments: 12 months from collection. Statistics: 36 months |
| Media | Thumbnails, avatars, banners only |

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
| Text, author channel ID, author handle/name, avatar | Web | Exact (stored pseudonymized by default, §8.9) |
| Publish time | Web | **Relative** ("3 hours ago"). Precise for comments we first see soon after posting; coarse for backfilled ones ("8 months ago" ≈ ±2 weeks). Precision stored per comment |
| Likes | Web | Exact below 1,000, abbreviated above ("1.2K"): lower bound + text stored [verify] |
| Replies, reply count | Web | Exact |
| Pinned, hearted by creator, reply from creator | Web | Exact |
| Edited | Web ("(edited)" marker) | Exact; earlier versions we saw are kept |
| Deleted | Missing on a re-scan | Fact and time only; the text is removed |

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

### 4.3 Personal data (your rules + GDPR)

- Commenters' names, IDs, avatars and text are personal data. So are channels run by individuals.
- GDPR applies to EU-based people, and the Iraqi diaspora in the EU is large.
- **We need:**
  - a legal basis: legitimate interest, with a written balancing test (a legitimate-interest assessment);
  - a **data protection impact assessment** (DPIA), because this is large-scale monitoring;
  - a public privacy notice (GDPR Article 14).
- Comments can reveal religion, politics or health, so reports should be aggregates, not profiles of individuals.
- Iraq: as far as I know there is no comprehensive data protection law in force yet; please confirm with your lawyer.
- Privacy controls are in §8.9.

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

Each of these becomes a candidate. **Commenters' channels are not used** (they are mostly private individuals).

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

Channels are discovered gradually, so in practice backfill is spread over the first months rather than done in one block.

### 6.5 Storage

| | 20,000 | 100,000 | 300,000 |
|---|---|---|---|
| Comments (12 months) | ~140 M rows → ~100 GB | ~700 M → ~500 GB | ~2.1 B → ~1.5 TB |
| Videos and channels | ~20 GB | ~100 GB | ~300 GB |
| Snapshots (36 months) | ~25 GB | ~130 GB | ~400 GB |
| **Database total** | **~150 GB** | **~750 GB** | **~2.2 TB** |
| S3 (thumbnails + debug samples) | ~50 GB | ~250 GB | ~700 GB |

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
| 1 | **Google Cloud project** + **YouTube Data API v3** + **API key** (restricted to that API and your server IP) | API search, Phase 0 accuracy check | Free. Steps in §9.3 |
| 2 | **Servers**, Linux with Docker | Everything | See 7.2 per scenario. Start with one; add as coverage grows |
| 3 | **Static datacenter/ISP proxy IPs** from **2–3 providers** and many subnets, **outside the EU** | Scraping | Per-IP pricing with unlimited or large bandwidth matters at TB/month volumes. Outside the EU avoids YouTube's cookie-consent page |
| 4 | Optional **small residential proxy allowance** | Only for endpoints Phase 0 shows fail on datacenter IPs | Consent-based sourcing only; capped in GB/month. Per-GB pricing makes it unaffordable as the main pool |
| 5 | **Public HTTPS subdomain** (Let's Encrypt) | WebSub push | Phase 0 can run without it |
| 6 | **S3-compatible object storage** | Thumbnails, debug samples | |
| 7 | **Offsite backups** | Production | 30-day rolling |
| 8 | **Alert destination** | Production | Telegram, email or Slack |
| 9 | **Legal review**, privacy notice, legitimate-interest assessment, DPIA | Before production | Legal work |
| 10 | **Scraper maintenance time** | Ongoing | A few fixes a month when YouTube changes its pages (§8.11) |

### 7.2 Estimated monthly cost (USD, during backfill; lower afterwards)

| Item | 20,000 channels | 100,000 channels | 300,000 channels |
|---|---|---|---|
| YouTube Data API | $0 | $0 | $0 |
| Servers | $100–150 (1 server: 16 cores, 64 GB RAM, 2 × 2 TB NVMe) | $350–600 (database server: 32 cores, 128 GB, 4 × 4 TB NVMe, + 2 workers) | $1,000–1,600 (database primary + replica, 4 workers) |
| Datacenter/ISP proxies (~$0.7–3 per IP) | $30–150 | $80–750 | $200–2,100 |
| Object storage + backups | $30–60 | $80–150 | $200–400 |
| Domain/TLS, monitoring (self-hosted) | $0 | $0 | $0 |
| **Total** | **~$160–360** | **~$510–1,500** | **~$1,400–4,100** |
| Optional residential allowance | +$0–300 | +$0–500 | +$0–800 |

After backfill, the proxy line drops by roughly 40–50%. The range within each column depends mostly on the per-IP pace measured in Phase 0.

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
| **scheduler** | A "next check time" per channel, video, comment stream and post. Priority queues: P0 new uploads and new-video details, P1 new comments, P2 refreshes and sweeps, P3 backfill and candidate checks |
| **proxy-manager** | IP pool, health per IP **and per endpoint type**, subnet grouping, per-IP budgets, cooldowns, failover, the 3-IP park rule, the pool brake (§3). Credentials loaded from a file and never logged |
| **scraper-fetcher** | InnerTube client: logged-out web client (`hl=ar`, `gl=IQ`), continuations, image CDN, client version auto-detected daily. Raw responses kept only for parse failures and a 1% sample (3 days) |
| **parser** | Versioned, type-based parsers; validates fields and reports **field fill rates** (§8.11) |
| **writer** | Upserts, snapshots, change detection (edits, deletions, title/description changes), runs the **keyword matcher** on all new text, drops suppressed items **before writing** |
| **websub-receiver** | Public HTTPS endpoint; hub verification, HMAC signature checks, lease renewals |
| **rss-poller** | Tier-interval feed checks with conditional requests; exact stats for the latest 15 videos |
| **privacy** | Retention, pseudonymization, erasure requests, suppression list, erasure audit log |
| **media** | Thumbnails, avatars, banners |
| **admin CLI** (later a small web page) | Keywords, channels, tiers, review of flagged keywords, erasure requests, health |

**No Kafka.** Up to ~50 requests per second is comfortable with a Postgres job queue (`FOR UPDATE SKIP LOCKED`), with the queue tables split
by priority. **Valkey** holds per-IP rate counters, cooldowns and health, shared by all fetcher processes. At the 300,000 scale, statistics
and analytics may move to **ClickHouse**, with Postgres keeping the main records. That decision is for later.

### 8.3 Storage (Postgres + S3)

- **Main tables:** `channels` (type, tier, country, links), `videos` (kind; `relevant` flag + matched keywords), `comments`,
  `comment_versions`, `authors`, `posts`, `post_comments`, `playlists`, `playlist_items`.
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
- **Large tables** (comments, snapshots) are partitioned by month, so retention drops whole partitions cheaply.

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
   - then the text and author data are deleted, and only a tombstone (ID hash, video, time) is kept.
9. **Completeness per video:** collected ÷ the displayed comment count. Below 90% → re-sweep and alert.
10. **Limits we accept:**
    - the website has no channel-wide "all new comments" feed;
    - late comments on older videos are caught by monthly or quarterly first-page scans;
    - deletions on videos older than 30 days are caught only by those scans.

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
  | Video/channel gone | Re-checked once; if still gone, content deleted and a tombstone kept |

- **Circuit breakers** per endpoint and for RSS (if more than 20% of feeds fail at once, RSS pauses and tab checks take over).
- **Kill switches** per endpoint and per feature (comments, posts, scraped search, discovery).
- **Safe re-runs:** all writes are upserts keyed by YouTube IDs.

### 8.9 Privacy: retention, pseudonymization, erasure

**Retention defaults** (settings; can be shortened):

| Data | Kept for |
|---|---|
| Comments, replies, versions, author data | 12 months from collection |
| Deleted comments | Text and author removed when detected; tombstone only |
| Snapshots (stats over time) | 36 months |
| API search hits | 30 days (API rule) |
| Raw debug responses | 3 days |
| Logs | 14 days; IDs and counts only, never comment text or names |
| Backups | 30 days rolling; erasures re-applied after any restore |

**Pseudonymization modes:**

| Mode | What we store |
|---|---|
| `pseudonymous` (default) | `author_key = HMAC-SHA256(secret, author_channel_id)`; no name, handle or avatar. Unique-author counts and erasure-by-author still work. The secret lives in a file |
| `full` | Name, handle, avatar, channel ID |
| `text_only` | No author key at all |
| `aggregates_only` | No comment text, only counts and metrics |

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
- one server;
- **about 10 test proxy IPs from 2 providers, spread over at least 3 subnets**, so we can measure failover and subnet effects;
- optionally, a public HTTPS subdomain for push notifications.

### 9.2 I provide (1–2 small draft pull requests after approval)

- a `ytprobe` command-line tool;
- the seed keyword list with Arabic/Kurdish normalization;
- the minimal fake-youtube;
- the `skeleton` scrubber;
- a report template.

Raw data stays only on your server, in `/var/lib/securebrand-youtube/phase0`, and is deleted after 14 days. The report contains only
aggregates.

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
| E11 | **Keyword matching quality** on a hand-labelled sample of 300 items | Precision/recall with Arabic/Kurdish normalization; ambiguous keywords | Matcher rules, suggestion threshold |
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
15. Retention, erasure workflow, suppression audit.
16. Media (thumbnails, avatars, banners).
17. Monitoring: metrics, dashboards, alerts, canaries.

---

## 11. Main risks

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| 1 | **YouTube's Terms; failover after blocks (D7) reads as deliberate evasion** | Legal claims, cease-and-desist | Your decision; lawyer review; no CAPTCHA/token workarounds; data minimisation; pseudonymization; aggregate reporting |
| 2 | **Blocking escalates** (subnet or provider bans, more anti-bot tokens) | Lost IPs, endpoints or fields | Subnet spread, 2–3 providers, the park rule, pool brake, per-endpoint switches; accept losing a field over breaking your rules |
| 3 | **List size explodes** (D10 + D11) | Cost growth, freshness debt | Core/mention split (N1), tiering, capacity planner, freshness debt reporting, optional budget ceiling |
| 4 | **Backfill takes long** at large scale | New channels stay partial for weeks | Priority by size and relevance; extra temporary IPs during ramp-up |
| 5 | **API key cut off** | API search stops | Scraped search and the channel graph continue |
| 6 | **Scraped search heavily protected** | Weaker discovery | API search + channel graph; tuned pacing |
| 7 | **The player endpoint is unavailable** without anti-bot tokens | No tags/category; date-only times for old videos | Detected in Phase 0 |
| 8 | **Website changes break parsers** | Data gaps | Type-based parsing, fill-rate alerts, canaries, same-day fixes |
| 9 | **Keyword noise** (ambiguous terms, bad suggestions) | Irrelevant channels listed | Precision tracking, ban list, suggestion threshold and daily cap |
| 10 | **Relative comment times / abbreviated likes** | Lower precision than the official API | Frequent scans on new videos; precision stored |
| 11 | **GDPR exposure** | Fines, complaints | DPIA, legitimate-interest assessment, notice, retention, erasure + suppression, pseudonymization |
| 12 | **WebSub hub limits** at 100,000+ subscriptions | Slower detection | RSS tightened automatically |
| 13 | **Database growth** (TB scale at 300,000 channels) | Cost, slow queries | Monthly partitions, retention drops, ClickHouse for analytics if needed |
| 14 | **Credential leak** | Abuse, cost | Files only, IP-restricted key, redaction, gitleaks, rotation |

## 12. Open questions and things to verify

- **N1–N4** in §1, and the still-open v2 items (privacy defaults, legal sign-off, servers, alerts, budget ceiling, live chat).
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
