package state

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

const (
	AIStatusPending = "pending"
	AIStatusSuccess = "succeeded"
	AIStatusFailed  = "failed"
)

var (
	recordsBucket      = []byte("records")
	hashIndexBucket    = []byte("hash_index")
	failedIndexBucket  = []byte("failed_index")
	pendingIndexBucket = []byte("pending_index")
)

type Store struct {
	db *bbolt.DB
}

type Record struct {
	SourcePath    string    `json:"source_path"`
	SHA256        string    `json:"sha256"`
	SourceModTime time.Time `json:"source_mod_time"`
	SourceSize    int64     `json:"source_size"`
	VaultPDFPath  string    `json:"vault_pdf_path"`
	VaultNotePath string    `json:"vault_note_path"`
	ImportedAt    time.Time `json:"imported_at"`

	// AIStatus is persisted as status. Records written before the asynchronous
	// queue used ai_status (or no status at all); UnmarshalJSON handles both.
	AIStatus      string    `json:"status"`
	AIRetryCount  int       `json:"ai_retry_count"`
	AILastError   string    `json:"ai_last_error"`
	AILastRetryAt time.Time `json:"ai_last_retry_at"`
	// AILastSuccessAt records when AI last completed successfully for this
	// record. Used by the reprocess-debounce feature to avoid re-running AI
	// on BOOX wrapper-only PDF rewrites. Zero value for legacy records or
	// records that never had a successful AI run.
	AILastSuccessAt time.Time `json:"ai_last_success_at"`
}

// UnmarshalJSON accepts the former ai_status field and treats records without
// either status as completed. This keeps pre-queue databases from being
// accidentally enqueued after upgrade.
func (r *Record) UnmarshalJSON(data []byte) error {
	type recordAlias Record
	var raw struct {
		recordAlias
		LegacyAIStatus string `json:"ai_status"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = Record(raw.recordAlias)
	if r.AIStatus == "" {
		if raw.LegacyAIStatus != "" {
			r.AIStatus = raw.LegacyAIStatus
		} else {
			r.AIStatus = AIStatusSuccess
		}
	}
	// The original synchronous implementation used "success".
	if r.AIStatus == "success" {
		r.AIStatus = AIStatusSuccess
	}
	return nil
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
		hashIndexMissing := tx.Bucket(hashIndexBucket) == nil
		failedIndexMissing := tx.Bucket(failedIndexBucket) == nil
		pendingIndexMissing := tx.Bucket(pendingIndexBucket) == nil
		if _, err := tx.CreateBucketIfNotExists(hashIndexBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(failedIndexBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(pendingIndexBucket); err != nil {
			return err
		}
		if !hashIndexMissing && !failedIndexMissing && !pendingIndexMissing {
			return nil
		}
		return tx.Bucket(recordsBucket).ForEach(func(_, v []byte) error {
			var r Record
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			return addRecordIndexes(tx, &r)
		})
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
		hashPaths := tx.Bucket(hashIndexBucket).Bucket(hashIndexKey(hash))
		if hashPaths == nil {
			return nil
		}
		_, sourcePath := hashPaths.Cursor().First()
		if sourcePath == nil {
			return nil
		}
		v := tx.Bucket(recordsBucket).Get(sourcePath)
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
			if err := removeRecordIndexesForSourcePath(tx, previousSourcePath); err != nil {
				return err
			}
			if err := recB.Delete([]byte(previousSourcePath)); err != nil {
				return err
			}
		}
		if err := removeRecordIndexesForSourcePath(tx, r.SourcePath); err != nil {
			return err
		}
		if err := recB.Put([]byte(r.SourcePath), data); err != nil {
			return err
		}
		return addRecordIndexes(tx, r)
	})
}

// Delete removes a record and its derived index entries atomically.
func (s *Store) Delete(sourcePath string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		recB := tx.Bucket(recordsBucket)
		if recB.Get([]byte(sourcePath)) == nil {
			return nil
		}
		if err := removeRecordIndexesForSourcePath(tx, sourcePath); err != nil {
			return err
		}
		return recB.Delete([]byte(sourcePath))
	})
}

// GetFailedAIImports returns all records whose AIStatus is "failed".
// Records without an AIStatus field (legacy records) are not returned.
func (s *Store) GetFailedAIImports() ([]Record, error) {
	var out []Record
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(failedIndexBucket).ForEach(func(_, sourcePath []byte) error {
			v := tx.Bucket(recordsBucket).Get(sourcePath)
			if v == nil {
				return nil
			}
			var r Record
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	})
	if out == nil {
		out = []Record{}
	}
	return out, err
}

// GetPendingAndFailedAIImports returns work owned by the background worker.
// Legacy records decode as succeeded and therefore are never selected.
func (s *Store) GetPendingAndFailedAIImports() ([]Record, error) {
	var out []Record
	err := s.db.View(func(tx *bbolt.Tx) error {
		for _, indexBucket := range [][]byte{pendingIndexBucket, failedIndexBucket} {
			if err := tx.Bucket(indexBucket).ForEach(func(_, sourcePath []byte) error {
				v := tx.Bucket(recordsBucket).Get(sourcePath)
				if v == nil {
					return nil
				}
				var r Record
				if err := json.Unmarshal(v, &r); err != nil {
					return err
				}
				out = append(out, r)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if out == nil {
		out = []Record{}
	}
	return out, err
}

func hashIndexKey(hash string) []byte {
	return []byte("hash:" + base64.RawURLEncoding.EncodeToString([]byte(hash)))
}

func addRecordIndexes(tx *bbolt.Tx, r *Record) error {
	hashPaths, err := tx.Bucket(hashIndexBucket).CreateBucketIfNotExists(hashIndexKey(r.SHA256))
	if err != nil {
		return err
	}
	sourcePath := []byte(r.SourcePath)
	if err := hashPaths.Put(sourcePath, sourcePath); err != nil {
		return err
	}
	if r.AIStatus == AIStatusFailed {
		return tx.Bucket(failedIndexBucket).Put(sourcePath, sourcePath)
	}
	if r.AIStatus == AIStatusPending {
		return tx.Bucket(pendingIndexBucket).Put(sourcePath, sourcePath)
	}
	return nil
}

func removeRecordIndexesForSourcePath(tx *bbolt.Tx, sourcePath string) error {
	recB := tx.Bucket(recordsBucket)
	v := recB.Get([]byte(sourcePath))
	if v == nil {
		return nil
	}
	var r Record
	if err := json.Unmarshal(v, &r); err != nil {
		return err
	}
	hashB := tx.Bucket(hashIndexBucket)
	hashKey := hashIndexKey(r.SHA256)
	if hashPaths := hashB.Bucket(hashKey); hashPaths != nil {
		if err := hashPaths.Delete([]byte(sourcePath)); err != nil {
			return err
		}
		firstKey, _ := hashPaths.Cursor().First()
		if firstKey == nil {
			if err := hashB.DeleteBucket(hashKey); err != nil {
				return err
			}
		}
	}
	if r.AIStatus == AIStatusFailed {
		if err := tx.Bucket(failedIndexBucket).Delete([]byte(sourcePath)); err != nil {
			return err
		}
	}
	if r.AIStatus == AIStatusPending {
		if err := tx.Bucket(pendingIndexBucket).Delete([]byte(sourcePath)); err != nil {
			return err
		}
	}
	return nil
}
