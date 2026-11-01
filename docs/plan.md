# SecureBrand YouTube collector: plan v2 (draft for approval)

Status: **proposal, nothing built yet.** Nothing gets implemented until you approve this plan.
Last updated: 1 November 2026. All times are **Iraq time (UTC+3)**.

Version 2 rewrites the plan around your answers of 1 November 2026: scraping for watched channels, the official API for search only.

Legend: **[verify]** marks a fact I could not check against a primary source. Google's developer site is blocked from my build sandbox,
and YouTube's internal website endpoints are undocumented. Phase 0 (§9) confirms or rejects every [verify] item before we build on it.

---

## 0. Decisions so far, and the short version

### 0.1 Your decisions (1 November 2026)

| # | Decision |
|---|---|
| D1 | **Scope:** thousands of channels relevant to Iraq and the Iraqi audience |
| D2 | **No client-owned channels:** no owner logins, no Analytics API |
| D3 | **Discovery:** search all of YouTube for keywords. Channels whose videos match are added to the watch list |
| D4 | **Scraping** (logged out) for the watched channels and their content. The **official Data API is used for search only** |
| D5 | **Goal:** as optimized as possible while keeping the highest quality, stability, data volume, speed and accuracy |
| D6 | **No reuse of the Facebook project ("fbs") for now:** a standalone stack, which can be connected later |

Your non-negotiable rules still apply in full:

- public data only, and no logins;
- no getting around bot checks or CAPTCHAs, pacing and backing off;
- privacy controls for comment data;
- no credentials or real data in the repo.

§3 turns them into concrete scraper behaviour.

### 0.2 The short version

- **Collection:** a scraper reads YouTube's website endpoints the way a logged-out browser does. It covers channels, videos, Shorts, live
  streams, comments and replies (**including pinned and hearted flags**), community posts, playlists, and the links on each channel's About
  page. That is more fields than the official API offers.
- **What the scraper loses compared with the official API:**
  - **Comment timestamps are relative** ("3 days ago"). We make them precise for new comments by checking often.
  - **Large comment like counts are abbreviated** ("1.2K").
  - **Website changes break the scraper** a few times a month and need quick fixes.
- **What is not possible within your rules:**
  - **video/audio files**;
  - most likely **transcripts**.
  Both now need anti-bot tokens from YouTube's BotGuard system, and generating those would mean getting past a bot check.
- **Discovery:** the official search API. The free quota allows **about 100 searches per day**, each returning up to 50 videos. We combine
  keywords per search (`بغداد|البصرة|الموصل`) to stretch that. More quota would need a YouTube audit, which an application that also scrapes
  YouTube should not expect to pass. Optionally we can also scrape YouTube search for wider coverage (question Q2).
- **New uploads:** YouTube's push notifications (WebSub) usually arrive within minutes. RSS feeds are the backup, and also give exact
  view/like counts for each channel's latest 15 videos.
- **Scale and cost:**
  - 5,000 channels ≈ **400,000 requests/day** (about 5 per second), spread over **20–40 proxy IPs** at a polite pace per IP;
  - estimated **$150–450 per month** on datacenter/ISP proxies, or up to about $750 if some traffic needs residential proxies.
- **Legal risk you are accepting:** YouTube's Terms forbid automated access and collecting information that identifies people. That risks
  IP blocks, legal claims, and losing the API key used for search. Please get a lawyer's sign-off (§4).
- **Phase 0 comes first:** two weeks of careful measurement. It finds how fast we can go without triggering bot checks, which fields really
  come back, and how much keyword search really finds.

---

## 1. Remaining questions (all at once)

1. **Your rules applied to scraping:** please confirm my reading in §3:
   - **(a)** we may use a **pool of proxy IPs**, each at a slow, polite pace;
   - **(b)** when YouTube shows a bot check, that IP stops and **everything slows down**. We never solve, bypass, or immediately retry the
     check from another IP;
   - **(c)** anything that needs YouTube's anti-bot tokens is **out of scope**: video/audio files, and probably transcripts.
2. **Search breadth:** official API only (about 100 searches/day, as you said), or **also scrape YouTube search**? Scraped search has no
   quota and would find far more channels. It is the same legal category as the rest of what we scrape. I recommend both, with the API for
   scheduled sweeps and scraped search for breadth.
3. **Keywords:**
   - the keyword list or categories (brands, people, places, topics);
   - the languages: Arabic, Kurdish (Sorani/Kurmanji), English, Turkmen?
4. **"Relevant to Iraq": what makes a found channel join the watch list automatically?** My proposal:
   - the channel lists Iraq as its country, or its content is mostly Arabic/Kurdish with Iraqi place names;
   - and it has ≥ 1,000 subscribers;
   - and it uploaded in the last 90 days;
   - borderline channels go to a review list;
   - at most N new channels per day (for example 100).
5. **Watch-list size:** what target for the first year (2,000, 5,000, 10,000)? It drives the proxy count and cost.
6. **Tiers:** OK to sort channels automatically into tiers A (top ~10%), B and C by size, activity and keyword relevance, with manual
   overrides? See §6.2.
7. **Backfill for each new channel:** I propose metadata for **all** its videos, but comments only from the **last 90 days** (deeper for
   tier A). Comments are about 80% of backfill cost, so this decides how fast new channels become useful.
8. **Freshness targets:** OK with the defaults in §8.6? For example, tier A: new upload within minutes, new comments within 1 hour.
9. **Privacy defaults:**
   - comments and commenter data kept **12 months**;
   - commenter identities **pseudonymized by default**.
   Large Iraqi diaspora communities live in Germany, Sweden and other EU countries, so GDPR will apply to some commenters.
10. **Legal sign-off:** will a lawyer review the scraping and personal-data risk (§4) before production?
11. **Servers:** what server(s) do you have, and in which country? Do you have a domain for one public HTTPS address (needed for push
    notifications)?
12. **Alerts and consumers:** where should alerts go (Telegram, email, Slack)? Who reads the data: a dashboard, an internal API, an
    analysis service?
13. **Phase 0 accuracy check:** may Phase 0 make a **one-time** call to the official API for about 200 videos (≈ 10 quota units)? It would
    measure how accurate the scraped numbers are. This is outside "API for search only", so I'm asking.
