# SecureBrand YouTube collector: plan (draft for approval)

Status: **proposal, nothing built yet.** Nothing gets implemented until you approve this plan.
Last updated: 1 November 2026. All times are **Iraq time (UTC+3)**.

How to read this:

- Section 1 has my questions for you, all in one place. Until you answer, I've used the assumptions listed under them.
- Sections 2–4 cover what YouTube data we can realistically get, the ways to get it, and my recommendation.
- Sections 5–8 cover the rules that shape the design, the quota maths, what you need to provide (with costs), and the architecture.
- Section 9 is the small first experiment (Phase 0). Sections 10–12 cover the delivery order, risks and open questions.

Legend: **[verify]** marks a fact I could not check against Google's own pages, because `developers.google.com` is blocked from my
build sandbox. These facts come from secondary sources or my own knowledge. Phase 0 (or one manual read of the page) will confirm each one
before we rely on it.

---

## 0. The short version

- **Use YouTube's official Data API as the backbone.** It is free in money terms. It covers channels, videos, Shorts, live streams,
  comments, replies and playlists. Its limit is a daily "quota" of request units, not a bill.
- **Detect new uploads in three layers.** YouTube's official push notifications (WebSub) usually arrive within minutes. RSS polling is the
  backup. A periodic check of each channel's upload list catches anything both missed.
- **Some things you asked for are not in the official API.** These are community posts, "pinned" and "hearted" comment flags,
  transcripts of other people's videos, the links on a channel's About page, and stats from before we start watching a channel. The only
  ways to get them are scraping YouTube's website (against YouTube's Terms) or buying them from a scraping vendor. I recommend we start
  without them and decide field by field later.
- **Two YouTube API rules shape the whole design.**
  1. Data from the API must be **refreshed or deleted every 30 days**.
  2. Keeping **statistics history longer than 30 days**, or computing our own scores such as sentiment, needs YouTube's approval. Approved
     clients may keep this data for up to 36 months.
  Both requests go through the same "quota extension" form, and the review takes weeks. We should apply in week 1.
- **Cost estimate:** roughly **$75–340 per month** for about 1,000 channels on the recommended path, mostly for a server. No proxies are
  needed. Doing the same through paid scraping vendors would cost tens of thousands of dollars a month at that comment volume.
- **Phase 0 comes first:** a two-week measurement on 10–20 real channels to replace my estimates with real numbers.

---

## 1. Questions for you (all at once)

Your brief still has the placeholder "[the brands, creators or competitors we watch, and roughly how many channels]", so the first question
matters most.

**Scope and volume**

1. **Which channels, and how many?** Give a rough count per type: brands' own channels, creators, competitors. Also: how fast will the list
   grow?
2. **Third-party public channels only, or also our clients' own channels?** For clients' own channels, the owner can grant us access
   (OAuth). That unlocks the Analytics API: daily history back to the channel's creation, watch time and demographics. That data is the
   owner's private data, not public, so it would be an explicit exception to "public data only". Do you want that exception, for owner-
   authorized data only?
3. **Do we only watch listed channels, or also look for mentions of your brands on any channel?** YouTube search costs 100 quota units per
   request, against 1 unit for most other requests, so searching for mentions is the single biggest quota cost. Example: 50 keywords
   checked every 2 hours ≈ 60,000 units/day.
4. **Languages** (Arabic, Kurdish, English, others)? This affects search keywords and transcript options.

**Freshness and depth**

5. **How fast must new uploads be detected?** Minutes, under an hour, or same day?
6. **How fresh must view/like/comment counts be?** For example, hourly for the first 2 days after upload, then less often.
7. **How far back for each new channel?** Video list and current metadata are cheap for the full history. **Comments are the expensive
   part.** All comments ever, or only on videos from the last N months?
8. **How fast must new comments appear?** For example, within 1 hour on new videos and within a day on old ones.

**Content**

9. **Do you need video/audio files**, or only metadata and thumbnails? Downloading videos is explicitly against YouTube's Terms and raises
   copyright questions. I recommend metadata and thumbnails only.
10. **Transcripts:** how important are they, and for which analysis? For other people's videos the official API does not provide them (see
    §2).
11. **Community posts and pinned/hearted comment flags:** must-have or nice-to-have? Neither is in the official API.
12. **Live streams:** is the viewer count during the stream enough, or do you also need the **live chat**? Live chat is out of scope unless
    you ask for it.
13. **Deleted comments:** is it enough to record *that* a comment was deleted, and when? Keeping the deleted *text* conflicts with both the
    API rules and the privacy rules, so I recommend recording only the fact.

**Compliance and legal**

14. **Gaps policy:** for data the official API can't give, may we buy it from a vendor such as ScrapeCreators, Apify or Bright Data, after
    a legal check? Or do we stay official-API-only? (I recommend: official API only to start, then decide per field.)
15. **Statistics history:** OK to apply to YouTube for permission to store statistics for more than 30 days, and to compute derived metrics
    such as sentiment and scores? If they refuse, is a 30-day trend window acceptable?
16. **Privacy defaults:**
    - Comment retention: I propose 12 months.
    - Author pseudonymization: on by default, or opt-in per client?
    - Where do erasure requests come from, and who handles them?
    - Are any commenters in the EU or UK? If so, GDPR applies.
17. **Legal entity and public privacy policy URL.** YouTube's audit requires a public privacy policy. Do we have one that covers YouTube
    data?

**Infrastructure and budget**

18. **Budget per month?**
19. **Reuse the Facebook project's ("fbs") infrastructure?** That means the same Postgres, Redpanda, Valkey, S3 and analysis service. If
    yes, **please attach `Abdullah95k/securebrand-microservices` to this session** so I can check what fits instead of guessing. See §8.13
    for what I'd check.
20. **Servers:** what server(s) do you have, and in which country? Do you have a domain where we can expose one public HTTPS address? The
    push notifications need it.
21. **Google account:** do you already have a Google Cloud project or organization for SecureBrand? Creating the project and API key uses a
    Google account. That is not "logging in to scrape": the account only owns the project and key.
22. **Alerts:** where should alerts go (Telegram, email, Slack)?
23. **Consumers:** who reads the collected data? A dashboard, an internal API, or only the analysis service?

### Assumptions used until you answer

| Topic | Assumption |
|---|---|
| Channels | Three sizes modelled: **S = 100**, **M = 1,000** (main case), **L = 5,000** |
| Average channel | 600 videos in total (Shorts included), 15 new uploads/month, 300 comments per video (top-level + replies) |
| Comment window | Comments kept and refreshed for videos from the **last 12 months** |
| Freshness | New uploads: under 15 minutes. New-video stats: hourly for 48 hours. New comments on new videos: within 1 hour |
| Media | Metadata + thumbnails only; no video/audio files |
| Gap fields | Not collected in v1 |
| Owned channels | Not in v1; designed so they can be added later |

Real channels have a heavy tail: a few viral videos can have 100,000+ comments. Phase 0 measures the real spread.

---

## 2. What's realistic: field by field

✅ = official Data API, using only an API key (no login). 🔑 = only with the channel owner's authorization. ❌ = not in any official API.

### Channel

