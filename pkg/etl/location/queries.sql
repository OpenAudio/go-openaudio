-- name: GetCountryCode :one
select iso2 from countries where name = ? limit 1;

-- name: GetStateCode :one
select iso2 from states where name = ? and country_code = ? limit 1;

-- name: GetCityLatLong :one
select latitude, longitude from cities where name = ? and state_code = ? and country_code = ? limit 1;
