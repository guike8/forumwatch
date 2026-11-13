# Research notes: where to collect first

Research date: **2026-11-13**. This is a documented-source shortlist, **not** a live forum availability check, current activity ranking, endorsement of a mirror, or assertion that access is authorized. No underground sites were contacted, accounts created, or stolen data/attachments collected during this research.

## Main finding

"The most active exploit forums" cannot responsibly be supplied as a permanent list. Communities fragment, operators change, addresses are cloned, and much relevant commerce is not on forums or Tor at all. Build independent source adapters and validate access/identity before adding an endpoint.

Collection priorities should separate exploit/vulnerability discussions, access brokering, malware services, and breach claims. Those are related but not interchangeable signals.

## Candidate collection priorities

### 1. Exploit community: strong relevance lead; current availability unverified

A June 23, 2026 research submission, *Inside Crypter-as-a-Service*, studied exploit.in using approximately one million screened posts, narrowing to 491 threads and 2,949 posts. Its actual observation window was **January 2020–August 2025**, not 2026. It supports the forum's relevance to malware-service commerce and community intermediaries, not a claim that it currently leads zero-day sales.

The abstract describes ordinary-web and dark-web access. Obtain current endpoint authenticity and permissions independently; this repository intentionally includes no guessed addresses.

Source: https://arxiv.org/abs/2606.24226

### 2. XSS successor ecosystem: monitor migration, not a single old hostname

Flashpoint's **June 3, 2026** analysis describes fragmentation after the July 2025 XSS disruption:

- **Rehub:** emerging trading functionality and recruitment of established community participants.
- **DamageLib:** technical community formed by former moderators; described as prohibiting commercial transactions, so its signal is not equivalent to a marketplace.
- **Revived XSS (.pro variant):** restored historical material reportedly appeared in August 2025, but operator identity and community trust are disputed.
- **XSSF:** claimed connections are disputed; do not assume the name proves XSS continuity.

The report does not provide a measured present-day ranking or verified current registration/access conditions. Monitor source provenance and recurring identities. Claims of police control are community allegations, not independently established facts.

Source: https://flashpoint.io/blog/understanding-illicit-ecosystems-xss-russian-speaking-underground/

### 3. Dread: secondary Tor ecosystem context, not automatically an exploit priority

Flashpoint's historical overview describes Dread as a venue for darknet-market/vendor information. This supports its value for ecosystem changes, scams, outages, and migration—not the claim that it is a leading zero-day source. Current November 2026 availability and address authenticity were not established. Do not use an onion address copied from an arbitrary directory.

Source: https://flashpoint.io/blog/2023-year-in-review-blogs-commentary-press/

### Exclude stale recommendations pending independent revalidation

- **RAMP:** BleepingComputer reported FBI seizure notices and FBI-controlled DNS on **January 28, 2026**. This is reporting based on observed takeover indicators; the article did not provide an official seizure date or substantiate a rebuild plan. Do not configure the former forum as a known-active source.
- **LeakBase:** DOJ's **March 4, 2026** announcement documents the March 3–4 operation and seizure of forum data/domains. The announcement does not establish where participants migrated.
- **BreachForums clones:** a familiar name alone is not proof of operational continuity, authenticity, or an active community. No specific current instance was verified here.

Sources:
- https://www.bleepingcomputer.com/news/security/fbi-seizes-ramp-cybercrime-forum-used-by-ransomware-gangs/
- https://www.justice.gov/opa/pr/united-states-leads-dismantlement-one-worlds-largest-hacker-forums

## Do not make Tor coverage the whole strategy

Brandefense's 2026 exploit-trade report analyzed posts timestamped **January 1–October 27, 2026**. In its collected sample of **221 exploit-related offers/requests**, **195 were Telegram records and 25 were forum records**. That reflects the publisher's collection coverage, not the size or distribution of the entire criminal market.

The report explicitly warns of limited visibility into restricted forums, brokers, and Tor. Researchers did not purchase or execute the advertised offerings; "zero-day" terminology was not independently validated. This supports a future separate adapter for authorized messaging-channel collection, not a conclusion that all serious exploit trade happens on Telegram.

Source: https://brandefense.io/wp-content/uploads/cybercrime-report-2026/exploit-trade/exploit-trade-report-2026.html

## Source onboarding checklist

1. Verify address provenance using multiple independent, recent references or a trusted existing relationship. Never automatically follow a purported mirror migration.
2. Establish what public/authorized access reveals, source rules, allowed request rates, and whether collection is lawful for the operator.
3. Prefer a native new-post feed; otherwise build and test a source-specific adapter against sanitized fixtures.
4. Define coverage: new threads, new replies, edits, removals, pagination, and catch-up after downtime. The current generic HTML adapter does not provide all of these.
5. Record publication time when available, local first-seen time, fetch completion, durable-save time, and client receipt time. Source timestamps are untrusted.
6. Track precision, duplicate rates, missed-post windows, collector health, and measured delivery latency. Rank collected sources by relevant unique observations, not total advertised membership.
7. Store minimal excerpts/metadata, not stolen databases or executable attachments. Corroborate exploitation claims separately through trusted technical evidence.

## Low-latency implementation path

1. **Implemented skeleton:** HTTP/SOCKS fetching, per-source polling, conditional requests, dedup/persistence, private RSS, live SSE, health, and bounded responses.
2. **Next:** validated forum-specific adapters, authorized cookie/session support, fixture tests, reply identity/pagination, and a measured latency harness.
3. **Then:** SQLite with indexed retention and append-only durable events; cursor-based replay so reconnecting clients cannot miss items outside the latest-200 window.
4. **Optional:** authorized messaging-source ingestion and local push clients. Third-party intelligence can be clearly labeled fallback/corroboration, not the primary collection path.

SSE removes the reader's polling delay. It cannot remove upstream publication delays, source polling, slow Tor circuits, access challenges, or downtime. No "instant" delivery guarantee is made.