| Field | Official API | Notes |
|---|---|---|
| Name, description, country, creation date | ✅ `channels.list` (snippet) | 1 unit per call; up to 50 channels per call |
| Handle (@name) | ✅ `snippet.customUrl`; `forHandle` lookup turns a handle into a channel ID | |
| Subscriber, view, video counts | ✅ (statistics) | **Subscriber count is rounded to 3 significant figures** (1,234,567 → 1,230,000), and creators can hide it. So changes on big channels show up as steps, not smooth curves |
| Avatar, banner, keywords, trailer | ✅ (snippet thumbnails, brandingSettings) | |
| Links on the About page | ❌ | Only on the website. Links written in the description can be parsed out |
| Changes over time | ✅ we take snapshots from day 1 | **No past history** before we start watching. **Storing history beyond 30 days needs YouTube approval** (§5). Past history: Social Blade (paid) or the owner's Analytics 🔑 |

### Videos, Shorts, live streams

| Field | Official API | Notes |
|---|---|---|
| Title, description, publish time, duration, tags, category, language, thumbnails, made-for-kids, region restrictions, topics, "has captions" flag | ✅ `videos.list` | 1 unit per call; up to 50 videos per call |
| View, like, comment counts | ✅ | Dislikes have been hidden since 2021. We build "over time" ourselves from snapshots (30-day limit unless approved) |
| Is it a Short? | ❌ no official flag | Each channel has hidden playlists: `UUSH…` (Shorts), `UULF…` (long videos), `UULV…` (live streams). They are read through the official API but **undocumented** [verify in Phase 0]. Fallback: duration ≤ 3 minutes as a hint only |
| Live / upcoming / premiere | ✅ `liveBroadcastContent`, `liveStreamingDetails` | Scheduled/actual start and end times, concurrent viewers while live |
| Chapters | Partly | Chapters the creator wrote in the description: ✅ we parse them. YouTube's automatic chapters: ❌ |
| Paid-promotion flag | 🔑 | Only the video owner can read it |
| Unlisted / private videos | ❌ by our rules | Public data only |
| Full upload history | ✅ the channel's uploads playlist, 50 per call | Very large channels: check that the full list comes back [verify in Phase 0] |

### Captions and transcripts

| What | Official API | Notes |
|---|---|---|
| Whether captions exist | ✅ `contentDetails.caption` | |
| Caption text of **other people's** videos | ❌ | `captions.download` needs the video owner's authorization (about 200 units) [verify]. The website's transcript endpoint now needs "proof-of-origin" tokens produced by YouTube's anti-bot system (BotGuard), and blocks datacenter IPs. Getting past that would break our "no bot-check workarounds" rule. Remaining options: a vendor, or accept the gap |
| Caption text of **clients' own** videos | 🔑 `captions.download` | With the owner's OAuth grant |

### Comments and replies

| Field | Official API | Notes |
|---|---|---|
| Text, author name, author channel ID, author avatar, publish time, likes, reply count | ✅ `commentThreads.list`, `comments.list` | 1 unit per page of up to 100 |
| Replies | ✅ | A thread includes at most 5 replies; the full set comes from `comments.list?parentId=…` |
| Edits | ✅ derived | `updatedAt` later than `publishedAt` means edited. We compare on each refresh |
| Deletions | ✅ derived | A comment missing from a full re-scan is deleted or hidden. We record **that** it happened, not its text (Q13) |
| Creator replies | ✅ derived | Reply author = channel owner |
| Pinned / hearted by creator | ❌ | Website only (yt-dlp exposes `is_pinned` / `is_favorited`). Vendor or gap |
| Held for review / spam | ❌ | Not public; excluded even for owned channels |
| Comments turned off | ✅ | The API answers `commentsDisabled` |
| Comments on all of a channel's videos, newest first | ✅ `allThreadsRelatedToChannelId` | A cheap way to catch new comments on old videos [verify the ordering and coverage in Phase 0] |

### Community posts, playlists, new uploads, backfill

| What | Official API | Notes |
|---|---|---|
| Community posts | ❌ | Not in any official API. Website or vendor only |
| Playlists and their items | ✅ `playlists.list`, `playlistItems.list` | |
| New uploads, quickly | ✅ WebSub push (documented in the Data API guides) + RSS + uploads playlist | See §8.5 |
| Backfill per new channel | ✅ all videos and current metadata; all **currently visible** comments | ❌ past statistics, ❌ comments deleted before we arrived |

---

## 3. The ways to get the data, compared

The costs below are for the **M scenario (1,000 channels)** unless stated otherwise.

### 3.1 YouTube Data API v3 (official)

- **Gets:** everything ✅ in §2.
- **Can't get:** community posts, pinned/hearted flags, other people's caption text, About-page links, automatic chapters, an official
  Shorts flag, past statistics, dislikes.
- **Quota:**
  - 10,000 units per day per Google Cloud project by default.
  - Most reads cost 1 unit for up to 50 items (100 for comments).
  - `search.list` costs 100 units. `captions.list` costs 50.
  - Failed requests cost units too.
  - Quota resets at **midnight US Pacific time**: **11:00 Iraq time** until 13 March 2027, and **10:00 Iraq time** during US daylight saving
    (14 March – 6 November 2027).
- **Raising quota:**
  - Submit the *YouTube API Services – Audit and Quota Extension Form*. YouTube runs a compliance audit, with no fee. Reported waiting
    times range from weeks to months, and approval isn't guaranteed.
  - Spreading the load over several Google Cloud projects to multiply the quota is not allowed. Google has warned developers about "multiple
    projects acting as one" and threatened to restrict them.
  - Since **1 June 2026** the same form is where you apply for derived metrics and longer statistics storage (§5).
- **Money cost:** $0.
- **Reliability:** high. The API is documented and versioned; expect occasional 5xx errors that retries handle.
- **Blocking risk:** none in the usual sense. The limit is quota, not IP blocks. Misusing the key can get it suspended.
- **Terms and legal:** permitted use, with obligations:
  - refresh or delete data every 30 days;
  - limits on storing statistics history;
  - a public privacy policy;
  - periodic audits.
  The API terms also prohibit scraping, so mixing scraped data into the same system is an audit risk [verify exact wording]. Comment data
  is still personal data under GDPR.
- **Maintenance:** low.

### 3.2 YouTube Analytics API + Reporting API (owned channels only)

- **Gets, with the owner's OAuth grant:**
  - daily history back to channel creation: views, watch time, average view duration, subscribers gained and lost, likes, shares,
    comments, traffic sources, geography, aggregated demographics;
  - bulk daily reports from the Reporting API [verify which reports include impressions and click-through rate];
  - owner-only Data API features: caption download and the paid-promotion flag.
- **Can't get:** anything about channels whose owner hasn't granted access.
- **Quota:** separate from the Data API, and generous. **Cost:** $0.
- **Reliability:** high. Owners can revoke the grant at any time; we must notice and delete their data.
- **Terms and legal:** the cleanest option. It is "Authorized Data": we can keep it while the grant is valid, re-checking at least every 30
  days. But it is **not public data**, so it needs your explicit exception (Q2).
- **Extra step:** Google must verify our OAuth consent screen for the YouTube read-only scopes [verify current scope classification]. That
  takes time. Until then we can run in "testing" mode with up to 100 users.
