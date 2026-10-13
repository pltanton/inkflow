package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

const (
	AIStatusSuccess = "success"
	AIStatusFailed  = "failed"
)

var (
	recordsBucket  = []byte("records")
	errRecordFound = errors.New("record found")
)

type Store struct {
	db *bbolt.DB
}

type Record struct {
	SourcePath    string    `json:"source_path"`
	SHA256        string    `json:"sha256"`
	ContentHash   string    `json:"content_hash,omitempty"`
	SourceModTime time.Time `json:"source_mod_time"`
	SourceSize    int64     `json:"source_size"`
	VaultPDFPath  string    `json:"vault_pdf_path"`
	VaultNotePath string    `json:"vault_note_path"`
	ImportedAt    time.Time `json:"imported_at"`

	// AI processing state. Empty AIStatus means AI was not configured for this
	// import. Existing records without these fields deserialise to zero values.
	AIStatus      string    `json:"ai_status"`
	AIRetryCount  int       `json:"ai_retry_count"`
	AILastError   string    `json:"ai_last_error"`
	AILastRetryAt time.Time `json:"ai_last_retry_at"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open state db %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(recordsBucket); err != nil {
			return err
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init state db %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) GetBySourcePath(p string) (*Record, error) {
	var out *Record
	err := s.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(recordsBucket).Get([]byte(p))
		if v == nil {
			return nil
		}
		var r Record
		if err := json.Unmarshal(v, &r); err != nil {
			return err
		}
		out = &r
		return nil
	})
	return out, err
}

func (s *Store) GetByHash(hash string) (*Record, error) {
	var out *Record
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(recordsBucket).ForEach(func(_, v []byte) error {
			var r Record
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if r.SHA256 == hash {
				out = &r
				return errRecordFound
			}
			return nil
		})
	})
	if err == errRecordFound {
		err = nil
	}
	return out, err
}

func (s *Store) Put(r *Record) error {
	return s.Save("", r)
}

func (s *Store) Save(previousSourcePath string, r *Record) error {
	if r == nil {
		return fmt.Errorf("nil record")
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		recB := tx.Bucket(recordsBucket)
		if previousSourcePath != "" && previousSourcePath != r.SourcePath {
			_ = recB.Delete([]byte(previousSourcePath))
		}
		if err := recB.Put([]byte(r.SourcePath), data); err != nil {
			return err
		}
		return nil
	})
}

// GetFailedAIImports returns all records whose AIStatus is "failed".
// It performs a full bucket scan. Records without an AIStatus field
// (legacy records) deserialise to an empty string and are not returned.
func (s *Store) GetFailedAIImports() ([]Record, error) {
	var out []Record
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(recordsBucket).ForEach(func(_, v []byte) error {
			var r Record
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if r.AIStatus == AIStatusFailed {
				out = append(out, r)
			}
			return nil
		})
	})
	if out == nil {
		out = []Record{}
	}
	return out, err
}
