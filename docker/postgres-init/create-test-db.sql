-- Isolated database for the PostgreSQL-backed test suite. Kept separate
-- from the app's runtime "identity" database because the adapter tests
-- TRUNCATE identity_users before every run; sharing a database with a
-- running app service would wipe its real data.
CREATE DATABASE identity_test;
