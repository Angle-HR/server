# Reference

Product-specific catalogs for onboarding forms. These are **separate** from waitlist catalogs (`GET /industries`, `GET /roles`, etc.) which serve the pre-launch interest form.

Base path: `/api/v1/onboarding`

**Status:** Design only — not yet implemented.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/business-types` | What best describes your business? |
| `GET` | `/industries` | Select your industry |
| `GET` | `/company-roles` | Role at the company |

Countries reuse the existing waitlist endpoint: `GET /api/v1/countries`.

All catalog responses include `Cache-Control: public, max-age=300`.

## Response shapes

### Business type

```json
{
  "data": [
    {
      "id": "61000000-0000-4000-8000-000000000001",
      "name": "Early-stage startup",
      "slug": "early-stage-startup"
    }
  ]
}
```

### Industry

```json
{
  "data": [
    {
      "id": "62000000-0000-4000-8000-000000000001",
      "name": "Tech / Software",
      "slug": "tech-software",
      "emoji": "💻"
    }
  ]
}
```

### Company role

```json
{
  "data": [
    {
      "id": "60000000-0000-4000-8000-000000000001",
      "name": "Founder / CEO",
      "slug": "founder-ceo",
      "icon_key": "building"
    }
  ]
}
```

## Seed data: business_types

| UUID | Name | Slug | Sort |
|------|------|------|------|
| `61000000-0000-4000-8000-000000000001` | Early-stage startup | `early-stage-startup` | 1 |
| `61000000-0000-4000-8000-000000000002` | Small business | `small-business` | 2 |
| `61000000-0000-4000-8000-000000000003` | Mid-size company | `mid-size-company` | 3 |
| `61000000-0000-4000-8000-000000000004` | Enterprise | `enterprise` | 4 |
| `61000000-0000-4000-8000-000000000005` | Non-profit | `non-profit` | 5 |
| `61000000-0000-4000-8000-000000000006` | Freelancer / agency | `freelancer-agency` | 6 |

## Seed data: onboarding_industries

Aligned with UI mockups (15 options):

| UUID | Name | Slug | Emoji | Sort |
|------|------|------|-------|------|
| `62000000-0000-4000-8000-000000000001` | Tech / Software | `tech-software` | 💻 | 1 |
| `62000000-0000-4000-8000-000000000002` | Finance / Fintech | `finance-fintech` | 💰 | 2 |
| `62000000-0000-4000-8000-000000000003` | Retail / E-commerce | `retail-ecommerce` | 🛍️ | 3 |
| `62000000-0000-4000-8000-000000000004` | Hospitality / Food & Drink | `hospitality-food-drink` | 🍽️ | 4 |
| `62000000-0000-4000-8000-000000000005` | Professional Services | `professional-services` | 💼 | 5 |
| `62000000-0000-4000-8000-000000000006` | Beauty & Personal Care | `beauty-personal-care` | 💅 | 6 |
| `62000000-0000-4000-8000-000000000007` | Logistics / Transport | `logistics-transport` | 🚚 | 7 |
| `62000000-0000-4000-8000-000000000008` | Trades / Home Services | `trades-home-services` | 🛠️ | 8 |
| `62000000-0000-4000-8000-000000000009` | Real Estate / Property | `real-estate-property` | 🏠 | 9 |
| `62000000-0000-4000-8000-00000000000a` | Media / Creative | `media-creative` | 🎨 | 10 |
| `62000000-0000-4000-8000-00000000000b` | Health | `health` | 🩺 | 11 |
| `62000000-0000-4000-8000-00000000000c` | Education | `education` | 📚 | 12 |
| `62000000-0000-4000-8000-00000000000d` | Agriculture | `agriculture` | 🌾 | 13 |
| `62000000-0000-4000-8000-00000000000e` | Construction | `construction` | 🏗️ | 14 |
| `62000000-0000-4000-8000-00000000000f` | Others | `others` | — | 15 |

## Seed data: company_roles

Aligned with UI mockups (8 options):

| UUID | Name | Slug | Icon key | Sort |
|------|------|------|----------|------|
| `60000000-0000-4000-8000-000000000001` | Founder / CEO | `founder-ceo` | `building` | 1 |
| `60000000-0000-4000-8000-000000000002` | Engineer / Designer | `engineer-designer` | `code` | 2 |
| `60000000-0000-4000-8000-000000000003` | Marketing / Sales | `marketing-sales` | `megaphone` | 3 |
| `60000000-0000-4000-8000-000000000004` | HR / People | `hr-people` | `people` | 4 |
| `60000000-0000-4000-8000-000000000005` | Product | `product` | `box` | 5 |
| `60000000-0000-4000-8000-000000000006` | Customer Support | `customer-support` | `phone` | 6 |
| `60000000-0000-4000-8000-000000000007` | Operations | `operations` | `globe` | 7 |
| `60000000-0000-4000-8000-000000000008` | Others | `others` | `more` | 8 |

## Example

```bash
curl http://localhost:8080/api/v1/onboarding/industries
```

## Waitlist catalogs unchanged

Existing waitlist reference endpoints and seed data in `db/migrations/global_registry/000001_global_registry.up.sql` are not modified. Waitlist `industries`, `roles`, and `team_sizes` serve a different form with different labels.
