# KYB V2: Business Verification for the onboarding flow

This is the version 2 of the document link to version 1 [KYB](/doc/69d68ae5-9b15-4b6e-aed3-1b807f5e0f1e) \nFigma design: @[https://www.figma.com/design/7jCMwDng7HF5g7F3tGXf0E/Open-HR?node-id=3530-4038&t=JbGwDSfrvYxysOhl-1](mention://85c272c1-9c90-409e-9e64-0c9cab58e607/url/7f28e93c-ea3f-4add-a87e-b904daf5e3f7)

## 1. Problem statement

Before a business can publish job listings on Open HR, we need reasonable confidence they're a real, registered, active company. This is corporate entity verification (KYB), not individual identity verification (KYC) we're confirming _the business exists and is in good standing_, not verifying the person signing up.

**Constraint that shapes every decision in this doc: Open HR is pre-revenue and bootstrapped.** We cannot pay per-lookup fees to commercial KYB aggregators (Middesk, Kyckr, Northdata, etc.) before we have the volume or revenue to justify it. The verification architecture must default to $0-cost paths and only escalate to paid options once a concrete, defined trigger is hit.

## 2. Goals

- Verify a business's registration status against an authoritative source (government registry) before allowing job publication.
- Default to free verification (official API or free manual portal check) for every supported market.
- Never block a user from _drafting_ a job while verification is pending or being corrected only block _publishing_.
- Give users a clear, specific reason when verification fails, and a fast path to fix it not a generic "failed" dead end.

## 3. Non-goals

- Full UBO (ultimate beneficial owner) resolution or AML/sanctions screening out of scope for this phase.
- Real-time verification in every market several markets have no free API, so "real-time" is only true for a subset (see Tier 1 below); everywhere else is asynchronous manual review, and the product must be honest about that in the UX.
- Financial statement / creditworthiness checks we're confirming the entity is real and active, not assessing its finances.

## 4. Verification architecture

### 4.1 Execution tiers

| Tier                           | Mechanism                                                                                                                                                                | Cost                             | When it applies                                                                                                    |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| **Tier 1: Free Official API**  | Direct REST/SOAP call to a government or fully-official open-data endpoint                                                                                               | $0                               | Automate immediately real-time verification at signup                                                              |
| **Tier 2: Free Manual Portal** | Official government search portal exists, but no API (or the free API has enough friction approval lag, low rate limits that Tier 1 automation isn't worth building yet) | $0 (ops time only)               | Status goes to `pending`; an operator checks the portal and approves/rejects                                       |
| **Tier 3: Paid Fallback**      | Commercial aggregator (Middesk, Kyckr, etc.) or a market with no free option at all (e.g. Indonesia)                                                                     | $ per lookup or flat monthly fee | Deferred until the trigger in §4.3 is hit or, for markets with no free option, used at low volume as the only path |

### 4.2 Field spec

Every market collects: **Business Name, Registered Address, Country of Registration**, plus one additional identifier field whose label, format, and verification method are country-specific (full table in the Appendix).

### 4.3 Trigger to revisit paid aggregators

Don't defer on a vague "later." Use a concrete trigger e.g. **30+ verifications/week**, or the point where tracked ops time on manual review exceeds what a flat-fee aggregator subscription (\~€400–1,000/mo) would cost. Below that line, Tier 1/Tier 2 is strictly the better trade.

---

## 5. Product requirements verification states & gating

### 5.1 Status states

| Status        | Meaning                                                           | Draft a job? | Publish a job? |
| ------------- | ----------------------------------------------------------------- | ------------ | -------------- |
| `not_started` | User hasn't submitted registration info yet                       | Yes          | No             |
| `pending`     | Submitted awaiting Tier 1 automated check or Tier 2 manual review | Yes          | No             |
| `verified`    | Confirmed match against the official registry                     | Yes          | Yes            |
| `failed`      | Checked, did not match see failure taxonomy below                 | Yes          | No             |

**Gating rule:** drafting is always allowed; publishing requires `verified`. This lets users build out a job post while verification is in flight rather than losing work, without ever letting an unverified business go live.

### 5.2 Failure message (don't show a generic "failed" message) @[jerry](mention://3ba915ac-dfcf-4232-a4f9-7bacdd919020/user/1865153e-439c-48d6-bf00-7ec46f872984)

| Failure reason                                     | Likely cause                                                                  | Re-verification behavior                                                                                                                                                                            |
| -------------------------------------------------- | ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Registration number not found**                  | Typo, or wrong number for the selected country                                | Inline error on the field, expected format hint. Lightweight retry don't clear other fields                                                                                                         |
| **Company Name doesn't match registry record**     | Trading name (DBA) entered instead of legal name, or a typo                   | Show the registry's actual name on file next to what they entered; single-click "confirm this is us" to accept the legal name, or let them correct the number if it's genuinely a different company |
| **Address doesn't match**                          | Registered office vs. trading address legitimately differ (common, not fraud) | **Soft warning, not a hard block.** Ask the user to confirm which address type they entered; accept on confirmation                                                                                 |
| **Company found but dissolved/inactive/insolvent** | Real dissolution, or a defunct entity being reused fraudulently               | **Hard block no self-serve retry.** Disable resubmission; direct to support with a note that registry records show the entity as inactive                                                           |
| **Wrong country selected**                         | Format mismatch against the selected country's pattern                        | Distinct "change country" action (not a field edit) re-triggers the full country-specific field set without resetting unrelated fields                                                              |
| **Manual review still pending (Tier 2 markets)**   | No free API exists a human hasn't reviewed it yet                             | Not a failure. Separate "still reviewing" messaging, distinct tone from a failure email                                                                                                             |

### 5.3 Re-verification form

For the two fixable cases (number not found / name mismatch), re-verification should be **lighter than the original signup form**: just **Company Name + Registration Number**. Don't re-ask for address (rarely the actual problem) or country (unless the user explicitly chooses to change it) a shorter retry form reduces the odds of introducing a second typo on the field that already failed once.

### 5.4 Email lifecycle (when "user does nothing")

| Trigger               | Timing                      | Tone / content                                                                                                                              |
| --------------------- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| Verification failed   | Immediately                 | State the specific failure reason (not generic), CTA straight to the lightweight re-verification form, reassure that the draft job is saved |
| Manual review queued  | Immediately                 | Reassuring, not urgent "we're reviewing your details, job drafting is fully unlocked in the meantime"                                       |
| Re-engagement nudge 1 | +2–3 days, still unresolved | Value-focused remind them of the job post waiting in draft                                                                                  |
| Re-engagement nudge 2 | +7 days, still unresolved   | Final reminder mention publishing stays blocked, offer direct support contact. The user account should be deleted after 30 days             |

Stop the cadence once the user resubmits or reaches `verified`.

### 5.5 Dashboard display

- Persistent status badge: **Verified** (green) / **Pending Review** (amber) / **Verification Action Required** (orange) / **Verification Failed** (red, dissolved-entity case) / **Not Started** (grey)
- Clicking the badge opens a detail panel: what was submitted, the specific failure reason, and the matching CTA
- Any draft job shows a locked "Publish" button with a tooltip/banner explaining _why_ never hide the button silently

---

## 6. Appendix Country verification reference

### 6.1 Core markets

| Tier | Country           | Field label                                | Format                           | How to verify                                                                                                                                               | Gotcha                                                                                                                                                                           |
| ---- | ----------------- | ------------------------------------------ | -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1    | United Kingdom    | Company Registration Number (CRN)          | 12345678 / SC123456 / NI123456   | [Companies House Public Data API](https://developer.company-information.service.gov.uk/) free, requires API key registration                                | Uses Basic Auth; returns live status, officers, PSC                                                                                                                              |
| 2    | United States     | State of Incorporation + State File Number | e.g. Delaware File No. 1234567   | No national registry per-state free Secretary of State portal. [Delaware ICIS](https://icis.corp.delaware.gov) covers the large majority of startup signups | EIN cannot be verified via any public API must collect state + file number, not EIN alone. Don't pre-build all 50 states; add on demand                                          |
| 2    | Germany           | Handelsregisternummer                      | HRB 12345                        | [handelsregister.de](https://www.handelsregister.de) free basic search **since Aug 2022** (DiRUG law / EU Digitalization Directive)                         | No official API at any price. [handelsregister.ai](http://handelsregister.ai) / [companyhouse.de](http://companyhouse.de) are paid third-party wrappers, not government products |
| 2    | India             | GSTIN and CIN                              | GSTIN: 27AAAAA0000A1Z5           | [mca.gov.in](https://www.mca.gov.in) Master Data (CIN/LLPIN) + [gst.gov.in](https://www.gst.gov.in) Search Taxpayer (GSTIN), both free manual               | GSTIN and CIN for business and PAN for individual or sole traders                                                                                                                |
| 2    | Nigeria           | RC number                                  | RC 1234567                       | [search.cac.gov.ng](https://search.cac.gov.ng), free manual                                                                                                 | Portal has frequent downtime/rate-limiting budget extra review time                                                                                                              |
| 2    | Kenya             | BRS Registration Number + KRA PIN          | PVT-XXXXXX / BN-XXXXXX + KRA PIN | [BRS/eCitizen](https://brs.ecitizen.go.ke) + [KRA PIN Checker](https://itax.kra.go.ke/KRA-Portal/pinChecker.htm), both free                                 | BRS requires account creation to search; official extracts (CR12 forms) cost a government fee                                                                                    |
|      | Additional for EU | VAT Number                                 | Country prefix + digits          | [VIES](https://ec.europa.eu/taxation_customs/vies/), free official                                                                                          | **Not a substitute for a national registry check** see §6.3                                                                                                                      |

### **6.2 EU-27**

| **Tier**         | **Country**    | **Field label**             | **Format**   | **How to verify**                                                                                                                     | **Gotcha**                                                                                                                                                                                                                    |
| ---------------- | -------------- | --------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **FREE API**     | Croatia        | MBS                         | 012345678    | [sudreg-data.gov.hr](https://sudreg-data.gov.hr/) — free official API                                                                 | —                                                                                                                                                                                                                             |
| **FREE API**     | Czech Republic | IČO                         | 12345678     | [ARES REST API](https://ares.gov.cz/swagger-ui/) — free, no auth required                                                             | Best-in-class free gov API                                                                                                                                                                                                    |
| **FREE API**     | Denmark        | CVR-nummer                  | 12345678     | [VIRK System-til-System](https://datacvr.virk.dk/artikel/system-til-system-adgang-til-cvr-data) — free official API, requires account | **cvrapi.dk is an unofficial third-party wrapper, not the government API** — don't treat it as authoritative                                                                                                                  |
| **FREE API**     | Estonia        | Registrikood                | 12345678     | [e-Business Register Open Data API](https://avaandmed.ariregister.rik.ee/) — free official                                            | —                                                                                                                                                                                                                             |
| **FREE API**     | Finland        | Y-tunnus                    | 1234567-8    | [PRH Avoindata (YTJ API v3)](https://avoindata.prh.fi/) — free, no authentication required                                            | —                                                                                                                                                                                                                             |
| **FREE API**     | France         | SIREN                       | 123456789    | [INSEE Sirene API](https://api.insee.fr/) — free via OAuth2, 30 req/min                                                               | Official K-bis paper extracts cost money; raw API data is 100% free                                                                                                                                                           |
| **FREE API**     | Ireland        | CRO Number                  | 123456       | [CRO Open Data API](https://opendata.cro.ie/) — free after developer registration                                                     | Certified downloads carry standard fees                                                                                                                                                                                       |
| **FREE API**\*\* | Latvia         | Reģistrācijas numurs        | 40003123456  | [ur.gov.lv Registry API/web services](https://www.ur.gov.lv/en/registry-api-web-services-service-list) — free, official               | **Real free API, but requires an application** (\~5–10 business days for approval + certificate registration) not instant self-serve like the others in this group. Worth building, just budget lead time                     |
| **FREE API**\*\* | Slovakia       | IČO                         | 12345678     | [RPO API (api.statistics.sk/rpo)](https://slovak.statistics.sk/) — free, official, CC-BY 4.0                                          | **Corrects earlier guidance** — this is a genuine free API, distinct from orsr.sk (which has no API). [ORSF](https://orsf.sk/en/api) is a free no-key aggregator of 14 Slovak registries if a single-call option is preferred |
| **FREE API**     | Slovenia       | Matična številka            | 1234567000   | [AJPES](https://www.ajpes.si/) — free, official (restPrsInfo API relaunched Feb 2026)                                                 | Basic web services need account registration                                                                                                                                                                                  |
| MANUAL           | Austria        | Firmenbuchnummer            | FN 123456a   | [Justiz-Online Firmenbuch](https://justizonline.gv.at/jop/web/firmenbuchabfrage), free manual                                         | No free API                                                                                                                                                                                                                   |
| MANUAL\*         | Belgium        | Ondernemingsnummer          | 0123.456.789 | [KBO Public Search](https://kbopub.economie.fgov.be/), free manual, no account                                                        | **The official webservice API is paid, not free** — free-tier third-party wrappers (CBEAPI, CompanyBelgium) exist if automation is wanted before Tier 3 budget                                                                |
| MANUAL           | Bulgaria       | ЕИК / Булстат               | 130456789    | [portal.registryagency.bg](https://portal.registryagency.bg/), free manual                                                            | Cyrillic-only interface                                                                                                                                                                                                       |
| MANUAL           | Cyprus         | HE Number                   | HE123456     | [Registrar of Companies e-Filing](https://efiling.drcor.mcit.gov.cy/), free manual (basic)                                            | Certificate downloads cost a fee                                                                                                                                                                                              |
| MANUAL           | Germany        | Handelsregisternummer       | HRB 12345    | See Core Markets above                                                                                                                | —                                                                                                                                                                                                                             |
| MANUAL           | Greece         | GEMI Number                 | 123456789000 | [GEMI Business Portal](https://businessportal.gr/), free manual                                                                       | Automated web services restricted to public-sector nodes                                                                                                                                                                      |
| MANUAL           | Hungary        | Cégjegyzékszám              | 01-09-123456 | [e-cegjegyzek.hu](https://e-cegjegyzek.hu/), free manual                                                                              | No official REST API for external software                                                                                                                                                                                    |
| MANUAL           | Italy          | Numero REA / Partita IVA    | RM-123456    | [registroimprese.it](https://www.registroimprese.it/), free manual (name search)                                                      | Infocamere programmatic API is paywalled per call                                                                                                                                                                             |
| MANUAL           | Lithuania      | Įmonės kodas                | 123456789    | [registrucentras.lt/en](https://www.registrucentras.lt/en/), free manual                                                              | Full DB available via periodic open-data file downloads                                                                                                                                                                       |
| MANUAL           | Luxembourg     | Numéro RCS                  | B123456      | [Luxembourg Business Registers (lbr.lu)](https://www.lbr.lu/), free manual                                                            | Certified extracts paid                                                                                                                                                                                                       |
| MANUAL           | Malta          | Company Registration Number | C12345       | [Malta Business Registry](https://mbr.mt/), free manual                                                                               | Financial statement downloads incur fees                                                                                                                                                                                      |
| MANUAL           | Netherlands    | KVK-nummer                  | 12345678     | [kvk.nl/zoeken](https://www.kvk.nl/zoeken), free manual. Cheap paid API exists later: €6.40/mo + €0.02/query                          | —                                                                                                                                                                                                                             |
| MANUAL           | Poland         | Numer KRS                   | 0000123456   | [EkRS Portal](https://ekrs.ms.gov.pl/) / [biznes.gov.pl](https://www.biznes.gov.pl/en), free manual                                   | **No official free REST API for real-time KRS lookups, at any price.** Sole traders can be checked via the free CEIDG API instead                                                                                             |
| MANUAL           | Portugal       | NIPC                        | 512345678    | [ePortugal NIPC lookup](https://eportugal.gov.pt/), free, no account                                                                  | Official registration certificates paywalled                                                                                                                                                                                  |
| MANUAL           | Romania        | CUI                         | RO12345678   | [portal.onrc.ro](https://portal.onrc.ro/), free manual (requires registration)                                                        | Commercial RECOM API requires paid contract                                                                                                                                                                                   |
| MANUAL           | Spain          | NIF / CIF                   | A12345674    | [Registro Mercantil Central](https://www.rmc.es/), free manual (name search)                                                          | Formal Nota Simple extracts carry mandatory fees                                                                                                                                                                              |
| MANUAL           | Sweden         | Organisationsnummer         | 556677-8899  | [Bolagsverket](https://bolagsverket.se/), free manual                                                                                 | API tier exists but paid per call for expanded lookups                                                                                                                                                                        |

**10 markets with a genuine free API** (top of table) vs. **17 manual-only** (below). \* Belgium: official API is paid, not free corrected from an earlier draft of this doc. \*\* Latvia and Slovakia: free APIs confirmed, but both require an upfront application/approval step rather than instant self-serve access. \*\* Latvia and Slovakia: corrected upward from "manual only" after verification both have genuine free official APIs.

### 6.3 VIES limitation (don't build around this as a primary check)

[VIES](https://ec.europa.eu/taxation_customs/vies/) validates VAT registration only it is not a company registry substitute. Two structural gaps:

- **Coverage**: non-VAT-registered businesses, small enterprises under exemption thresholds, and holding entities don't appear at all.
- **Suppression**: Germany and Spain return only a binary valid/invalid no name or address, making automated matching impossible for those two countries specifically.

Use VIES only as a secondary check layered on top of the national registry number.

### 6.4 Paid fallback options (for when the Tier 3 trigger is hit)

Confirmed, priced options for the four markets flagged worth knowing now even though nothing here should be wired up before §4.3's trigger is hit.

| Market                    | Provider                                                                                                                           | What it does                                                                                                                                                                                                  | Pricing                                                                                                                                                           |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| India (GSTIN)             | [Cashfree GSTIN Verification](https://www.cashfree.com/docs/api-reference/vrs/v2/gstin/verify-gstin)                               | Validates a GSTIN and returns legal/trade name, registration date, status, business constitution, and address                                                                                                 | Not published on the docs page check Cashfree's pricing page directly                                                                                             |
| Nigeria (CAC)             | [Mono CAC Lookup v3](https://docs.mono.co/docs/lookup/cac-lookup) ([launch post](https://mono.co/blog/mono-cac-lookup-v3-is-live)) | Looks up by RC number or company name against 3.1M+ registered Nigerian businesses company basics, shareholders, directors/secretary, name history, address history                                           | Per-endpoint pricing (separate rate for company search vs. shareholder/director/history lookups) exact figures not shown in public docs, check their pricing page |
| Germany (Handelsregister) | [OpenAPI.com Company Start Germany](https://openapi.com/products/company-start-germany)                                            | Looks up by VAT number, company registration number, or internal ID returns legal name, trading alias, VAT code, registration number, address with GPS, status, LEI code, formation date, \~10 sec turnaround | €0.06+VAT/request monthly plan, or €0.055+VAT/request on the annual plan (5,000 calls/mo included). No free tier                                                  |
| United States             | [Cobalt Intelligence](https://cobaltintelligence.com/) Secretary of State API                                                      | Pulls live per-state SoS data, returns current status plus a timestamped, watermarked screenshot as documentary evidence, plus UCC data                                                                       | $0.50–$2.00/lookup (credit-based), 20 free lookups to test, \~3x cost for multi-state searches. Cheaper per-lookup than Middesk, no flat monthly minimum          |
| United States             | [Middesk Name & Address Verification](https://docs.middesk.com/verify-business/name-address)                                       | Specific endpoint (not the whole platform): confirms legal entity name, compares submitted DBA against known entities, and classifies the address (commercial/residential, virtual/mailbox, registered agent) | Same overall Middesk pricing as noted in §4.1 (\~$2–5/verification typical) this is one endpoint within that                                                      |

**Read on this**: Cobalt Intelligence is worth flagging as the better US Tier 3 default over Middesk specifically _because_ it's usage-based per-lookup with no flat monthly minimum ($300–500/mo minimums are common elsewhere) and includes primary-source evidence (the timestamped SoS screenshot) rather than just a pass/fail useful if you ever need to show _why_ a business was approved, not just that it was.

---

## 7. Open questions

- What's the actual expected volume mix across markets at launch does it change which Tier 1 integrations get built first?
- For Tier 2 (manual review) markets, who owns the review queue operationally, and what's the target SLA (e.g. within 24h)?
- Should "manual review pending" have a maximum wait time before it auto-escalates to a support ticket?
- Latvia/Slovakia API access requires an application step worth starting that process now given the lead time, even before volume justifies full automation?

## 8. Sources

- [UK Companies House API](https://developer.company-information.service.gov.uk/overview)
- [EU VIES](https://ec.europa.eu/taxation_customs/vies/)
- [Delaware Division of Corporations](https://icis.corp.delaware.gov)
- [ARES Technical Documentation](https://ares.gov.cz/swagger-ui/)
- [Danish CVR System-til-System Access](https://datacvr.virk.dk/artikel/system-til-system-adgang-til-cvr-data)
- [Estonia e-Business Register Open Data API](https://avaandmed.ariregister.rik.ee)
- [Finland PRH Open Data (avoindata.prh.fi)](https://avoindata.prh.fi/en/info/swagger-ui)
- [INSEE Sirene API](https://api.insee.fr)
- [Ireland CRO Open Data Portal](https://data.gov.ie/blog/cro-open-data-portal)
- [Croatia Sudreg Open Data Portal](https://sudreg-data.gov.hr)
- [Slovenia AJPES](https://www.ajpes.si)
- [Belgium KBO/BCE Public Search](https://kbopub.economie.fgov.be)
- [Belgium CBE API pricing/free-tier confirmation](https://cbeapi.be/en)
- [Latvia UR Registry API Web Services](https://www.ur.gov.lv/en/registry-api-web-services-service-list)
- [Slovakia RPO Register](https://slovak.statistics.sk)
- [Slovakia ORSF free aggregator API](https://orsf.sk/en/api)
- [Singapore ACRA Open Data Initiative](https://www.acra.gov.sg/resources/open-data-initiative/)
- [Singapore data.gov.sg ACRA collection](https://data.gov.sg/collections/2/view)
- [MCA Master Data Guide](https://vakilsearch.com/article/mca-master-data/)
- [Nigeria CAC Public Search](https://search.cac.gov.ng)
- [Poland KRS post-eKRS-2024 API guide](https://dev.to/openregistry/poland-krs-post-ekrs-2024-reality-api-guide-46df)
