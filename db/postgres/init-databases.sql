-- Creates regional databases on a single Postgres instance.
-- anglehr_global is created via POSTGRES_DB; this runs as the anglehr superuser on first boot.
CREATE DATABASE anglehr_uk OWNER anglehr;
CREATE DATABASE anglehr_us OWNER anglehr;
CREATE DATABASE anglehr_africa OWNER anglehr;
CREATE DATABASE anglehr_eu OWNER anglehr;
CREATE DATABASE anglehr_asia OWNER anglehr;
