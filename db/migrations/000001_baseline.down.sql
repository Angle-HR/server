DROP TRIGGER IF EXISTS waitlist_soft_delete ON waitlist.waitlist;
DROP TRIGGER IF EXISTS waitlist_set_updated_at ON waitlist.waitlist;
DROP TABLE IF EXISTS waitlist.waitlist;

DROP SCHEMA IF EXISTS waitlist;

DROP FUNCTION IF EXISTS soft_delete_row();
DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS pgcrypto;