14. **Budget ceiling:** "as optimized as possible" is noted. Is the recommended configuration in §7 (≈ $150–450/month at 5,000 channels)
    acceptable, or is there a hard limit?
15. **Live chat** during live streams: wanted? It can be scraped logged out, but it is high volume. Out of scope by default.

### Assumptions used until you answer

| Topic | Assumption |
|---|---|
| Watch list | Three sizes modelled: **2,000 / 5,000 (main case) / 10,000** channels |
| Average channel | 500 videos (many Shorts), 20 uploads/month, 200 comments per video (top-level + replies); heavy tail |
| Tier mix | A 10%, B 40%, C 50% |
| Comment retention | 12 months |
| Backfill | All video metadata; comments for the last 90 days |
| Search | API only, ~100 searches/day |
| Media | Thumbnails, avatars and banners only |

---

## 2. What we'll collect, from where, and how accurate it is

Sources:

- **Web** = YouTube's internal website endpoints, logged out. These are `youtubei/v1/browse`, `/next`, `/player` and the watch page.
  Together they are called "InnerTube".
- **RSS / WebSub** = public channel feeds and push notifications.
- **API** = the official Data API, used for search only.

### Channel

| Field | Source | Accuracy / notes |
|---|---|---|
| Name, handle, description, country, join date | Web (channel page + About panel) | Exact |
| **Links on the About page** | Web (About panel) | Exact |
| Subscriber count | Web | **Abbreviated** ("1.23M"), like the website shows. The official API is rounded to 3 digits too, so no real loss |
| Total views, video count | Web (About panel) | Exact [verify] |
| Avatar, banner | Web → image CDN | Exact; images fetched from YouTube's image servers (no bot checks there) |
| Featured / linked channels | Web (Channels tab, About) | Exact; also feeds discovery (§5) |
| Change history | Our own snapshots from day 1 | No history before day 1 |

### Videos, Shorts, live streams

| Field | Source | Accuracy / notes |
|---|---|---|
| Title, description, channel | Web (`/next` or watch page); RSS for new uploads | Exact |
| Publish time | RSS/WebSub (exact, new uploads); Web player data (date or date-time [verify]) | Exact for new videos; possibly date-only for old ones |
| Duration | Web (player data; tab listings show "12:34") | Exact |
| Tags, category | Web (**player data only**) | Only if the player endpoint answers without a bot check from our IPs [verify, critical]. Otherwise we drop these fields rather than push |
| Views | Web (exact "12,345 views"); RSS (exact, latest 15 videos) | Exact |
| Likes | Web (like button label); RSS (latest 15) | Exact [verify]; dislikes are not public |
| Comment count | Web (comments header) | Exact or abbreviated [verify] |
| Shorts vs long video vs live | Web (separate Videos / Shorts / Live tabs) | Exact, from the tab the video appears in |
| Live status, schedule, live viewers | Web (live badge, `updated_metadata`) | Viewer count every 5 min while live |
| **Chapters** (creator-written **and automatic**) | Web (`/next`) | Exact |
| Thumbnails | Image CDN | Exact |
| Transcripts | Web | **Probably not available within your rules.** In 2026 the transcript endpoints need BotGuard "proof-of-origin" tokens. Phase 0 checks honestly; if blocked, we stop |
| Video/audio files | — | **Out of scope:** streams need anti-bot tokens, and downloading media is also a copyright issue |

### Comments and replies

| Field | Source | Accuracy / notes |
|---|---|---|
| Text, author channel ID, author handle/name, avatar | Web (`/next` comment pages) | Exact |
| **Publish time** | Web | **Relative** ("3 hours ago", "2 years ago"). For comments we first see soon after posting, we store a precise estimate (error ≤ our scan interval). Backfilled old comments only get coarse values ("2 years ago" ≈ ±6 months). Each comment stores its precision |
| Likes | Web | Exact below 1,000; **abbreviated above** ("1.2K"). We store the lower bound plus the text [verify] |
| Reply count, replies | Web (reply pages) | Exact |
| **Pinned, hearted by creator, reply from creator** | Web | Exact |
| Edited | Web ("(edited)" marker) | Exact, and we keep earlier versions we saw |
| Deleted | Derived: missing on a re-scan | Detected during sweeps (§8.7); we keep only the fact, not the text |
| Order | Web "Newest first" sort | Lets us stop scanning as soon as we reach known comments |

### Community posts, playlists, new uploads

| What | Source | Notes |
|---|---|---|
| **Community posts** (text, images, polls, likes, comment count, post comments) | Web (Posts tab) | Likes abbreviated; publish time relative |
| Playlists and items | Web (Playlists tab, playlist pages) | Exact |
| New uploads | WebSub push + RSS + channel tabs | §8.5 |
| Keyword search | **API** `search.list` (+ optional scraped search) | §5 |

---

## 3. How the scraper follows your rules

| Your rule | What the scraper does |
|---|---|
| Public data only, never log in | Only logged-out requests. **No Google/YouTube accounts and no account cookies, ever.** The only cookie is the anonymous visitor ID every logged-out browser receives. Nothing private, unlisted-by-link, members-only or age-gated-behind-login is collected |
| Don't get around bot checks or CAPTCHAs | **Never** solve CAPTCHAs or use CAPTCHA-solving services. **Never** generate or obtain BotGuard / "proof-of-origin" tokens. **Never** drive headless browsers to pass challenges, or pretend to be Googlebot. Endpoints that require these (media streams, likely transcripts) are switched off |
| Pace requests | Every proxy IP has its own slow budget, for example one request every 3–5 s; Phase 0 sets the real number. A global limit sits on top. Requests are spread evenly, never in bursts |
| Back off when blocked | Signals: HTTP 429/403, "Sign in to confirm you're not a bot", redirects to Google's "sorry" page. See the list below |
| Credentials never committed or logged | Proxy passwords and the API key are read from files named in environment variables (`*_FILE`). They are never logged; the logger redacts proxy URLs and auth headers |
| Public repo, no real data | Fixtures are generated. Real response *shapes* are captured only as **skeletons**: every value is replaced with a type placeholder by a scrubber, and a human reviews each one before commit (§8.10) |

