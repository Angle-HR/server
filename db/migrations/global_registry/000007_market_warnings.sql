-- Selection warnings shown when an employer picks a market (job drafts, phase 1).
-- Wording is a starting point for counsel review; the shape is {code, title, message, severity}.

-- +goose Up
-- +goose StatementBegin
UPDATE hiring.markets SET selection_warnings = '[
  {"code":"us_pay_transparency","title":"US pay transparency","severity":"warning",
   "message":"Some US states and cities require a pay range in the posting, for example CA, CO, IL, NY, WA and New York City. Add the state, and the city for New York City, so the right rules apply."}
]'::jsonb WHERE code = 'US';

UPDATE hiring.markets SET selection_warnings = '[
  {"code":"in_dpdp","title":"India data protection","severity":"info",
   "message":"India''s DPDP Rules were notified on 13 November 2025 and the core duties apply from 13 May 2027. Whether applicants must consent is still being confirmed with counsel."}
]'::jsonb WHERE code = 'IN';

UPDATE hiring.markets SET selection_warnings = '[
  {"code":"ke_s50","title":"Kenya data protection","severity":"warning",
   "message":"Kenya''s Data Protection Act (section 50) may require applicant data to be kept in Kenya. Hiring in Kenya stays unavailable until legal advice is final."}
]'::jsonb WHERE code = 'KE';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE hiring.markets SET selection_warnings = '[]'::jsonb WHERE code IN ('US', 'IN', 'KE');
-- +goose StatementEnd