- **Maintenance:** low to moderate: the consent flow and encrypted token storage.

### 3.3 YouTube's website endpoints, logged out ("InnerTube", the `youtubei/v1/...` calls the website makes)

- **Gets:** almost everything the website shows: community posts, pinned/hearted flags, About links, automatic chapters, Shorts tabs, and
  newest-first comments.
- **Can't get within our rules:** transcripts and video streams. These now need anti-bot "proof-of-origin" tokens, and datacenter IPs get
  "Sign in to confirm you're not a bot". The workarounds are generating BotGuard tokens (getting past a bot check) or using logged-in
  cookies (logging in). **Both are forbidden by your rules.**
- **Cost:** no fee. But at M volume from one server we would be throttled and blocked. Residential proxies would cost about **$1,000–5,000
  per month** (300–600 GB at roughly $3–8/GB), plus engineering time.
- **Quota:** none published. Limits are adaptive and undocumented.
- **Reliability:** low to medium. The internal JSON changes often and without notice, and YouTube runs A/B experiments on it.
- **Blocking risk:** high from datacenter IPs, escalating to bot checks.
- **Terms and legal:** **YouTube's Terms of Service forbid automated access** (except search engines, or with written permission). They
  also forbid collecting information that identifies people, such as usernames, and that is exactly what comment collection does. Risks:
  breach-of-contract claims, IP bans, and **putting our official API access at risk** if the audit finds scraping. GDPR still applies.
- **Maintenance:** high.

### 3.4 RSS feeds and WebSub push (new uploads)

- **RSS** (`https://www.youtube.com/feeds/videos.xml?channel_id=UC…`):
  - Gets the latest ~15 uploads per channel (Shorts and scheduled streams included [verify]): title, description, publish and update times,
    thumbnail, views and likes.
  - Free, no quota. Low blocking risk at about one fetch every 15 minutes per channel.
  - **Reliability is medium:** feed readers report bursts of 404 errors lasting hours, so it can't be our only signal.
  - Terms risk is low: it is a public feed meant for feed readers, though technically still automated access.
- **WebSub push:** documented in the Data API guides. Hub: `https://pubsubhubbub.appspot.com/`. Topic:
  `https://www.youtube.com/xml/feeds/videos.xml?channel_id=UC…`.
  - YouTube POSTs to us when a channel uploads a video, or changes a title or description. Deletions arrive as a separate
    `at:deleted-entry` element [verify].
  - Needs a public HTTPS address and lease renewal every few days. The lease length is not officially documented; projects use 5 days, and
    up to ~9.5 days is reported [verify].
  - Free. The most official and fastest signal.
- **Can't get:** older uploads, comments, channel stats.
- **Maintenance:** low.

### 3.5 yt-dlp and similar tools

- **Gets:** full metadata, chapters, subtitles (automatic ones too), comments with pinned/hearted/uploader flags, media files.
- **Can't get within our rules:** reliable operation from a server in 2026. "Sign in to confirm you're not a bot" errors are widespread. The
  documented fixes are browser cookies (logging in) or proof-of-origin token plugins (getting past a bot check). Both are excluded.
- **Cost:** free software. Proxies cost extra if used.
- **Reliability:** breaks often and needs updates almost weekly.
- **Terms and legal:** the same Terms problem as 3.3. Downloading media is also explicitly prohibited, and raises copyright issues.
- **Maintenance:** high.
- **Verdict:** not for production. This also covers using it to fill gaps.

### 3.6 Paid third-party data APIs

