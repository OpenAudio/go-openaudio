package location

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed *.sqlite3
var databases embed.FS

// LocationService provides location lookup functionality
type LocationService struct {
	citiesDB       *sql.DB
	statesDB       *sql.DB
	regionsDB      *sql.DB
	countriesDB    *sql.DB
	citiesQuery    *Queries
	statesQuery    *Queries
	regionsQuery   *Queries
	countriesQuery *Queries
}

// LatLong represents latitude and longitude coordinates
type LatLong struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// NewLocationService creates a new location service with embedded databases
func NewLocationService() (*LocationService, error) {
	service := &LocationService{}

	// Extract and open each database
	var err error

	service.citiesDB, err = openEmbeddedDB("cities.sqlite3")
	if err != nil {
		return nil, fmt.Errorf("failed to open cities database: %w", err)
	}
	service.citiesQuery = New(service.citiesDB)

	service.statesDB, err = openEmbeddedDB("states.sqlite3")
	if err != nil {
		service.citiesDB.Close()
		return nil, fmt.Errorf("failed to open states database: %w", err)
	}
	service.statesQuery = New(service.statesDB)

	service.regionsDB, err = openEmbeddedDB("regions.sqlite3")
	if err != nil {
		service.citiesDB.Close()
		service.statesDB.Close()
		return nil, fmt.Errorf("failed to open regions database: %w", err)
	}
	service.regionsQuery = New(service.regionsDB)

	service.countriesDB, err = openEmbeddedDB("countries.sqlite3")
	if err != nil {
		service.citiesDB.Close()
		service.statesDB.Close()
		service.regionsDB.Close()
		return nil, fmt.Errorf("failed to open countries database: %w", err)
	}
	service.countriesQuery = New(service.countriesDB)

	return service, nil
}

// openEmbeddedDB extracts an embedded database to a temporary file and opens it
func openEmbeddedDB(filename string) (*sql.DB, error) {
	// Read the embedded database
	data, err := databases.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded database %s: %w", filename, err)
	}

	// Create a temporary file
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("audiusd_%s", filename))

	// Write the database to the temporary file
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write temporary database file: %w", err)
	}

	// Open the database
	db, err := sql.Open("sqlite", tmpFile)
	if err != nil {
		os.Remove(tmpFile)
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the database connection
	if err := db.Ping(); err != nil {
		db.Close()
		os.Remove(tmpFile)
		return nil, fmt.Errorf("failed to ping database %s: %w", filename, err)
	}

	return db, nil
}

// GetLatLong retrieves latitude and longitude for a given city, state, and country
func (ls *LocationService) GetLatLong(ctx context.Context, city, state, country string) (*LatLong, error) {
	// Step 1: Get country code from country name
	countryCode, err := ls.countriesQuery.GetCountryCode(ctx, country)
	if err != nil {
		return nil, fmt.Errorf("failed to get country code for %s: %w", country, err)
	}
	if !countryCode.Valid {
		return nil, fmt.Errorf("country not found: %s", country)
	}

	// Step 2: Get state code from state name and country code
	stateCode, err := ls.statesQuery.GetStateCode(ctx, GetStateCodeParams{
		Name:        state,
		CountryCode: countryCode.String,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get state code for %s in %s: %w", state, country, err)
	}
	if !stateCode.Valid {
		return nil, fmt.Errorf("state not found: %s in %s", state, country)
	}

	// Step 3: Get latitude and longitude from city name, state code, and country code
	cityResult, err := ls.citiesQuery.GetCityLatLong(ctx, GetCityLatLongParams{
		Name:        city,
		StateCode:   stateCode.String,
		CountryCode: countryCode.String,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("city not found")
		}
		return nil, fmt.Errorf("failed to get coordinates for %s, %s, %s: %w", city, state, country, err)
	}

	return &LatLong{
		Latitude:  cityResult.Latitude,
		Longitude: cityResult.Longitude,
	}, nil
}

// Close closes all database connections and cleans up temporary files
func (ls *LocationService) Close() error {
	var errs []error

	if ls.citiesDB != nil {
		if err := ls.citiesDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if ls.statesDB != nil {
		if err := ls.statesDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if ls.regionsDB != nil {
		if err := ls.regionsDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if ls.countriesDB != nil {
		if err := ls.countriesDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing databases: %v", errs)
	}

	return nil
}
