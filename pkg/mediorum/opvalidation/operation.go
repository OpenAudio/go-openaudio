package opvalidation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm/schema"
)

type AudioAnalysisResult struct {
	BPM float64 `json:"bpm"`
	Key string  `json:"key"`
}

type FFProbeResult struct {
	Format struct {
		Filename       string `json:"filename"`
		FormatName     string `json:"format_name"`
		FormatLongName string `json:"format_long_name"`
		Duration       string `json:"duration,omitempty"`
		Size           string `json:"size"`
		BitRate        string `json:"bit_rate,omitempty"`
	} `json:"format"`
}

type JobTemplate string

type Upload struct {
	ID string `json:"id"`

	UserWallet              sql.NullString       `json:"user_wallet"`
	Template                JobTemplate          `json:"template"`
	OrigFileName            string               `json:"orig_filename"`
	OrigFileCID             string               `json:"orig_file_cid"`
	SelectedPreview         sql.NullString       `json:"selected_preview"`
	FFProbe                 *FFProbeResult       `json:"probe"`
	Error                   string               `json:"error,omitempty"`
	ErrorCount              int                  `json:"error_count,omitempty"`
	Mirrors                 []string             `json:"mirrors"`
	TranscodedMirrors       []string             `json:"transcoded_mirrors"`
	Status                  string               `json:"status"`
	PlacementHosts          []string             `json:"placement_hosts"`
	CreatedBy               string               `json:"created_by"`
	CreatedAt               time.Time            `json:"created_at"`
	UpdatedAt               time.Time            `json:"updated_at"`
	TranscodedBy            string               `json:"transcoded_by"`
	TranscodeProgress       float64              `json:"transcode_progress"`
	TranscodedAt            time.Time            `json:"transcoded_at"`
	TranscodeResults        map[string]string    `json:"results"`
	AudioAnalysisStatus     string               `json:"audio_analysis_status"`
	AudioAnalysisError      string               `json:"audio_analysis_error,omitempty"`
	AudioAnalysisErrorCount int                  `json:"audio_analysis_error_count"`
	AudioAnalyzedBy         string               `json:"audio_analyzed_by"`
	AudioAnalyzedAt         time.Time            `json:"audio_analyzed_at"`
	AudioAnalysisResults    *AudioAnalysisResult `json:"audio_analysis_results"`
}

type StorageAndDbSize struct {
	LoggedAt           time.Time
	Host               string
	StorageBackend     string
	DbUsed             uint64
	MediorumDiskUsed   uint64
	MediorumDiskSize   uint64
	StorageExpectation uint64
	LastRepairSize     int64
	LastCleanupSize    int64
}

type QmAudioAnalysis struct {
	CID        string               `json:"cid"`
	Mirrors    []string             `json:"mirrors"`
	Status     string               `json:"status"`
	Error      string               `json:"error,omitempty"`
	ErrorCount int                  `json:"error_count"`
	AnalyzedBy string               `json:"analyzed_by"`
	AnalyzedAt time.Time            `json:"analyzed_at"`
	Results    *AudioAnalysisResult `json:"results"`
}

type AudioPreview struct {
	CID                 string `json:"cid"`
	SourceCID           string
	PreviewStartSeconds string
	CreatedBy           string    `json:"created_by"`
	CreatedAt           time.Time `json:"created_at"`
}

var mediorumTableTypes = map[string]reflect.Type{
	// Keep this list in sync with the models registered by mediorum/server dbMigrate.
	tableNameFor(Upload{}):           reflect.TypeOf(Upload{}),
	tableNameFor(StorageAndDbSize{}): reflect.TypeOf(StorageAndDbSize{}),
	tableNameFor(QmAudioAnalysis{}):  reflect.TypeOf(QmAudioAnalysis{}),
	tableNameFor(AudioPreview{}):     reflect.TypeOf(AudioPreview{}),
}

func tableNameFor(model interface{}) string {
	return schema.NamingStrategy{}.TableName(reflect.TypeOf(model).Name())
}

func ValidateOperation(ulidValue, host, action, table string, data []byte) error {
	if ulidValue == "" {
		return errors.New("mediorum operation missing ulid")
	}
	if _, err := ulid.ParseStrict(ulidValue); err != nil {
		return fmt.Errorf("invalid mediorum operation ulid: %w", err)
	}
	if host == "" {
		return errors.New("mediorum operation missing host")
	}
	return ValidatePayload(action, table, data)
}

func ValidatePayload(action, table string, data []byte) error {
	switch action {
	case crudr.ActionCreate, crudr.ActionUpdate, crudr.ActionDelete:
	default:
		return fmt.Errorf("unknown mediorum operation action %q", action)
	}
	if table == "" {
		return errors.New("mediorum operation missing table")
	}
	elemType, ok := mediorumTableTypes[table]
	if !ok {
		return fmt.Errorf("no type registered for %s", table)
	}
	if len(data) == 0 {
		return errors.New("mediorum operation missing data")
	}
	records := reflect.New(reflect.SliceOf(elemType)).Interface()
	if err := json.Unmarshal(data, records); err != nil {
		return fmt.Errorf("invalid mediorum operation data for %s: %w", table, err)
	}
	return nil
}
