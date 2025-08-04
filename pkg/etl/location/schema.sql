CREATE TABLE "cities" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "name" VARCHAR(255) NOT NULL,
  "state_id" MEDIUMINT NOT NULL,
  "state_code" VARCHAR(255) NOT NULL,
  "country_id" MEDIUMINT NOT NULL,
  "country_code" CHARACTER(2) NOT NULL,
  "latitude" DECIMAL NOT NULL,
  "longitude" DECIMAL NOT NULL,
  "created_at" DATETIME NOT NULL DEFAULT '2014-01-01 12:01:01',
  "updated_at" DATETIME NOT NULL DEFAULT 'CURRENT_TIMESTAMP',
  "flag" TINYINT NOT NULL DEFAULT '1',
  "wikiDataId" VARCHAR(255)
);

CREATE TABLE "states" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "name" VARCHAR(255) NOT NULL,
  "country_id" MEDIUMINT NOT NULL,
  "country_code" CHARACTER(2) NOT NULL,
  "fips_code" VARCHAR(255),
  "iso2" VARCHAR(255),
  "type" VARCHAR(191),
  "level" INTEGER,
  "parent_id" INTEGER,
  "native" VARCHAR(255),
  "latitude" DECIMAL,
  "longitude" DECIMAL,
  "created_at" DATETIME,
  "updated_at" DATETIME NOT NULL DEFAULT 'CURRENT_TIMESTAMP',
  "flag" TINYINT NOT NULL DEFAULT '1',
  "wikiDataId" VARCHAR(255)
);

CREATE TABLE "regions" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "name" VARCHAR(100) NOT NULL,
  "translations" TEXT,
  "created_at" DATETIME,
  "updated_at" DATETIME NOT NULL DEFAULT 'CURRENT_TIMESTAMP',
  "flag" TINYINT NOT NULL DEFAULT '1',
  "wikiDataId" VARCHAR(255)
);

CREATE TABLE "countries" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "name" VARCHAR(100) NOT NULL,
  "iso3" CHARACTER(3),
  "numeric_code" CHARACTER(3),
  "iso2" CHARACTER(2),
  "phonecode" VARCHAR(255),
  "capital" VARCHAR(255),
  "currency" VARCHAR(255),
  "currency_name" VARCHAR(255),
  "currency_symbol" VARCHAR(255),
  "tld" VARCHAR(255),
  "native" VARCHAR(255),
  "region" VARCHAR(255),
  "region_id" MEDIUMINT,
  "subregion" VARCHAR(255),
  "subregion_id" MEDIUMINT,
  "nationality" VARCHAR(255),
  "timezones" TEXT,
  "translations" TEXT,
  "latitude" DECIMAL,
  "longitude" DECIMAL,
  "emoji" VARCHAR(191),
  "emojiU" VARCHAR(191),
  "created_at" DATETIME,
  "updated_at" DATETIME NOT NULL DEFAULT 'CURRENT_TIMESTAMP',
  "flag" TINYINT NOT NULL DEFAULT '1',
  "wikiDataId" VARCHAR(255)
);