What happens on a block signal:

1. **That IP stops** and cools down: 30 min, then 2 h, then 12 h, then 24 h on repeat. It resets after a clean day.
2. **The global speed drops** by 30%, and recovers by 5% every 10 clean minutes.
3. **The request waits at least 15 minutes.** It is **not** immediately re-sent from another IP.
4. If more than 20% of IPs are challenged within an hour, **everything pauses for an hour** and you get an alert.
5. If one endpoint (for example the player endpoint) keeps getting challenged, **we switch that endpoint off.** We lose its fields rather
   than push.

Point (1) is the interpretation you need to confirm (Q1). A pool of IPs, each going slowly, spreads normal load. Using a fresh IP to
re-send a request that was just challenged would be evasion, and we won't do that.

---

## 4. Legal and policy position

### 4.1 YouTube's Terms of Service (the scraping part)

YouTube's Terms forbid:

- accessing the service by **automated means** (except public search engines, or with YouTube's written permission);
- **collecting information that might identify a person**, such as usernames, without their permission.

Collecting comments with author names does exactly that. Possible consequences:

- IP blocks;
- a breach-of-contract claim or cease-and-desist letter;
- loss of our Google Cloud project's API access if YouTube links it to the scraping.

Courts in some countries have held that scraping logged-out public pages is not computer crime, but contract and data-protection law
still apply. **This is your business decision (D4). I recommend a lawyer reviews it before production (Q10).**

### 4.2 The official API (the search part)

- **Quota:** 10,000 units/day by default. One `search.list` call costs 100 units, so **≈ 100 searches per day**, each returning up to 50
  results. Every extra page of results costs another 100. Quota resets at midnight US Pacific time: **11:00 Iraq time** until 13 March
  2027, and **10:00 Iraq time** during US daylight saving (14 March – 6 November 2027).
- **More quota needs a compliance audit** of the whole application, and the API terms prohibit scraping YouTube [verify exact wording].
  So we should **plan for 10,000 units/day as a hard ceiling** and not apply for more.
- Using several Google Cloud projects to multiply quota is forbidden, and Google enforces it. We won't.
- **API data rules:** search results are "API Data". They must be refreshed or deleted within 30 days. So we keep search hits (keyword →
  video and channel IDs) for at most 30 days. Channels promoted to the watch list are then collected by the scraper.

### 4.3 Personal data (your rules + GDPR)

- Commenters' names, IDs, avatars and text are personal data. So are channels run by individual creators.
- GDPR applies to EU-based commenters, and the Iraqi diaspora in the EU is large.
- **We need:**
  - a legal basis: legitimate interest, with a written balancing test (a legitimate-interest assessment);
  - a **data protection impact assessment** (DPIA), because this is large-scale monitoring;
  - a public privacy notice (GDPR Article 14).
- Comments can reveal religion, politics or health, so reports should be **aggregates**, not profiles of individuals.
- Iraq: as far as I know there is no comprehensive data protection law in force yet; please confirm with your lawyer.
- Privacy controls (pseudonymization, retention, erasure, suppression) are in §8.9.

---

## 5. Discovery: keywords → candidate channels → watch list

### 5.1 Keyword search through the API (≈ 100 calls/day)

- **Keyword groups:** we combine related keywords with the API's OR operator (`|`) and exclusions (`-`). Example group:
  `بغداد|البصرة|الموصل|أربيل|النجف` (Baghdad, Basra, Mosul, Erbil, Najaf). One call then covers 5–10 keywords.
- **Each call uses:**
  - `type=video`;
  - `order=date`;
  - `publishedAfter` = the group's last successful run, so we only get new videos;
  - `regionCode=IQ`;
  - `relevanceLanguage` = `ar`, `ku` or `en`, depending on the group;
  - `maxResults=50`.
- **Paging:** we fetch a second page only when the first one is full, which means more results are probably waiting.
- **Default budget:**

  | Use | Calls/day |
  |---|---|
  | Priority groups, every 3 h | ~20 groups × 4 = 80 |
  | Extra pages and ad-hoc searches | ~20 |

  The quota governor never lets a search run past the daily ceiling.
- **Known limitation:** API search is not exhaustive. It returns a relevance-filtered subset, and results differ from the website. Phase 0
  measures the real yield.

### 5.2 Free discovery from data we already scrape (no API quota)

- Channels **@mentioned or linked** in watched channels' descriptions, community posts and About links.
- **Featured channels** on watched channels' Channels tab.
- **Collaborators** on collaboration videos.
- Optional (Q2): **scraped YouTube search**, using the same keyword groups with "upload date" sorting. No quota, much wider reach.
- We do **not** use commenters' channels for discovery. Commenters are mostly private individuals.

### 5.3 Scoring and promotion

1. **Every found channel becomes a candidate.** We fetch its About panel and first page of videos: 2–3 requests.
2. **Relevance score**, from:
   - country = Iraq;
   - language mix of recent titles (Arabic/Kurdish), using a language-detection library;
   - Iraqi place and keyword hits;
   - number of keyword-matching videos;
   - subscribers;
   - recent activity.
3. **Auto-promote** above a threshold. Borderline channels go to a **review list**; below the threshold they are rejected, and the
   rejection is remembered so we don't re-check them for 90 days.
4. **Daily cap** on promotions, for example 100/day. A capacity check refuses promotions when the proxy pool can't keep up with the tier
   freshness targets.
5. Promoted channels get a tier (§6.2) and a backfill job.

---

## 6. Volume and capacity model (estimates, to be replaced by Phase 0)

### 6.1 Requests per channel per day

| Task | Tier A | Tier B | Tier C |
|---|---|---|---|
| RSS check (detection backup + exact stats for the latest 15 videos) | every 30 min = 48 | every 2 h = 12 | every 6 h = 4 |
| New-video details (watch page / `/next` + player) | ~1.5 | ~1.5 | ~1.5 |
| Older-video stats (channel tab listings + per-video refresh) | ~10 | ~10 | ~5 |
| Channel snapshot (About panel) | every 6 h = 8 | daily = 2 | every 3 days ≈ 1 |
| New-comment scans on recent videos (newest-first, stop at known) | ~70 | ~37 | ~23 |
| Late comments on older videos (first page, monthly) | ~6 | ~6 | ~3 |
| Full comment sweeps at 7 and 30 days (edits/deletions) | ~20 | ~20 | ~10 |
| Community posts and their comments | ~2 | ~1 | ~0.5 |
| Playlists | ~0.5 | ~0.5 | ~0.3 |
| **Total per channel per day** | **~166** | **~90** | **~48** |

With the tier mix A 10%, B 40%, C 50%, the average is **≈ 80 requests per channel per day**. About a quarter of those are cheap RSS
fetches.

### 6.2 Tiers

| Tier | Share | Who | Promotion / demotion |
|---|---|---|---|
| A | Top ~10% | Largest, most active and most keyword-relevant channels | Recomputed weekly; manual override |
| B | ~40% | Normal channels | Recomputed weekly |
| C | ~50% | Long tail, low activity | Recomputed weekly; channels with no upload in 180 days go to "dormant" (weekly check only) |

### 6.3 Scenarios

| | 2,000 channels | **5,000 channels** | 10,000 channels |
|---|---|---|---|
| Steady requests/day | ~160,000 (≈ 2/s) | **~400,000 (≈ 5/s)** | ~800,000 (≈ 9/s) |
| Backfill per new channel (all metadata + 90 days of comments) | ~1,500 requests | ~1,500 | ~1,500 |
| Backfill at 100 new channels/day | +150,000/day | +150,000/day | +150,000/day |
| Proxy IPs needed (at ~1 request per 3 s per IP, +50% headroom) | ~10–15 | **~20–40** | ~40–80 |
| Data transfer (≈ 40 KB compressed per request) | ~200 GB/month | **~500–650 GB/month** | ~1–1.3 TB/month |
| API search | ~100 calls/day | ~100 calls/day | ~100 calls/day |

The per-IP pace and bytes per request are the two most uncertain numbers. Phase 0 measures both.

### 6.4 Storage (5,000 channels)

| Data | Estimate |
|---|---|
| Comments (12-month window): 5,000 × ~240 videos × 200 comments ≈ 240 M rows | ~150–200 GB in Postgres with indexes |
| Videos (~2.5 M) and channels | ~10 GB |
| Snapshots (views/likes/comments/subscribers over time) | ~25 MB/day → ~9 GB/year |
| Raw responses (only parse failures + a 1% sample, kept 3 days) | ~5–20 GB in S3 |
| Thumbnails for new videos | ~3 GB/month |

---

## 7. What you need to provide, and the monthly cost

### 7.1 Requirements list

| # | Item | Needed for | Notes |
|---|---|---|---|
| 1 | **Google Cloud project** + **YouTube Data API v3** enabled + **API key** (restricted to that API and your server IP) | Search | Free; no billing account needed. Steps in §9.3 |
| 2 | **Server(s)**, Linux with Docker | Everything | 5,000 channels: 1 dedicated server, 16 cores, 64 GB RAM, 2 × 2 TB NVMe. 10,000: two of those |
| 3 | **Proxy IPs**: static datacenter or ISP IPs from **2–3 providers** and different networks, **outside the EU** | Scraping | 20–40 IPs for 5,000 channels. Outside the EU avoids YouTube's EU cookie-consent page. Credentials in a file on the server |
| 4 | Optional **small residential proxy allowance** | Only for endpoints Phase 0 shows fail on datacenter IPs | Only from a provider with documented, consent-based IP sourcing; capped in GB per month |
| 5 | **Public HTTPS subdomain** (for example `yt-hooks.example.com`, with Let's Encrypt) | WebSub push | Phase 0 can run without it |
| 6 | **S3-compatible object storage** | Thumbnails, debug samples | |
| 7 | Offsite **backups** | Production | 30-day rolling |
| 8 | **Alert destination** | Production | Telegram, email or Slack |
| 9 | **Legal review**, privacy notice, legitimate-interest assessment, DPIA | Before production | Legal work, not engineering |
| 10 | Engineering time for **scraper maintenance** | Ongoing | Expect a few fixes a month when YouTube changes its pages (§8.11) |

### 7.2 Estimated monthly cost (USD, check prices at purchase)

| Item | 2,000 channels | **5,000 channels** | 10,000 channels |
|---|---|---|---|
| YouTube Data API (search) | $0 | $0 | $0 |
| Server(s) | $50–100 | **$90–150** | $200–300 |
| Datacenter/ISP proxies (~$1.5–4 per IP) | $15–60 | **$30–160** | $60–320 |
| Optional residential allowance (capped, ~$2–6/GB) | $0–150 | $0–300 | $0–500 |
| Object storage | ~$5 | ~$10–20 | ~$20–40 |
| Offsite backups | ~$10 | ~$20–40 | ~$40–80 |
| Domain/TLS, monitoring (self-hosted) | $0 | $0 | $0 |
| **Total** | **~$80–325** | **~$150–450** (≤ ~$750 with residential) | **~$320–1,240** |

Things that would push cost up:

- running everything through residential proxies (≈ $1,000–4,000/month at 5,000 channels, because it is priced per GB);
- deeper comment backfill;
- faster freshness targets.

---

## 8. Architecture

### 8.1 Overview

```
 YouTube WebSub hub ──POST──► websub-receiver ─┐
 YouTube RSS feeds ◄──────── rss-poller ───────┤  "check this now"
                                               ▼
 search-runner ──► YouTube Data API     ┌─────────────┐   due jobs    ┌──────────────────┐  via proxy pool  ┌───────────┐
 (quota-governed,   (search.list only)  │  scheduler  │──────────────►│ scraper-fetcher  │─────────────────►│ YouTube   │
  ≤100 calls/day)                       │ (per-entity │  (priority    │ (InnerTube client│                  │ website   │
        │ hits (kept ≤30 days)          │  next-check │   queues)     │  + image CDN)    │◄── responses ────│ endpoints │
        ▼                               │  times)     │               └───────┬──────────┘                  └───────────┘
 discovery (candidates, scoring, ───────►─────────────┘                       │       ▲ per-IP budgets, cooldowns,
            review list, promotion)          ▲                                │       │ global AIMD speed
        ▲                                    │                                ▼       │
        └──── mentions / featured channels ──┤                         parser (versioned, ┌──────────────┐
                                             │                          drift detection)──│ proxy-manager│
                                   registry (watch list, tiers)               │           └──────────────┘
                                                                              ▼
           privacy (retention, erasure,  ◄── suppression check ──►  writer (upsert, snapshots, diffs) ──► Postgres
                    suppression list)                                         │                          S3 (thumbnails,
                                                                              ▼                              debug samples)
                                                                   events → dashboards / analysis (later)
```

### 8.2 Services

All services are written in Go. One repo, several binaries, deployed with Docker Compose.

| Service | Job |
|---|---|
| **registry** | The watch list: channel IDs, tiers, status (candidate / watched / dormant / rejected / suppressed), backfill state |
| **search-runner** | Runs keyword-group searches on schedule through the official API, under the **quota governor**. The governor charges 100 units per call before sending, stops at the daily ceiling, and knows the reset time (11:00 Iraq time in winter, 10:00 in summer). Writes search hits for discovery |
| **discovery** | Turns search hits and mentions into candidates, scores them (§5.3), manages the review list and promotions, and respects the daily cap and capacity check |
| **scheduler** | Keeps a "next check time" for every channel, video, comment stream and post. Emits due jobs into priority queues (§8.6). Push and RSS signals jump the queue |
| **proxy-manager** | Holds the IP pool and health scores, enforces per-IP budgets, cooldowns and the global speed (§3). Detects block signals. Has a per-endpoint on/off switch |
| **scraper-fetcher** | The InnerTube client: builds logged-out web-client requests (`hl=ar`, `gl=IQ`), follows continuations, fetches images from the CDN. It auto-detects the current client version from YouTube's homepage. Raw responses are kept only for parse failures and a 1% sample (3 days) |
| **parser** | Versioned parsers. Finds data by object type rather than fixed paths, which survives layout changes. Validates required fields and reports how often each field is filled (drift detection, §8.11) |
| **writer** | Upserts into Postgres, appends snapshots, detects changes (titles, descriptions, comment edits and deletions). **Drops anything on the suppression list before writing** |
| **websub-receiver** | Public HTTPS endpoint. Answers the hub's verification, checks HMAC signatures, renews subscriptions before their lease ends (5 days) |
| **rss-poller** | Fetches feeds at tier intervals with conditional requests. Exact views/likes for the latest 15 videos go into snapshots |
| **privacy** | Retention jobs, pseudonymization, erasure requests, suppression list, erasure audit log |
| **media** | Thumbnails, avatars and banners from YouTube's image servers, under the same retention |
| **admin CLI** (later a small web page) | Add/remove channels, review candidates, override tiers, run erasure requests, inspect health |

**No Kafka.** At about 5–10 requests per second, a Postgres job queue (`FOR UPDATE SKIP LOCKED`) is simpler and enough. **Valkey** holds the
per-IP rate counters and cooldowns, so several fetcher processes can share them. Event publishing to other systems can be added later.

### 8.3 Storage (Postgres + S3)

- **Main tables:**
  - `channels`, `channel_links`, `videos` (kind: video/short/live/premiere), `comments`, `comment_versions`, `authors`;
  - `posts`, `post_comments`, `playlists`, `playlist_items`.
- **Time series:** `channel_snapshots`, `video_snapshots`, `post_snapshots`, partitioned by month.
- **Discovery:** `search_hits` (deleted after 30 days, API rule), `candidates`, `rejections`, `keyword_groups`.
- **Privacy:** `suppression_list`, `erasure_requests`, `erasure_audit`.
- **Operations:** `jobs`, `entity_schedule`, `quota_ledger`, `proxy_health` (no credentials stored), `parse_failures`, `field_fill_rates`.
- **Precision columns** keep accuracy visible:
  - `published_at_est` + `published_at_precision_s` for comments and posts;
  - `like_count_min` + `like_count_text` where counts are abbreviated;
  - `source` (`web`, `rss`, `websub`, `api_search`) on every row.

### 8.4 How each collection works

- **Channel:** channel page + About panel (country, links, join date, totals) and the Channels tab, every 6 h / 1 day / 3 days by tier.
- **Video list:** Videos, Shorts and Live tabs. Each page returns about 30 items (more for Shorts [verify]); we follow continuations.
  Listings also give abbreviated view counts, which are good enough for slow refreshes of old videos.
- **Video details:**
  - one **watch page** request carries both the page data and the player data, compared with two separate JSON requests. Phase 0 picks
    whichever is cheaper and less blocked;
  - if the player data is refused, we keep the `/next` fields and mark tags and category "unavailable".
- **Comments:** §8.7.
- **Community posts:** Posts tab pages, then each new post's comments.
- **Live:** the Live tab and WebSub catch scheduled streams; while a stream is live, we read the viewer count every 5 min (tier A/B) or
  15 min (tier C).

### 8.5 New-upload detection

1. **WebSub push.** The video is fetched immediately; target: within minutes.
2. **RSS** at tier intervals (30 min / 2 h / 6 h). Any video ID we haven't seen triggers a fetch. RSS also feeds exact stats for recent
   videos.
3. **Channel tab check** daily (tier A: every 6 h). It catches anything missed, premieres, and videos made public later.

We measure **detection delay** = first seen − published, for each layer.

### 8.6 How often things are re-checked (defaults, tuned after Phase 0)

| What | Tier A | Tier B | Tier C |
|---|---|---|---|
| New upload detected | minutes (push), ≤ 30 min (RSS) | minutes, ≤ 2 h | minutes, ≤ 6 h |
| New-video stats, first 48 h | 30 min (RSS) | 2 h | 6 h |
| Video stats, 2–30 days | 30 min–6 h (RSS while in the latest 15, then per video) | 12 h | 24 h |
| Video stats, 30 days – 1 year | weekly | weekly | monthly |
| Video stats, older | monthly | monthly | quarterly |
| New comments, video 0–48 h | 1 h | 3 h | 6 h |
| New comments, 2–7 days | 6 h | 12 h | 24 h |
| New comments, 7–30 days | 24 h | 48 h | weekly |
| Late comments, 30 days – 12 months | monthly (first page) | monthly | quarterly |
| Full comment sweep | at 7 and 30 days | at 7 and 30 days | at 30 days |
| Channel snapshot | 6 h | 24 h | 3 days |
| Community posts | 24 h | 3 days | weekly |
| Playlists | weekly | weekly | monthly |

**Priority order when capacity is short:**

| Priority | Work |
|---|---|
| P0 | New-upload detection and new-video details. Never dropped |
| P1 | New-comment scans on recent videos |
| P2 | Stats refreshes, sweeps, posts |
| P3 | Discovery candidate checks, backfill |

### 8.7 Keeping comments complete and up to date

1. **Open the comment section** from the video's data, then switch to **"Newest first"**.
2. **Read top-level comments.** Each page has about 20. For each comment we record:
   - ID, text, author channel ID, handle/name, avatar;
   - the relative time;
   - likes (exact or abbreviated);
   - reply count;
   - **pinned**, **hearted**, author-is-creator;
   - the "(edited)" marker.
3. **Read replies** for threads with replies (about 10 per page), following continuations.
4. **New-comment scans:** read newest-first and **stop after the first page that is entirely already known**, plus one page of overlap.
   Most scans cost **1 request**. When a known thread's reply count has grown, we fetch its replies again.
5. **Publish-time estimates:** a comment showing "25 minutes ago" when we first see it gets a timestamp accurate to a minute or two. A
   backfilled "2 years ago" comment gets a coarse estimate, and its precision is stored.
6. **Edits:** a changed text or a new "(edited)" marker → we save a new version.
7. **Deletions:** a full sweep (at 7 and 30 days) that no longer finds a known comment marks it **missing**. A second miss on the next scan
   confirms it. Then we **delete its text and author data** and keep a tombstone (ID hash, video, time) so deletions can still be
   counted.
8. **Completeness per video:** collected ÷ the displayed comment count. Below 90% (threshold set in Phase 0) → re-sweep and alert. Some gap
   is normal: YouTube's displayed count includes comments it hides.
9. **Huge videos:** backfill at most N comments per video (for example 20,000, newest first); after that, rely on new-comment scans. Capped
   videos are flagged "partial".
10. **Limits we accept:**
    - there is no channel-wide "all new comments" feed on the website (that exists only in the official API);
    - so late comments on older videos are caught by monthly first-page scans, not within hours;
    - deletions on videos older than 30 days are only caught if they show up in those scans.

### 8.8 Rate limiting, retries, stability

- **Rate limits:**
  - per IP: a token bucket at the Phase 0 pace, with random gaps between requests;
  - global: one speed multiplier (adaptive: drops quickly on trouble, recovers slowly);
  - per endpoint: concurrency caps;
  - per channel: at most one job at a time.
- **Retries:**

  | Error | Action |
  |---|---|
  | Network errors, 5xx | Exponential backoff with jitter (2 s, 4 s, 8 s, 16 s, 32 s), at most 5 attempts, on the **same** IP after its next slot |
  | Block signals | §3 rules: cooldown, slowdown, re-queue after 15 min or more |
  | Parse errors | Raw response kept (3 days), job moved to `parse_failures`, alert if the rate rises |
  | Video/channel gone | Re-checked once; if still gone, content deleted and a tombstone kept |

- **Circuit breakers** per endpoint and for RSS. If more than 20% of feeds fail at once, YouTube's feed service is probably down, so we
  pause RSS and rely on tab checks.
- **Safe re-runs:** all writes are upserts keyed by YouTube IDs; re-running a job is harmless.
- **Kill switches** per endpoint and per feature (comments, posts, discovery), so one problem never takes down everything.

### 8.9 Privacy: retention, pseudonymization, erasure

**Retention defaults** (settings; can be shortened):

| Data | Kept for |
|---|---|
| Comments, replies, versions, author data | 12 months from collection |
| Deleted comments | Text and author removed when detected; tombstone only |
| Snapshots (stats over time) | 36 months (channel/video statistics, not personal) |
| Search hits from the API | 30 days (API rule) |
| Raw debug responses | 3 days |
| Logs | 14 days. Logs contain IDs and counts only, never comment text or names |
| Backups | 30 days rolling; erasures re-applied from the suppression list after any restore |

**Pseudonymization modes**, per watch-list segment:

| Mode | What we store |
|---|---|
| `pseudonymous` (default) | `author_key = HMAC-SHA256(secret, author_channel_id)`; no name, handle or avatar stored. Unique-author counts and erasure-by-author still work. The secret lives in a file, like other credentials |
| `full` | Name, handle, avatar, channel ID |
| `text_only` | No author key at all |
| `aggregates_only` | No comment text, only counts and metrics |

**Erasure requests:**

| Kind | Input | What happens |
|---|---|---|
| By author | Channel URL, handle or ID (resolved by one scraped channel-page request) | Delete all their comments, replies, versions and author row. Add a suppression entry (keyed hash) so their future comments are dropped **before** storage |
| By channel | Channel ID | Delete the channel, its videos, posts, comments on them, snapshots and media. Suppress it, so discovery can never re-add it without an admin override with a recorded reason |
| By video / comment / post | ID | Delete and suppress |

How an erasure is processed:

- primary stores cleaned **within 72 hours**;
- debug samples age out in 3 days, backups in 30;
- every step logged in `erasure_audit`, with no personal data.

**The suppression list is checked everywhere:** in the writer, discovery, backfill and restores. A daily job confirms that suppressed items
in the database = 0; anything else raises an alert.

### 8.10 The repo is public: hygiene

- No captured responses, real IDs, real names or comments in the repo, ever.
- **Fixtures** come from a generator: fake channels, videos and comments, with fake IDs in a recognisable pattern (for example `UCfake…`).
- **Response skeletons:**
  - when we need a real response's *structure* for tests, the `skeleton` tool turns every value into a type placeholder (`"<string>"`,
    `"<int>"`);
  - it drops arrays down to one item;
  - a human reviews the result before commit.
- **CI on every pull request:**
  - gitleaks (secrets);
  - a fixture scanner that fails on real-looking YouTube IDs, emails, phone numbers, `AIza…` keys and proxy URLs with passwords.
- **`.gitignore`** blocks `.env*`, `*.key`, `secrets/` and data folders.

### 8.11 Stability against website changes

- **Type-based parsing:** find all comment objects of a given type anywhere in the response, instead of fixed paths that break when YouTube
  moves things.
- **Field fill-rate monitoring:**
  - for each field (views, likes, tags, pinned…) we track how often it is present;
  - a drop (for example likes missing on more than 5% of videos in an hour) raises an alert within minutes;
  - affected jobs are paused rather than storing bad data.
- **Canary checks:** every 15 minutes a few public channels and videos are fetched and parsed. Only health metrics are kept, nothing is
  stored.
- **Client version auto-update:** read from YouTube's homepage once a day.
- **Reference implementations:** when something breaks, compare with yt-dlp's (public domain) and YouTube.js's (MIT) current parsers.
- **Expected maintenance:** a few breakages a month. Most are a parser update shipped as a small pull request the same day.

### 8.12 Testing: a fake YouTube

- **fake-youtube:** a Go server that imitates the website endpoints we use (`browse`, `next`, `player`, watch page), plus RSS, the WebSub hub
  and the search API. It uses generated data and reproduces:
  - continuations;
  - "Newest first" ordering;
  - relative times and abbreviated counts;
  - pinned and hearted comments, edits and deletions over time;
  - block responses (429, the "confirm you're not a bot" page) and layout changes;
  - search quota.
- **Unit tests:** parsers, time and count estimates, scheduler, proxy manager (cooldowns, speed changes), quota governor, scoring,
  pseudonymization, suppression.
- **Integration tests** (Docker Compose in CI): end to end on fake-youtube, checking that:
  - discovery promotes a channel;
  - backfill runs;
  - a new upload arrives by push and is fetched;
  - comment edits and deletions are detected;
  - **block signals cause cooldown and slowdown, not retries elsewhere**;
  - erased data never returns;
  - the search quota ceiling holds.
- **No CI test touches real YouTube.**

### 8.13 Monitoring

- **Metrics (Prometheus):**
  - requests by endpoint, IP and result; block signals per IP and endpoint per hour; IPs in cooldown; the global speed multiplier;
  - parse failures; field fill rates;
  - detection delay per layer; freshness per tier (share of entities checked on time); comment completeness;
  - search quota used, and its forecast to 11:00 / 10:00 Iraq time;
  - discovery: candidates, promotions and rejections per day;
  - backlog and oldest due job;
  - erasure request age; suppression violations (must be 0).
- **Alerts:**
  - any IP-wide pause;
  - block rate above the threshold;
  - a fill-rate drop or parse-failure spike;
  - freshness targets missed for 30 min;
  - WebSub renewals failing;
  - search quota exhausted early;
  - an erasure request older than 48 h;
  - any suppression violation.
- **Dashboards (Grafana):** scraping health, freshness, discovery, comments, privacy.

---

## 9. Phase 0: measure before building

**Goal:** measure the numbers that decide cost and design:

- how fast we can go per IP without block signals;
- which fields really come back, and how accurately;
- bytes and requests per item;
- comment completeness;
- how much keyword search really finds.

Everything stays strictly within §3: we stop at the first block signal and record it.

**Duration:** about **2 weeks**, because edit/deletion detection needs a re-scan a week later.

### 9.1 You provide

- **30–50 Iraqi channels** of different sizes and types: news, entertainment, Shorts-heavy, a channel that streams live, Arabic and
  Kurdish;
- **10–20 keyword groups**;
- the API key file (steps in §9.3);
- one server;
- **3–5 test proxy IPs of different kinds**: datacenter, ISP, and optionally a small residential trial;
- optionally, a public HTTPS subdomain for push notifications.

### 9.2 I provide (1–2 small draft pull requests after approval)

- a `ytprobe` command-line tool;
- the minimal fake-youtube for its tests;
- the `skeleton` scrubber;
- a report template.

Raw data stays **only on your server** in `/var/lib/securebrand-youtube/phase0` and is deleted automatically after 14 days. The report
contains only aggregates.

### Experiments

| # | Experiment | What we learn | Decision it feeds |
|---|---|---|---|
| E1 | **Block-rate curve** per endpoint (`browse`, `next`, `player`, watch page, RSS, image CDN) × IP type × pace (1 request per 10 s, 5 s, 3 s, 2 s). We stop at the first block signal on each IP and measure time to recovery | Safe pace per IP and per endpoint; whether datacenter IPs are enough | Proxy count, type and cost |
| E2 | **Field coverage**: 200 videos, 50 channels | Which fields come back: tags, category, publish date-time, exact likes, chapters, links, country | Field list; whether we need the player endpoint |
| E3 | **Accuracy** (only if Q13 approved): the same 200 videos via one official API call batch (≈ 10 units) | Scraped vs official views, likes, comment counts, publish times | Trust levels per field |
| E4 | **Cost per item** | Requests and bytes per channel backfill, per video, per 100 comments, per post | Capacity model, bandwidth |
| E5 | **Comments** | Completeness against the displayed count; whether "Newest first" stop-early works; replies; pinned/hearted detection; like abbreviation rules; publish-time precision | Comment engine settings |
| E6 | **Edits and deletions**: re-sweep the same videos after 7 days | Edit and delete rates; the two-miss confirmation rule | Sweep schedule |
| E7 | **New-upload detection** over 14 days | Delay and miss rate of WebSub vs RSS vs tab checks; RSS error rate; **how fresh RSS view counts are** | Tier intervals |
| E8 | **Search yield** for each keyword group | Results per call, new channels per day, **relevance precision** (we hand-label 100 candidates), overlap between groups. If Q2 is approved, scraped search vs API search | Keyword grouping, scoring thresholds, whether scraped search is worth it |
| E9 | **Transcripts within the rules** | Does any transcript endpoint answer **without** anti-bot tokens from our IPs? If not, transcripts are dropped | Transcript go/no-go |
| E10 | **Parser robustness** | Parse-failure rate; client-version auto-detection | Maintenance estimate |

**Phase 0 result:** `docs/phase0-results.md` (aggregates only), containing:

- the measured safe pace and the proxy recommendation;
- the updated request, cost and storage model;
- the final field list with accuracy notes;
- tier schedules;
- the discovery settings;
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

8. Proxy credentials go into a file in the same folder, one proxy URL per line, in the form `http://user:password@host:port`. Create it
   with an editor:

```bash
sudo nano /etc/securebrand-youtube/proxies.txt
```

9. Make the proxy file readable by its owner only:

```bash
sudo chmod 600 /etc/securebrand-youtube/proxies.txt
```

I'll double-check these exact `gcloud` flags when we start Phase 0. If a command fails, send me the error message, but never a key or
password.

---

## 10. Delivery plan after Phase 0

These are small draft pull requests, each with unit and integration tests. I review each one and merge it when CI is green.

1. Repo skeleton: Go module, CI (lint, tests, gitleaks, fixture scanner), Docker Compose, `.gitignore`, README.
2. Phase 0 kit: `ytprobe`, minimal fake-youtube, `skeleton` scrubber. → **Run Phase 0, publish results, adjust this plan.**
3. Database schema + migrations; privacy building blocks (HMAC, suppression check, retention settings).
4. Proxy manager: pool, per-IP budgets, cooldowns, global speed, block detection. Tested hard against fake-youtube.
5. InnerTube client + parsers (channel, tabs, video, player) with fill-rate metrics.
6. Scheduler + job queue + registry + admin CLI.
7. Channel and video collection, snapshots, change logs.
8. New-upload detection: WebSub receiver, RSS poller, tab checks.
9. Comment engine: newest-first scans, replies, pinned/hearted, edits, deletions, completeness, caps.
10. Community posts and playlists.
11. Search-runner + quota governor + discovery (scoring, review list, promotion, capacity check).
12. Retention jobs, erasure workflow, suppression audit job.
13. Media (thumbnails, avatars, banners).
14. Monitoring: metrics, dashboards, alerts, canaries.

---

## 11. Main risks

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| 1 | **YouTube's Terms (automated access, collecting identities)** | Legal claims, cease-and-desist | Your decision (D4); lawyer review; data minimisation; pseudonymization by default; aggregate reporting |
| 2 | **API key terminated** for being part of a scraping system | Search-based discovery stops | Free discovery sources (§5.2) keep working; optional scraped search as fallback |
| 3 | **Blocking tightens** (more bot checks, more anti-bot tokens) | Endpoints or fields lost; slower collection | Polite pacing, diverse IPs, per-endpoint switches; we accept losing a field rather than breaking your rules |
| 4 | **The player endpoint unavailable without anti-bot tokens** | No tags or category; maybe date-only publish times for old videos | Detected in Phase 0; the rest still works |
| 5 | **Website changes break parsers** | Gaps in data | Type-based parsing, fill-rate alerts, canaries, same-day fixes |
| 6 | **Relative comment times and abbreviated likes** | Lower precision than the official API | Frequent scans on new videos; precision stored per value; explained in reports |
| 7 | **Search quota is small** (≈ 100 calls/day) | Slower discovery | Keyword grouping; free discovery; optional scraped search |
| 8 | **Discovery floods the watch list** | Capacity and cost blow-up | Scoring, daily cap, capacity check, review list, dormant tier |
| 9 | Proxy provider issues (bad IPs, outages, unethical sourcing) | Instability, ethical exposure | 2–3 providers; health scoring; residential only from consent-based sources, capped |
| 10 | **GDPR exposure** (diaspora commenters) | Fines, complaints | DPIA, legitimate-interest assessment, Article 14 notice, retention, erasure + suppression, pseudonymization |
| 11 | Viral videos with huge comment volumes | Capacity spikes | Per-video caps, priority queues |
| 12 | Credential leak (API key, proxy passwords) | Abuse, cost | Files only, IP-restricted key, log redaction, gitleaks, rotation |

## 12. Open questions and things to verify

- Your answers to §1, especially **Q1 (rule interpretation)**, **Q2 (scraped search)**, **Q3–Q4 (keywords and relevance)** and **Q5 (target
  size)**.
- To verify in Phase 0:
  - safe pace per IP and endpoint;
  - whether the player endpoint works without anti-bot tokens;
  - whether transcripts work within the rules;
  - publish-time format for old videos;
  - like-count abbreviation rules;
  - whether total views and comment counts are exact;
  - how fresh RSS stats are;
  - items per tab page;
  - WebSub lease and deletion notifications;
  - API search yield.
- Legal:
  - lawyer review of the Terms and data-protection risks;
  - legitimate-interest assessment, DPIA, privacy notice;
  - Iraq law status;
  - transfers if the server is outside the EU and EU commenters are involved.

---

## Appendix A: options considered (from plan v1)

| Approach | Coverage | Cost at 5,000 channels | Reliability | Blocking risk | Terms/legal risk | Maintenance | Role in v2 |
|---|---|---|---|---|---|---|---|
| Official Data API v3 | High, but no community posts, pinned/hearted, transcripts or About links | $0, but **quota**: ~500k units/day needed for collection, far above the free 10k | High | None | Low | Low | **Search only** (D4) |
| Analytics API | Owned channels only | $0 | High | None | Low | Low–Med | **Not used** (D2) |
| Website endpoints (InnerTube), logged out | Very high (minus anti-bot-token endpoints) | Proxies $30–450/month | Medium (breakages) | Medium–High | **High** | **High** | **Main collection** (D4) |
| RSS + WebSub | New uploads + latest-15 stats | $0 | Medium + High | Low | Low | Low | **Detection** |
| yt-dlp | Similar to InnerTube | — | Low (frequent breakage) | High | High | High | **Reference code only**; its server use needs logins or anti-bot tokens |
| Paid vendors (ScrapeCreators, Apify, Bright Data…) | Similar to InnerTube | $27k+/month at comment scale | Medium | Vendor's | Medium | Low–Med | **Not used** (own scraper is far cheaper); possible emergency fallback |

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
