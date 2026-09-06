-- One database per bounded context, plus a test counterpart for each.
--
-- Separate databases rather than separate schemas or one shared database:
-- PostgreSQL has no cross-database query without an extension, so a query
-- reaching from one context into another's tables fails as an unknown
-- relation instead of returning rows. The boundary is enforced by the
-- engine rather than by review attention.
--
-- The test counterparts are kept separate from their runtime databases
-- because the adapter tests TRUNCATE the tables they own before every run;
-- sharing a database with a running app service would wipe its real data.
-- That reasoning is per context, not global — a Video adapter test
-- truncating video_jobs has no business reaching Notification's rows
-- either.
--
-- "identity" itself is absent below: it is the server's POSTGRES_DB and
-- already exists by the time this script runs.
CREATE DATABASE identity_test;

CREATE DATABASE video;
CREATE DATABASE video_test;

CREATE DATABASE notification;
CREATE DATABASE notification_test;
