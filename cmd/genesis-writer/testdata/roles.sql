-- dp_schema.sql is a Cloud SQL dump: it hands the public schema and a few
-- pg_catalog grants to cloudsqlsuperuser, which a stock postgres image lacks.
CREATE ROLE cloudsqlsuperuser;
