DROP TRIGGER IF EXISTS submission_sessions_set_updated_at ON waitlist.submission_sessions;
DROP TRIGGER IF EXISTS waitlist_submissions_soft_delete ON waitlist.waitlist_submissions;
DROP TRIGGER IF EXISTS waitlist_submissions_set_updated_at ON waitlist.waitlist_submissions;
DROP TRIGGER IF EXISTS team_sizes_set_updated_at ON waitlist.team_sizes;
DROP TRIGGER IF EXISTS roles_set_updated_at ON waitlist.roles;
DROP TRIGGER IF EXISTS hiring_frustrations_set_updated_at ON waitlist.hiring_frustrations;
DROP TRIGGER IF EXISTS hiring_tools_set_updated_at ON waitlist.hiring_tools;
DROP TRIGGER IF EXISTS industries_set_updated_at ON waitlist.industries;

DROP TABLE IF EXISTS waitlist.submission_sessions;
DROP TABLE IF EXISTS waitlist.submission_frustrations;
DROP TABLE IF EXISTS waitlist.submission_hiring_tools;
DROP TABLE IF EXISTS waitlist.submission_industries;
DROP TABLE IF EXISTS waitlist.waitlist_submissions;
DROP TABLE IF EXISTS waitlist.team_sizes;
DROP TABLE IF EXISTS waitlist.roles;
DROP TABLE IF EXISTS waitlist.hiring_frustrations;
DROP TABLE IF EXISTS waitlist.hiring_tools;
DROP TABLE IF EXISTS waitlist.industries;

DROP TRIGGER IF EXISTS waitlist_soft_delete ON waitlist.waitlist;
DROP TRIGGER IF EXISTS waitlist_set_updated_at ON waitlist.waitlist;
DROP TABLE IF EXISTS waitlist.waitlist;

DROP SCHEMA IF EXISTS waitlist;

DROP FUNCTION IF EXISTS soft_delete_row();
DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS pgcrypto;
