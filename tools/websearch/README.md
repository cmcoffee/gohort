# websearch

Web search providers, article fetching, and shared research utilities used by all agents.

## Search Providers

| Provider | Function | API Key Required | Notes |
|----------|----------|-----------------|-------|
| DuckDuckGo | `searchDuckDuckGo` | No | Scrapes lite HTML interface, max 8 results |
| Brave | `searchBrave` | Yes | REST API, max 8 results |
| Google | `searchGoogle` | Yes (key:cx format) | Custom Search JSON API, max 8 results |
| SearXNG | `searchSearXNG` | No | Self-hosted instance, max 8 results |

Configured via `--setup`. `CrossProviderSearch` dispatches to the configured provider.

## Article Fetching

```go
// Basic text extraction
text, err := FetchArticle(url, maxChars)

// With metadata (author, date, site, domain)
text, meta, err := FetchArticleWithMeta(url, maxChars)
```

Features:
- HTML article extraction with nav/header/footer stripping
- PDF text extraction (FlateDecode streams)
- Metadata parsing (og:title, author, publication date)
- Size limits: 512KB HTML, 2MB PDF

## Shared Utilities

```go
// Credibility scoring (0-100) based on domain
score := DomainCredibility("nature.com") // 95

// Human-readable label
label := CredibilityLabel(score) // "High"

// Formatted metadata line for LLM context
meta := FormatArticleMeta(sourceMeta) // "[Author: ... | Credibility: High]"

// URL filtering for unfetchable sites
blocked := IsBlockedURL("youtube.com/...") // true

// Domain-diverse source selection
diverse := SelectDiverseArticles(sources, 7)

// Parse numbered search results into Source structs
sources := ParseSearchResults(resultText)
```

## Domain Discovery

```go
// Classify topic and return curated authoritative domains
domains := DiscoverDomains(topic, posFor, posAgainst, classifyFunc)
```

Classifies topics into categories and returns domain lists:

- **legal** — scholar.google.com, courtlistener.com, law.cornell.edu, supremecourt.gov
- **medical** — pubmed, WHO, CDC, Cochrane, FDA
- **economic** — BLS, Census, NBER, CBO, IMF, World Bank
- **scientific** — Nature, Science, arXiv, PNAS, NIH
- **criminal_justice** — BJS, Sentencing Commission, NIJ
- **environmental** — EPA, IPCC, NOAA, IEA
- **technology** — arXiv, ACM, IEEE, NIST, FTC, EFF
- **education** — Dept of Ed, NCES, OECD
- **military** — DoD, SIPRI, RAND, CBO, IISS
- **political** — Congress.gov, GAO, Brookings, RAND, Pew