- **Examples:**
  - ScrapeCreators, which is already connected to your Claude workspace;
  - Apify "actors" (ready-made scrapers sold on Apify's marketplace);
  - Bright Data;
  - SerpApi, for search;
  - Social Blade Business API, for **past** channel statistics.
- **Gets:** the gaps: community posts, transcripts, pinned/hearted flags, About links, Shorts lists. Social Blade adds subscriber and view
  history from before we started.
- **Prices seen (Nov 2026, check at signup):**
  - Comments: Apify actors about **$0.40–2.50 per 1,000 comments**.
  - Bright Data: about **$1–1.50 per 1,000 records**.
  - ScrapeCreators: about 1 credit per request, around $1–2 per 1,000 requests depending on plan.
  - Social Blade: credits at **$0.06–0.50 each**.
- **Cost at our volume:**
  - As the backbone, comments alone (≈ 54 M comments, re-fetched monthly) would cost about **$27,000–80,000 per month**. Not viable.
  - For small gap volumes (community posts, transcripts for new videos only): about **$20–200 per month**.
- **Reliability:** medium. These vendors scrape YouTube too, so they have outages when YouTube changes things. Watch for vendor churn.
- **Blocking risk:** carried by the vendor.
- **Terms and legal:** the data still comes from scraping YouTube. We remain the **controller** for any personal data under GDPR (the party
  responsible for it). We still need a data processing agreement and a legal review. **Prefer them for non-personal fields** (community
  posts, transcripts) over commenter identities. It may also matter for the YouTube audit (§11).
- **Maintenance:** low to medium.

### 3.7 Scorecard

| Approach | Coverage | Cost at M | Reliability | Blocking risk | Terms/legal risk | Maintenance |
|---|---|---|---|---|---|---|
| Data API v3 | High (with gaps) | $0 | High | None (quota) | **Low** | Low |
| Analytics API (owned) | Owned channels only, deep | $0 | High | None | **Low** (needs consent) | Low–Med |
| InnerTube (logged out) | Very high in theory | $1k–5k/mo proxies | Low–Med | **High** | **High** | **High** |
| RSS + WebSub | New uploads only | $0 | Medium + High | Low | Low | Low |
| yt-dlp | Very high in theory | Proxies + time | Low | **High** | **High** | **High** |
| Paid vendors | Gaps (and more) | $20–200/mo for gaps; $27k+/mo as backbone | Medium | Vendor's | Medium | Low–Med |

---

## 4. Recommendation: official first, gaps decided per field

1. **The Data API v3 is the backbone** for channels, videos (Shorts and lives included), comments, replies and playlists. Use **one Google
   Cloud project**, and apply for a quota extension plus derived-metrics/statistics storage **in week 1**. This is on the critical path.
2. **New uploads use three layers:** WebSub push (fastest), then RSS polling (backup), then an uploads-playlist check through the API (catches
   anything missed, and anything that later became public).
3. **Clients' own channels:** the Analytics API, Reporting API and owner-authorized Data API calls, opt-in per client. This comes in a later
   phase and only if you approve the exception (Q2).
4. **Gaps are "not available" by default.** Optionally we add a **vendor adapter per field** behind an on/off switch, after legal sign-off.
   It would start with non-personal fields (community posts, transcripts) and keep vendor data labelled by source. **No InnerTube scraping
   and no yt-dlp in production.**
5. **No proxies.** The official path doesn't need them.

**Why:** this is the lowest legal risk, it costs nothing for data, and it is reliable and cheap to maintain. It also satisfies your rules by
design: there are no bot checks to work around and no logins. The scarce resource is **quota**, which we manage with smart scheduling (§8.6)
and the audit.

**What we give up:**

- community posts;
- pinned/hearted flags;
- transcripts of other people's videos;
- About-page links;
- statistics from before day 1.

Each can be added later through a vendor if you decide it's worth the cost and risk.

---

## 5. Rules that shape the design

### 5.1 YouTube API policies

1. **The 30-day rule.** Data we got from the API without the owner's authorization ("Non-Authorized Data") must be **refreshed or deleted
   at least every 30 days**. Google has flagged developers for keeping statistics longer [verify exact wording].
   - **Effect on design:** every stored comment, video and channel record is re-fetched on a rolling schedule of at most 28 days, or
     deleted.
   - Nice side effect: the re-fetch is also how we detect deletions and edits, and it makes our data respect people's own deletions
     automatically.
2. **Statistics history and derived metrics (policy added 4 May 2026, applications open since 1 June 2026):**
   - By default, statistics (views, likes, subscriber counts, comment counts) **must not be stored for more than 30 days**.
   - Our own metrics built from API data, such as scores, ratios and **sentiment analysis**, need permission.
   - Audited developers with an accepted analytics use case may store such statistics **and** derived metrics **for up to 36 months**.
     Derived metrics must be clearly labelled as ours, not YouTube's.
   - **Effect on design:**
     - the snapshot retention period is a setting: 30 days by default, 36 months once approved;
     - derived values are stored separately from YouTube values, with a "SecureBrand-calculated" label;
     - our analysis service's sentiment output on comments counts as a derived metric, so we must include it in the application.
3. **One project, no quota splitting.** Google has warned developers who split work across several projects.
4. **Privacy policy and audits.** We need a public privacy policy and must pass the audit before we can go above 10,000 units/day.
5. **Deleted or private videos.** When a video or comment is no longer public, we delete its stored content and keep only a tombstone with
   no personal data.

### 5.2 YouTube Terms of Service (website)

These forbid automated access (except search engines, or with written permission) and forbid **collecting information that identifies
people** without their permission. This is why the recommendation keeps scraping out.

### 5.3 Personal data (your rules plus GDPR)

- Commenter names, IDs, avatars and comment text are personal data. So is a channel run by an individual creator.
- **Legal basis:**
  - most likely legitimate interest, which requires a written balancing test (a legitimate-interest assessment);
  - a **data protection impact assessment** (DPIA), recommended because this is large-scale monitoring;
  - a public notice explaining the collection (GDPR Article 14).
- Comments can reveal sensitive things (health, religion, politics). We do no special-category profiling of individuals, and the analysis
  should report **aggregates**.
- Iraq: as far as I know there is no comprehensive data protection law in force yet. Please confirm with your lawyer.
- If commenters include people in the EU or UK and the server is outside the EU, we need a lawful transfer mechanism. Server location is in
  Q20.

---

## 6. Quota and volume model (estimates, to be replaced by Phase 0)

### 6.1 Cost per average channel

The average channel has 600 videos, 15 uploads per month and 300 comments per video. Comments are kept for 12 months' worth of videos, which
is about 180 videos per channel.

**One-time backfill when a channel is added: ≈ 750 units**

| Task | How | Units |
|---|---|---|
| Video list + metadata | 12 calls to the uploads playlist + 12 `videos.list` calls | ~24 |
| Comments for 180 videos | ~4 units per video for a full sweep (threads + extra replies) | ~720 |
| Comments for all 600 videos (if you want everything) | | ~2,400 |

**Ongoing, per day: ≈ 90–100 units**

| Task | Schedule | Units/day |
|---|---|---|
| Channel snapshot | Daily (hourly for priority channels); 50 channels per call | < 1 |
| Uploads-playlist check | Every 6 h | 4 |
| Video stats | Tiered by age (§8.6); 50 videos per call, mixed across channels | ~6 |
| New-comment scans | Channel-wide every 2 h + new videos hourly for 48 h + slower afterwards | ~50 |
| Full comment sweeps | Every video in the window at most every 28 days (30-day rule), plus sweeps at 48 h and 7 days | ~30 |
| Playlists | Weekly | < 1 |

### 6.2 Scenarios

| | S (100 channels) | M (1,000) | L (5,000) |
|---|---|---|---|
| Ongoing units/day | ~10,000 | ~100,000 | ~500,000 |
| One-time backfill (12-month comment window) | ~75,000 | ~750,000 | ~3,750,000 |
| Fits the default 10,000/day? | Barely, with lighter schedules, no backfill headroom | **No**: needs extension | **No**: needs extension |
| Quota to request (with ~50% headroom) | 20,000–30,000 | ~150,000 | ~750,000 |
| Brand-mention search (optional, Q3) | +100 units per search call: 50 keywords every 2 h ≈ +60,000/day | same | same |

**Heavy-tail warning:** one video with 100,000 comments costs about 1,200 units per full sweep. Section 8.7 covers per-video caps, and Phase 0
measures the real spread.

### 6.3 Storage (M scenario)

| Data | Size estimate |
|---|---|
| Comments (≈ 54 M rows, 12-month window) | ~30–40 GB in Postgres, indexes included |
| Videos (600 k) and channels | ~2–3 GB |
| Video/channel snapshots | ~25 MB/day → ~1 GB at 30 days, ~27 GB at 36 months |
| Raw API responses (compressed, kept 7 days, for debugging) | ~10 GB in S3 |
| Thumbnails for new videos only (for analysis) | ~1.5 GB/month |

---

## 7. What you need to provide, and the monthly cost

### 7.1 Requirements list

| # | Item | Needed for | Notes |
|---|---|---|---|
| 1 | **Google Cloud project** with **YouTube Data API v3** enabled | Phase 0 onward | Free; no billing account needed for the Data API |
| 2 | **API key**, restricted to YouTube Data API v3 and your server's IP | Phase 0 onward | Stored in a file on the server (§7.3); never in chat or the repo |
| 3 | **Quota extension + derived-metrics/storage application** | Before scaling beyond ~100 channels | Needs the items in §7.4. Start in week 1 |
| 4 | **Server** (Linux, Docker) | Phase 0: any small VPS. Production: see 7.2 | Or reuse the fbs servers (Q19) |
| 5 | **Public HTTPS address** (a subdomain such as `yt-hooks.example.com`, with Let's Encrypt) | WebSub push | Phase 0 can run without it (RSS only) |
| 6 | **S3-compatible object storage** | Raw responses (7 days), thumbnails | Or the existing fbs bucket |
| 7 | **Postgres 16+** | Main database | Self-hosted or the fbs instance |
| 8 | Offsite **backups** | Production | 30-day rolling |
| 9 | **Alert destination** | Production | Telegram, email or Slack |
| 10 | **Privacy policy page** + legitimate-interest assessment + DPIA | YouTube audit, GDPR | Legal/ops work, not engineering |
| 11 | Optional: **OAuth consent screen** + Analytics API | Owned channels (later) | Needs Google verification |
| 12 | Optional: **vendor account** | Gap fields (later) | Only if approved |
| — | **Proxies** | **Not needed** | The recommended path uses only official endpoints |

### 7.2 Estimated monthly cost (USD, check prices at purchase)

| Item | S (100) | M (1,000) | L (5,000) |
|---|---|---|---|
| YouTube Data API | $0 | $0 | $0 |
| Server(s) | $25–50 (4 vCPU, 16 GB, 200 GB NVMe) | $60–120 (8 vCPU, 32 GB, 500 GB NVMe) | $200–400 (2–3 servers, or bigger + managed DB) |
| Object storage | ~$5 | ~$5–10 | ~$20–40 |
| Offsite backups | ~$5 | ~$10 | ~$30 |
| Domain/TLS | $0 (existing domain + Let's Encrypt) | $0 | $0 |
| Monitoring (self-hosted Prometheus + Grafana) | $0 | $0 | $0 |
| Optional gap vendor | $0–50 | $50–200 | $200–800 |
| **Total** | **~$35–110** | **~$75–340** | **~$250–1,300** |

If we reuse the fbs servers and storage, the server line may shrink to nearly zero.

For comparison, at M scale:

- InnerTube scraping with proxies would add about $1,000–5,000 per month;
- a vendor-only approach would cost about $27,000–80,000 per month.

### 7.3 How credentials are handled (applies from Phase 0)

- The API key lives in a file such as `/etc/securebrand-youtube/youtube_api_key`, readable only by the service user (mode `600`). The
  service reads the file's path from an environment variable (`YT_API_KEY_FILE`).
- It is sent in the `X-Goog-Api-Key` **header**, not in the URL, so it can't leak through logged URLs. The logger redacts that header.
- The key is restricted in Google Cloud to the Data API and your server IP. It can be rotated at any time.
- CI runs a secret scanner on every pull request (gitleaks). `.gitignore` blocks `*.key`, `.env*`, and the data and response folders.

### 7.4 Preparing the quota extension application (week 1)

Have ready:

- the Google Cloud project number;
- a short description of SecureBrand's use case (brand monitoring, aggregated reporting);
- the expected volume (from Phase 0);
- the public privacy policy URL and terms URL;
- a description of retention and deletion (§8.9);
- screenshots or a screencast of how the data is shown;
- an explicit **request for derived metrics (sentiment, scores) and statistics storage up to 36 months**.

Form: <https://support.google.com/youtube/contact/yt_api_form>

---

## 8. Architecture proposal

### 8.1 Overview

```
                 ┌──────────────┐  WebSub POST   ┌──────────────────┐
  YouTube hub ──►│ websub-recv  │───────────────►│                  │
                 └──────────────┘                │                  │
                 ┌──────────────┐  new video IDs │   scheduler      │  due jobs   ┌───────────────┐
  RSS feeds ────►│ rss-poller   │───────────────►│ (per-entity next │────────────►│ api-fetcher   │──► YouTube Data API
                 └──────────────┘                │  check times,    │  (priority  │ (quota-aware, │    (one project,
                 ┌──────────────┐  add/remove    │  priorities)     │   queues)   │  batching,    │     header key)
  Admin CLI/API ►│ registry     │───────────────►│                  │             │  retries)     │
                 └──────────────┘                └────────▲─────────┘             └──────┬────────┘
                                                          │ quota budget                 │ raw (7 days)   ┌────┐
                                                 ┌────────┴─────────┐                    ├───────────────►│ S3 │
                                                 │ quota-governor   │◄── units spent ────┤                └────┘
                                                 └──────────────────┘                    │ normalized records
                                                                                         ▼
  ┌──────────────┐  erase / retain   ┌───────────────────────────────────────────────────────────┐
  │ privacy      │──────────────────►│ writer: upsert, snapshots, diff (edits, deletions),       │
  │ (retention,  │◄── suppression ───│ suppression filter BEFORE write                           │──► Postgres
  │  erasure)    │                   └───────────────────────────────┬───────────────────────────┘
  └──────────────┘                                                   │ events (new video, new comments, erasure)
                                                                     ▼
                                                         analysis service (fbs) / dashboards
```

### 8.2 Services

All services are written in Go, to match fbs; I'll confirm once that repo is attached.

| Service | Job |
|---|---|
| **registry** | Add or remove watched channels by URL, handle or ID, resolving a handle to its `UC…` ID for 1 unit. Holds per-channel settings: tier (priority/standard), client, owned or not, privacy mode, comment window |
| **scheduler** | Keeps a "next check time" for every channel, video and comment stream (§8.6). Emits due jobs by priority. New-video signals jump the queue |
| **quota-governor** | Tracks units spent per US-Pacific day (resetting at 11:00 Iraq time in winter, 10:00 in summer). Reserves budget per priority. Pauses everything on `quotaExceeded` until the reset |
| **api-fetcher** | Runs Data API calls: batches 50 IDs per call, follows page tokens, retries, uses circuit breakers. Writes compressed raw responses to S3 (deleted after 7 days) and normalized records to the writer |
| **websub-receiver** | Public HTTPS endpoint. Answers the hub's verification challenge, checks the HMAC signature (`hub.secret`), turns notifications into "check this video now" jobs, and renews leases before they expire |
| **rss-poller** | Backup detector. Fetches each feed every 15 min (priority) or 60 min (standard), using conditional requests (ETag/If-Modified-Since). Backs off on errors |
| **writer** | Upserts into Postgres, appends snapshots, compares old and new versions (title or description changes, comment edits and deletions), **drops anything on the suppression list before writing**, and publishes events |
| **privacy** | Retention jobs, pseudonymization, erasure requests, suppression list, and an audit log of every erasure |
| **media-handoff** | Fetches thumbnails (and avatars/banners when they change) for analysis, under the same retention as metadata. No video or audio files |
| **owned-channels** (later) | OAuth consent flow, encrypted token storage, Analytics and Reporting API jobs, and deletion when a grant is revoked |
| **gap adapters** (optional, later) | One adapter per vendor and field, each behind an on/off switch, labelling data with its source |

Kafka/Redpanda is **not required** at this volume: about 1–2 API calls per second, even at M. The job queue can live in Postgres (`SELECT … FOR
UPDATE SKIP LOCKED`). If the fbs analysis service already consumes from Redpanda, we publish events there for consistency (Q19).

### 8.3 Storage

- **Postgres:**
  - main tables: `channels`, `videos`, `comments`, `authors`, `playlists`, `playlist_items`;
  - `channel_snapshots` and `video_snapshots`, partitioned by month so old data can be dropped cheaply;
  - change logs: `video_changes`, `comment_versions`, `channel_changes`;
  - privacy: `suppression_list`, `erasure_requests`, `erasure_audit`;
  - operations: `entity_schedule`, `quota_ledger`, `dead_letters`.
- **Notable columns:**
  - `comments.author_key` holds the author's channel ID, or its keyed hash in pseudonymized mode;
  - `comments.pinned` and `comments.hearted` are nullable, where NULL means "unknown, not available from the official API";
  - every row has `source` (`yt_data_api`, `yt_analytics`, `vendor:<name>`) and `last_refreshed_at`, which the 30-day rule depends on;
  - derived metrics live in separate tables marked `secure_brand_calculated`.
- **S3:** raw responses (gzip, encrypted, 7-day expiry) and thumbnails (with retention). Nothing from S3 is ever committed.
- **Valkey** (optional): fast copies of the quota counters and the suppression set. Postgres stays the source of truth.

### 8.4 How each collection works

- **Channel:** `channels.list` with `part=snippet,statistics,brandingSettings,contentDetails,topicDetails`, 50 channels per call. Store the
  current values, append a snapshot, and log any changes.
- **Video list:** page through the uploads playlist; classify each video with the `UUSH`/`UULF`/`UULV` playlists (if Phase 0 confirms they
  work) and `liveStreamingDetails`.
- **Video details and stats:** `videos.list` with
  `part=snippet,contentDetails,statistics,liveStreamingDetails,topicDetails,status`, 50 videos per call. Chapters are parsed from the
  description.
- **Comments:** see §8.7.
- **Playlists:** `playlists.list?channelId=…` weekly, plus their items.

### 8.5 New-upload detection (three layers)

1. **WebSub push.** When it arrives, we call `videos.list` straight away. The target is detection within minutes.
2. **RSS poll** every 15 or 60 minutes per channel. Any video ID we haven't seen triggers a fetch.
3. **Uploads-playlist check** every 6 hours. It catches anything both layers missed, scheduled premieres, and videos that went from private
   to public.

We measure **detection delay** = our first-seen time − the video's `publishedAt`, for each layer.

### 8.6 How often things are re-checked (defaults, tuned after Phase 0)

| What | Age or state | Check every |
|---|---|---|
| Channel stats + metadata | Priority tier | 1 h |
| | Standard tier | 24 h |
| Video stats | 0–48 h after publish | 1 h |
| | 2–30 days | 6 h |
| | 30 days – 1 year | 24 h |
| | Older than 1 year | 7 days (and ≤ 28 days to satisfy the 30-day rule) |
| Live stream | Upcoming | Every 30 min until the scheduled start |
| | Live now | 5 min (viewer count) |
| New comments, per video | 0–48 h | 1 h (newest-first scan, stops at known comments) |
| | 2–7 days | 6 h |
| | 7–30 days | 24 h |
| New comments, whole channel | All videos | Every 2 h via `allThreadsRelatedToChannelId` |
| Full comment sweep | At 48 h and 7 days after publish, then rolling | ≤ 28 days for every video in the window |
| Playlists | | 7 days |
| Every stored record | | Refreshed within 28 days, or deleted (30-day rule) |

When quota runs short, things are dropped in this order: backfill first, then sweeps that aren't yet due, then older-video stats. New-upload
detection and new-video stats are never dropped.

**Budget split:**

| Priority | Work | Share of daily budget |
|---|---|---|
| P0 | New-upload detection, new-video stats | 20% reserved |
| P1 | New-comment scans | 30% |
| P2 | Required refresh sweeps | 30% |
| P3 | Backfill | Whatever is left |

### 8.7 Keeping comments complete and up to date

1. **Backfill (full sweep):**
   - call `commentThreads.list?videoId=…&order=time&maxResults=100&textFormat=plainText` and follow every page;
   - for each thread whose `totalReplyCount` is higher than the replies included, call `comments.list?parentId=…&maxResults=100` through
     all pages.
2. **New-comment scans:**
   - scan newest-first and stop once a page contains only threads we already know (with a small overlap for safety);
   - the channel-wide scan (`allThreadsRelatedToChannelId`) catches new comments on old videos for about 1 unit per 100 new comments.
3. **New replies on old threads:** whenever a scan or sweep shows a thread's `totalReplyCount` has changed, we fetch that thread's replies
   again.
4. **Edits:** if `updatedAt` is newer than what we stored, we save the new text as a new version and mark the comment edited. Old versions
   follow the same retention.
5. **Deletions:**
   - a comment missing from a completed full sweep is marked "missing";
   - a batch `comments.list?id=…` call (up to 50 IDs per call) confirms it;
   - if confirmed, we **delete its text and author data** and keep a tombstone (comment ID hash, video, deletion time), so you can still
     count deletions.
6. **Completeness check per video:** we compare `collected comments ÷ statistics.commentCount`. If that falls below 90% (the threshold is
   tuned in Phase 0), the video gets a re-sweep and an alert. Some gap is normal, because spam-filtered and held comments are counted but
   not public.
7. **Caps for huge videos:**
   - backfill at most N comments per video (for example 20,000, newest first);
   - after that, rely on new-comment scans;
   - the cap is a setting, and capped videos are flagged as "partial".
8. **Comments turned off:** we record the state. Stored comments are deleted at the next due refresh, because they are no longer public.

### 8.8 Rate limiting, retries and failures

- **Quota:** units are charged *before* each call is sent (each method's cost is known). A call is refused locally if its priority's budget
  is used up.
- **Speed:** a local limit of about 5 requests per second for the API, and at most 1 request per second across all RSS feeds.
- **Retries:**

  | Error | Action |
  |---|---|
  | Network errors, 5xx, `rateLimitExceeded` | Exponential backoff: 2 s, 4 s, 8 s, 16 s, 32 s, with random jitter; at most 5 attempts |
  | `quotaExceeded` | Stop all API work until the reset (11:00 or 10:00 Iraq time) |
  | `commentsDisabled`, `forbidden` | Record the state; no retry |
  | 404 / not found | Check again once; if still missing, treat as removed and delete the content |
  | 400 | Bug: send to the dead-letter table and raise an alert |

- **Circuit breakers:**
  - per API method: open after a high error rate, then half-open probes;
  - for RSS: if more than 20% of feeds fail at once, YouTube's feed service is probably down. We pause RSS and rely on the API checks.
- **When blocked:** with official endpoints only, "blocked" means quota. We never rotate IPs or work around bot checks. RSS errors make us
  slow down, never try harder.
- **Safe re-runs:** all writes are upserts keyed by YouTube IDs, so re-running a job is harmless.

### 8.9 Privacy: retention, pseudonymization, erasure

**Retention defaults** (settings; per client values can be shorter, never longer):

| Data | Kept for |
|---|---|
| Comments + author data | While the video is in the comment window, maximum 12 months from collection, and always refreshed within 28 days |
| Deleted comments | Content removed when the deletion is detected; tombstone only |
| Statistics snapshots, change history | 30 days (36 months only after YouTube approval) |
| Derived metrics (sentiment, scores) | Only after YouTube approval; up to 36 months |
| Raw API responses (S3) | 7 days |
| Event topics (if Redpanda) | 7 days |
| Logs | 14 days. Logs contain IDs only, never comment text or names |
| Backups | 30 days rolling. On restore, erasures are re-applied from the suppression list |

**Pseudonymization modes, per client or channel:**

| Mode | What we store |
|---|---|
| `full` | Author channel ID, name and avatar |
| `pseudonymous` (proposed default) | `author_key = HMAC-SHA256(secret, author_channel_id)`; no name, avatar or URL. We can still count unique authors and honour erasure by author. The secret lives in a file like the API key. Rotating it breaks the links between old and new keys, and that is documented |
| `text_only` | Comment text with no author key at all |
| `aggregates_only` | Counts and metrics only; no comment text |

**Erasure requests:**

| Kind | Input | What happens |
|---|---|---|
| By author | Channel URL, handle or ID (resolved to an ID through the API) | Delete all their comments, replies, versions and author row. Add a suppression entry, the keyed hash of the ID, so their future comments are dropped **before** storage |
| By channel | A watched channel, or a channel owner's request | Delete the channel, its videos, the comments on them, snapshots and media. Suppress the channel ID so it can't be re-added without an admin override with a recorded reason |
| By video / comment | ID | Delete and suppress |

How an erasure is processed:

- the request is logged in `erasure_requests`;
- target: primary stores cleaned **within 72 hours**;
- S3 raw responses age out within 7 days, and backups within 30;
- an `erasure.requested` event goes to the analysis service, which must delete too;
- completion is written to `erasure_audit`, with no personal data.

**The suppression list is checked everywhere:** in the writer before every insert, in backfills, in vendor adapters, and on backup
restores. A daily job also checks that nothing suppressed exists in the database. It should always count 0; anything else raises an alert.

### 8.10 The repo is public: hygiene

- No captured responses, no real IDs, no real comments in the repo, ever.
- Test fixtures are **generated** by a fixture builder that creates fake channels, videos and comments. Fake IDs use a recognisable pattern
  (for example `UCfake…`) and fake names.
- CI runs two checks on every pull request:
  - **gitleaks** for secrets;
  - a **fixture scanner** that fails on real-looking YouTube IDs outside the fake pattern, email addresses, phone numbers and `AIza…` keys.
- Phase 0 output goes to a server folder outside the repo, and only **aggregate numbers** go into the report.

### 8.11 Testing: a fake YouTube

- **fake-youtube:** a small Go server that imitates the Data API endpoints we use, the RSS feed and the WebSub hub. It includes:
  - pagination and page tokens;
  - quota accounting with real per-method costs;
  - errors (`quotaExceeded`, `commentsDisabled`, 404s, 5xx);
  - edits, deletions and replies appearing over time;
  - ETags.
- **Unit tests:** scheduler maths, quota governor, change detection, pseudonymization, suppression, and parsers (chapters, ISO 8601
  durations).
- **Integration tests** (Docker Compose in CI): Postgres + fake-youtube + the services. We check end to end:
  - backfill;
  - new upload via WebSub → fetched;
  - comment edits and deletions detected;
  - erasure followed by re-ingestion stays erased;
  - the quota limit is respected.
- **Contract checks** against the real API run only in Phase 0 and by hand. They compare response *shapes*, never store bodies, and are
  not part of CI.

### 8.12 Monitoring

- **Metrics (Prometheus):**
  - units spent by method and priority, and the forecast to the end of the quota day;
  - detection delay per layer;
  - freshness per schedule tier (share of entities checked on time);
  - comment completeness;
  - API errors by reason; RSS 404 rate;
  - WebSub lease expiry;
  - queue backlog and oldest due job;
  - erasure request age;
  - suppression violations (must be 0);
  - records older than 28 days without a refresh (must be 0).
- **Alerts:**
  - quota above 80% before 22:00 Iraq time;
  - any `quotaExceeded`;
  - a WebSub lease about to expire;
  - freshness targets missed for 30 minutes;
  - completeness below the threshold;
  - an erasure request older than 48 hours;
  - any suppression or 30-day violation.
- **Dashboards (Grafana):** quota, freshness, comments, privacy.

### 8.13 What I'd check in the fbs repo (once attached)

- Whether its Postgres, Redpanda, S3 and analysis-service handoff can be shared, and the event formats and topic names it uses.
- Whether its rate limiter and circuit breaker libraries can be reused for the quota governor.
- Its fake-Facebook test harness and Phase 0 kit, as templates.
- Its GDPR erasure flow, so a single "erase this person" request covers both platforms.
- Its proxy tiers, which are probably **not** needed here: YouTube's official API isn't IP-limited the way Facebook scraping is.

---

## 9. Phase 0: measure before building

**Goal:** replace the estimates in §6 and §7 with real numbers, and confirm or reject each **[verify]** item, using only official endpoints.
Duration: about **2 weeks**, because detecting edits and deletions needs a second sweep a week after the first.

**You provide:**

- 10–20 channels: a mix of small, medium and very large; Arabic and English; some with Shorts and live streams; one client-owned channel if
  possible;
- the Google Cloud project and API key file (steps below);
- a small server;
- optionally, a public HTTPS subdomain for the WebSub test.

**I provide (as 1–2 small draft pull requests after you approve):**

- a `ytprobe` command-line tool;
- the minimal fake-youtube for its tests;
- a report template.

The tool stores raw data **only on your server**, in `/var/lib/securebrand-youtube/phase0`, and deletes it automatically after 14 days.
The report contains **only counts and sizes**.

### Experiments

| # | Experiment | What we learn | Decision it feeds |
|---|---|---|---|
| E1 | Full metadata backfill per channel | Units and bytes per channel; whether the uploads list is complete compared with `videoCount` on very large channels | Backfill budget |
| E2 | Full comment sweep on ~50 videos of different sizes | Units per video, comments per unit, the spread (heavy tail), bytes per comment | Quota request, caps (§8.7) |
| E3 | Comment completeness | Collected vs `commentCount`; replies through `comments.list`; newest-first vs relevance coverage; how `allThreadsRelatedToChannelId` is ordered and what it covers | Completeness threshold, scan design |
| E4 | Edits and deletions | Re-sweep the same videos after 7 days: how many edits and deletions we detect, and whether batch ID confirmation works | Sweep frequency |
| E5 | New-upload detection | Delay and miss rate of WebSub vs RSS vs uploads playlist over 7–14 days; RSS error rate | Polling intervals |
| E6 | Shorts and live classification | Do the `UUSH`/`UULF`/`UULV` playlists work, and are they correct against a human check of 50 videos? | Shorts detection method |
| E7 | API behaviour | Does an ETag "not modified" reply (304) still cost quota? How often do view and subscriber counts actually change? How much rounding? | Refresh schedule |
| E8 | Quota maths check | Units spent compared with the governor's own accounting | Governor correctness |
| E9 (only if you approve) | Gap fields through a vendor trial | Coverage and cost for community posts, transcripts and pinned/hearted flags on ~10 videos, using free trial credits (for example ScrapeCreators) | Whether to add gap adapters |

For pinned/hearted flags, a person can also simply look at about 10 videos in a normal browser. That is not scraping, and it tells us how
often these flags matter.

**Not in Phase 0:** InnerTube scraping, yt-dlp, proxies, or anything logged in.

**Phase 0 result:** `docs/phase0-results.md`, containing aggregates only:

- the updated quota and cost model;
- the recommended quota number to request;
- the final schedule settings;
- a go / no-go for each gap field.

### Getting ready for Phase 0 (you run these; nothing secret is shown in chat)

You can do this in the Google Cloud Console instead. These are the command-line versions. Replace `securebrand-youtube` with a globally
unique project ID if it's taken, and `YOUR_SERVER_IP` with your server's public IP.

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
gcloud services api-keys create --project=securebrand-youtube --display-name="yt-collector" --api-target=service=youtube.googleapis.com --allowed-ips=YOUR_SERVER_IP
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

I'll double-check these exact flags against the current `gcloud` documentation when we start Phase 0. If one fails, tell me the error
message, but never the key.

---

## 10. Delivery plan after Phase 0

These are small draft pull requests, each with unit and integration tests. I review each one and merge it when CI is green.

1. Repo skeleton: Go module, CI (lint, tests, gitleaks, fixture scanner), Docker Compose, `.gitignore`, README.
2. `ytprobe` + minimal fake-youtube (Phase 0 kit). → **Run Phase 0, publish the results, adjust this plan.**
3. Database schema and migrations; privacy building blocks (HMAC, suppression check, retention settings).
4. Data API client: header key, batching, paging, retries, quota charging; plus the full fake-youtube.
5. Quota governor + scheduler + job queue.
6. Registry (add or remove channels, handle resolution) + admin command-line tool.
7. Channel and video collection + snapshots + change logs.
8. New-upload detection: WebSub receiver, RSS poller, playlist checks.
9. Comment engine: sweeps, scans, replies, edits, deletions, completeness, caps.
10. Retention jobs, erasure workflow, suppression audit job.
11. Media handoff (thumbnails) and events to the analysis service.
12. Monitoring: metrics, dashboards, alerts.
13. (If approved) owned channels: OAuth + Analytics/Reporting API.
14. (If approved) gap adapters, one per field and vendor.

---

## 11. Main risks

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| 1 | Quota extension **slow or refused** | Stuck at ~100 channels | Apply in week 1 with Phase 0 numbers; strict prioritisation; possibly a paid vendor for overflow (costly) |
| 2 | **Statistics storage / derived metrics refused** | Only 30 days of trends; sentiment on comments possibly not allowed under the API policies | Apply early and explain the use case; for owned channels, use the Analytics API (no 30-day limit); for history, license Social Blade data |
| 3 | We misread the policy (30-day rule, aggregation) | Audit failure | Legal review of the policy text; design is already conservative (refresh ≤ 28 days) |
| 4 | Any scraping, including via vendors, seen by YouTube | Lost API access, Terms claims | Official-only by default; vendor use per field, after legal review, labelled by source |
| 5 | GDPR exposure from commenter data | Fines, complaints | DPIA, legitimate-interest assessment, Article 14 notice, pseudonymization by default, retention limits, erasure + suppression, aggregates in reports |
| 6 | Comment completeness lower than counts suggest | Under-reporting | Completeness metric, re-sweeps, honest "partial" flags |
| 7 | Undocumented behaviours change (`UUSH` playlists, channel-wide comment ordering, RSS) | Wrong classification or missed items | Layered detection; fallbacks; contract checks |
| 8 | Viral videos with huge comment counts | Quota spikes | Per-video caps, budget reservations |
| 9 | Subscriber rounding | Coarse subscriber trends | Explain it in reports; for owned channels, Analytics gives exact gains and losses |
| 10 | API key leak | Quota theft, suspension | IP + API restriction, file storage, header-only sending, redaction, gitleaks, rotation |
| 11 | Tight coupling with fbs infrastructure | One project's outage affects the other | Separate database schema, credentials and quotas; shared services only where it clearly helps |
| 12 | Owned-channel OAuth verification takes long | Delays the Analytics features | Run in testing mode with ≤ 100 users first |

## 12. Open questions and things to verify

- Your answers to §1, especially **channel count**, **owned channels**, **mention search**, **gaps policy** and **budget**.
- **Attach the fbs repo?** (Q19)
- To verify against Google's pages, or in Phase 0:
  - the exact 30-day and statistics wording;
  - the `captions.download` cost and rules;
  - the cost and permissions of `captions.list`;
  - how `allThreadsRelatedToChannelId` is ordered and what it covers;
  - the `UUSH`/`UULF`/`UULV` playlists;
  - the WebSub lease length and deletion notifications;
  - whether a 304 "not modified" reply costs quota;
  - whether very large channels return their full uploads list;
  - whether the API terms forbid scraping, and their exact wording;
  - the current classification of the OAuth scopes.
- Legal:
  - legal basis and DPIA;
  - Iraq law status;
  - transfers if EU/UK commenters are involved;
  - whether derived analysis (sentiment, logo detection on thumbnails) falls under YouTube's derived-metrics rules.

---

## Sources

Google's developer pages could not be fetched from my sandbox; claims from secondary sources are marked **[verify]** above.

- YouTube Data API quota costs: [determine_quota_cost](https://developers.google.com/youtube/v3/determine_quota_cost),
  [OutlierKit quota guide](https://outlierkit.com/resources/youtube-api-quota/), [Blotato pricing guide](https://www.blotato.com/blog/youtube-api-pricing)
- Quota and compliance audits: [Quota and Compliance Audits](https://developers.google.com/youtube/v3/guides/quota_and_compliance_audits),
  [Audit and Quota Extension Form](https://support.google.com/youtube/contact/yt_api_form)
- Policies: [Developer Policies](https://developers.google.com/youtube/terms/developer-policies),
  [Derived metrics and data storage policy](https://developers.google.com/youtube/terms/derived-metrics-policy),
  [Revision history](https://developers.google.com/youtube/terms/revision-history),
  [Policies guide](https://developers.google.com/youtube/terms/developer-policies-guide)
- Multiple-project enforcement example: [Solar2D forum post](https://forums.solar2d.com/t/critical-problem-with-your-google-cloud-platform-api-project-appodeal/350374)
- YouTube Terms of Service: [youtube.com/t/terms](https://www.youtube.com/static?gl=US&template=terms)
- Bot checks and proof-of-origin tokens: [yt-dlp #15865](https://github.com/yt-dlp/yt-dlp/issues/15865),
  [yt-dlp #16147](https://github.com/yt-dlp/yt-dlp/issues/16147), [bgutil PO token provider](https://pypi.org/project/bgutil-ytdlp-pot-provider/2.0.2/),
  [youtube-transcript-api #592](https://github.com/jdepoix/youtube-transcript-api/issues/592), [#593](https://github.com/jdepoix/youtube-transcript-api/issues/593)
- RSS reliability: [Google AI forum: RSS 404s](https://discuss.ai.google.dev/t/youtube-rss-feed-endpoint-returns-404-errors/113379),
  [Feeder help](https://feeder.co/help/rss/youtube-feeds/)
- Playlist prefixes: [Decoding YouTube playlist IDs](https://simpledecisionmaker.onrender.com/blogs/decoding-youtube-playlist-ids/)
- yt-dlp comment fields: [yt-dlp commit mirror](https://bytes.keithhacks.cyou/etc/yt-dlp/commit/c35448b7b14113b35c4415dbfbf488c4731f006f)
- Vendor prices: [Bright Data YouTube scrapers](https://brightdata.com/blog/web-data/best-youtube-scrapers),
  [Apify YouTube comment scrapers](https://apify.com/apidojo/youtube-comments-scraper),
  [ScrapeCreators comment scrapers](https://scrapecreators.com/blog/youtube-comment-scrapers),
  [Social Blade Business API](https://socialblade.com/business-api)
- WebSub: [Invidious lease handling](https://git.linux.ucla.edu/lug/invidious/commit/7425700009b2e127775ba33b9b9cf661867ecc2e),
  [ytWatchBot push handler](https://github.com/Feverqwe/ytWatchBot/blob/f03e8c53b27ba651868d495e310340ce4e8e62d5/legacy/pushApi.js)
